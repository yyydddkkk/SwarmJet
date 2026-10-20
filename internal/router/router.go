package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Backend is the subset of the Hive store the Router needs. Every method
// signature matches the shared contract verbatim so *store.Store can satisfy
// it in a later round.
type Backend interface {
	HiveByRepository(ctx context.Context, repository string) (domain.Hive, error)
	ForagerByIssue(ctx context.Context, hiveID string, issue int64) (domain.Agent, error)
	ForagerByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, error)
	ForagerByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, error)
	CreateForager(ctx context.Context, a domain.Agent) (domain.Agent, error)
	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)
}

// Action classifies the outcome of one routing decision.
type Action string

const (
	// ActionIgnored means the event was dropped without side effects.
	ActionIgnored Action = "ignored"
	// ActionDelivered means a message was written to a Forager Inbox.
	ActionDelivered Action = "delivered"
	// ActionForagerCreated means a trigger rule started a new Forager.
	ActionForagerCreated Action = "forager_created"
	// ActionInbox means the event was filed in the Hive Inbox.
	ActionInbox Action = "inbox"
)

// Decision describes what the Router did with an event. It is used for logging
// and is the primary assertion surface in tests.
type Decision struct {
	Action     Action         `json:"action"`
	Reason     string         `json:"reason,omitempty"`
	Repository string         `json:"repository,omitempty"`
	HiveID     string         `json:"hiveId,omitempty"`
	ForagerID  string         `json:"foragerId,omitempty"`
	Target     domain.Address `json:"target,omitempty"`
	MessageID  string         `json:"messageId,omitempty"`
}

// PRRecorder records the PR number a Forager opened. It matches the round-2
// store contract (SetAgentPR); Router detects it at runtime so the round-1 store
// keeps compiling and the link starts working once the data layer lands.
type PRRecorder interface {
	SetAgentPR(ctx context.Context, id string, pr int64) error
}

// MethodologyReader is the round-4 store method for the Hive's active
// methodology. It is optional so a store without the methodology migration
// keeps the HiveConfig.Triggers fallback.
type MethodologyReader interface {
	ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error)
}

// Methodology event names the Router understands. They are stable strings so a
// methodology can opt into platform events without the platform hard-coding a
// workflow.
const (
	MethodologyEventPRHead      = "pr_head"      // platform-pushed opened/reopened/synchronize
	MethodologyEventCheckFailed = "check_failed" // a failing CI conclusion
)

// eventRule is a methodology event or a legacy HiveConfig trigger in one
// shape. Role and Preset are empty for methodology and legacy rules
// respectively.
type eventRule struct {
	Event         string
	Label         string
	CommentPrefix string
	Role          string
	Preset        string
}

// Options configures optional Router behavior. The zero value is usable.
type Options struct {
	// AppSlug is the slug of the GitHub App the platform publishes with. When
	// set, pull_request events caused by that App's bot account can be
	// recognised from the sender alone. When empty the Router falls back to
	// sender.type == "Bot" plus ownership of the event branch.
	AppSlug string
	// AppSlugSource, when non-nil, is consulted for every event and takes
	// precedence over AppSlug. The Provider uses it so an App created at
	// runtime is recognised without rebuilding the Router.
	AppSlugSource func(context.Context) string
	// OnHead is called for a pull_request opened/reopened/synchronize caused by
	// the platform's own publisher. Those events are still not delivered to the
	// owner; the callback lets the platform supersede conclusions on old heads.
	OnHead func(ctx context.Context, hive domain.Hive, owner domain.Agent, pr int64, sha string) error
	// OnChecks is called whenever a check_suite or check_run completes, whether
	// it succeeded or failed. The Router itself still decides whether the event
	// wakes the owner: only failure-class conclusions are delivered.
	OnChecks func(ctx context.Context, hive domain.Hive, pr int64, sha string, conclusion string) error
}

// Router routes normalized GitHub events. It is safe for concurrent use when
// the Backend is.
type Router struct {
	backend       Backend
	prRecorder    PRRecorder
	appSlug       string
	appSlugSource func(context.Context) string
	onHead        func(context.Context, domain.Hive, domain.Agent, int64, string) error
	onChecks      func(context.Context, domain.Hive, int64, string, string) error
}

// New builds a Router over the given backend with default options.
func New(backend Backend) *Router {
	return NewWithOptions(backend, Options{})
}

// NewWithOptions builds a Router over the given backend and options.
func NewWithOptions(backend Backend, opts Options) *Router {
	r := &Router{
		backend:       backend,
		appSlug:       normalizeAppSlug(opts.AppSlug),
		appSlugSource: opts.AppSlugSource,
		onHead:        opts.OnHead,
		onChecks:      opts.OnChecks,
	}
	if recorder, ok := backend.(PRRecorder); ok {
		r.prRecorder = recorder
	}
	return r
}

// currentAppSlug resolves the slug for one routing decision. A dynamic source
// wins so a newly created GitHub App takes effect without restarting serve.
func (r *Router) currentAppSlug(ctx context.Context) string {
	if r.appSlugSource != nil {
		return normalizeAppSlug(r.appSlugSource(ctx))
	}
	return r.appSlug
}

// normalizeAppSlug accepts either "swarmjet" or "swarmjet[bot]" and returns the
// bare slug used to build the bot login.
func normalizeAppSlug(raw string) string {
	slug := strings.TrimSpace(raw)
	if len(slug) >= len("[bot]") && strings.EqualFold(slug[len(slug)-len("[bot]"):], "[bot]") {
		slug = strings.TrimSpace(slug[:len(slug)-len("[bot]")])
	}
	return slug
}

// Route applies the deterministic routing rules from ADR section 9:
//
//  1. find the Hive for the repository (ignore unknown repositories);
//  2. a matching trigger rule starts a Forager (or reuses the Issue's Forager);
//  3. an event that belongs to an existing non-terminal Forager is delivered to it;
//  4. everything else goes to the Hive Inbox.
func (r *Router) Route(ctx context.Context, e Event) (Decision, error) {
	repository := strings.TrimSpace(e.Repository)
	if repository == "" {
		return Decision{Action: ActionIgnored, Reason: "event has no repository"}, nil
	}
	hive, err := r.backend.HiveByRepository(ctx, repository)
	if errors.Is(err, domain.ErrNotFound) {
		return Decision{Action: ActionIgnored, Repository: repository, Reason: "no Hive for repository"}, nil
	}
	if err != nil {
		return Decision{}, err
	}
	cfg := resolveConfig(hive.Config)

	// Comments the platform posts itself (attestation summaries) are already
	// known to the Hive; routing them back would only add noise. Only the
	// configured App's bot is recognised, never other bots.
	if r.platformComment(ctx, e) {
		return Decision{Action: ActionIgnored, Reason: "comment posted by the platform's own GitHub App", Repository: e.Repository, HiveID: hive.ID}, nil
	}

	// The platform's own PR pushes never wake the PR owner. They may still be
	// routed by a methodology pr_head rule, which decides the target role.
	platformPR, platformDecision, err := r.platformGenerated(ctx, hive.ID, e)
	if err != nil {
		return Decision{}, err
	}
	if platformPR {
		if err := r.platformHeadChanged(ctx, hive, e); err != nil {
			return Decision{}, err
		}
	}
	rules, err := r.eventRules(ctx, hive.ID, cfg)
	if err != nil {
		return Decision{}, err
	}
	if platformPR {
		if rule, ok := matchRule(rules, e, true); ok {
			return r.routeTrigger(ctx, hive, cfg, e, rule)
		}
		return platformDecision, nil
	}
	if rule, ok := matchRule(rules, e, false); ok {
		return r.routeTrigger(ctx, hive, cfg, e, rule)
	}

	if e.Type == EventCheckRun || e.Type == EventCheckSuite {
		if r.onChecks != nil {
			if err := r.onChecks(ctx, hive, e.Number, e.SHA, e.Conclusion); err != nil {
				return Decision{}, err
			}
		}
		if !checkWakesOwner(e.Conclusion) {
			return Decision{
				Action:     ActionIgnored,
				Reason:     fmt.Sprintf("check conclusion %q does not wake the owner", e.Conclusion),
				Repository: e.Repository,
				HiveID:     hive.ID,
			}, nil
		}
	}

	forager, found, err := r.owningForager(ctx, hive.ID, e)
	if err != nil {
		return Decision{}, err
	}
	if found {
		reason := ""
		if r.shouldRecordPR(e, forager) {
			if err := r.prRecorder.SetAgentPR(ctx, forager.ID, e.Number); err != nil {
				return Decision{}, err
			}
			forager.PRNumber = e.Number
			reason = fmt.Sprintf("recorded PR #%d", e.Number)
		}
		return r.deliverToForager(ctx, hive, e, forager, domain.MessageEvent, reason)
	}
	return r.toInbox(ctx, hive, e, "")
}

// platformGenerated reports whether a pull_request event was caused by the
// platform's own publisher. Publishing a branch or opening/updating a PR would
// otherwise be delivered back to the Forager that just finished its turn, which
// would make it run once more with nothing to do.
//
// The decision uses structured sender fields only, never the body:
//   - with a configured App slug, the sender login must be "<slug>[bot]"
//     (or a Bot whose login is the bare slug);
//   - without a slug, any Bot sender is ignored only when the event branch is
//     owned by one of this Hive's Foragers.
//
// Human comments, reviews and CI results are never affected: those event types
// do not reach this helper.
func (r *Router) platformGenerated(ctx context.Context, hiveID string, e Event) (bool, Decision, error) {
	if e.Type != EventPullRequest || !platformPRAction(e.Action) {
		return false, Decision{}, nil
	}
	login := strings.TrimSpace(e.SenderLogin)
	appSlug := r.currentAppSlug(ctx)
	appBot := strings.EqualFold(e.SenderType, "Bot")
	if appSlug != "" {
		appBot = strings.EqualFold(login, appSlug+"[bot]") ||
			(appBot && strings.EqualFold(strings.TrimSuffix(login, "[bot]"), appSlug))
	}
	if !appBot {
		return false, Decision{}, nil
	}

	foragerID := ""
	if appSlug == "" {
		// Without the App slug, branch ownership is the only structured signal
		// that distinguishes our publisher from another bot.
		branch := strings.TrimSpace(e.Branch)
		if branch == "" {
			return false, Decision{}, nil
		}
		forager, found, err := r.foragerByBranch(ctx, hiveID, branch)
		if err != nil {
			return false, Decision{}, err
		}
		if !found {
			return false, Decision{}, nil
		}
		foragerID = forager.ID
	}

	sender := login
	if sender == "" {
		sender = "a bot"
	} else {
		sender = fmt.Sprintf("bot %q", sender)
	}
	reason := fmt.Sprintf("platform-generated pull_request.%s from %s; ignored so the publishing Forager does not run an empty turn", e.Action, sender)
	return true, Decision{
		Action:     ActionIgnored,
		Reason:     reason,
		Repository: e.Repository,
		HiveID:     hiveID,
		ForagerID:  foragerID,
	}, nil
}

// platformComment reports whether an issue or review comment was written by
// the platform's own App bot. Without a known App slug nothing is ignored.
func (r *Router) platformComment(ctx context.Context, e Event) bool {
	if e.Type != EventIssueComment && e.Type != EventPullRequestReviewComment {
		return false
	}
	slug := r.currentAppSlug(ctx)
	if slug == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(e.SenderLogin), slug+"[bot]")
}

// platformPRAction lists the PR lifecycle actions the platform itself emits
// when it publishes a Forager branch or updates the PR it opened.
func platformPRAction(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize", "edited":
		return true
	default:
		return false
	}
}

// platformHeadChanged calls OnHead for a new PR head published by the platform.
// The event itself is still reported as ignored and is never delivered to the
// owner: "edited" is only a metadata change and carries no new commit.
func (r *Router) platformHeadChanged(ctx context.Context, hive domain.Hive, e Event) error {
	if r.onHead == nil || e.Type != EventPullRequest || !headChangedAction(e.Action) {
		return nil
	}
	if strings.TrimSpace(e.SHA) == "" {
		return nil
	}
	owner, found, err := r.owningForager(ctx, hive.ID, e)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return r.onHead(ctx, hive, owner, e.Number, e.SHA)
}

func headChangedAction(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize":
		return true
	default:
		return false
	}
}

// checkWakesOwner implements the CI delivery rule: only a failure-class
// conclusion is an actionable event for the owner. Success, neutral, skipped,
// cancelled and startup-only conclusions are recorded as ignored instead.
func checkWakesOwner(conclusion string) bool {
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "failure", "timed_out", "action_required", "startup_failure":
		return true
	default:
		return false
	}
}

// shouldRecordPR links the Forager to a newly opened or reopened PR. It only
// fills a missing number, so a later event can never rewrite an existing link.
func (r *Router) shouldRecordPR(e Event, forager domain.Agent) bool {
	if r.prRecorder == nil || forager.PRNumber != 0 || e.Number <= 0 {
		return false
	}
	if e.Type != EventPullRequest {
		return false
	}
	return e.Action == "opened" || e.Action == "reopened"
}

// routeTrigger handles an event that matched a methodology or HiveConfig rule.
// Authorization applies to issue-triggered work only: platform events such as
// CI failures are routed by the methodology rule itself.
func (r *Router) routeTrigger(ctx context.Context, hive domain.Hive, cfg domain.HiveConfig, e Event, rule eventRule) (Decision, error) {
	issueEvent := rule.Event == "issue_labeled" || rule.Event == "issue_comment"
	if issueEvent && !triggerAuthorized(e.AuthorAssociation) {
		reason := fmt.Sprintf("author %q has association %q, need OWNER, MEMBER or COLLABORATOR", e.Author, e.AuthorAssociation)
		return r.toInbox(ctx, hive, e, reason)
	}
	if issueEvent && e.Number > 0 {
		forager, found, err := r.foragerByIssue(ctx, hive.ID, e.Number)
		if err != nil {
			return Decision{}, err
		}
		if found {
			return r.deliverToForager(ctx, hive, e, forager, domain.MessageTask, "reused existing Forager for issue")
		}
	}

	task := Digest(e)
	issueNumber := int64(0)
	if issueEvent {
		issueNumber = e.Number
	}
	agent := domain.Agent{
		HiveID:      hive.ID,
		Role:        domain.RoleForager,
		State:       domain.AgentCreating,
		Preset:      rule.Preset,
		CLI:         cfg.CLI,
		Task:        task,
		IssueNumber: issueNumber,
		ChainID:     chainIDFor(e),
		RoleName:    rule.Role,
	}
	var owner domain.Agent
	revisionEvent := rule.Event == MethodologyEventPRHead || rule.Event == MethodologyEventCheckFailed
	if revisionEvent {
		// Revision events only concern work this Hive owns: a failing check on
		// someone else's branch or PR must not spawn Agents. The new Agent
		// joins the owner's chain, so the chain limit bounds it too, and one
		// Agent per role and head commit is enough however many check runs or
		// deliveries report it.
		var found bool
		var err error
		owner, found, err = r.owningForager(ctx, hive.ID, e)
		if err != nil {
			return Decision{}, err
		}
		if !found || e.SHA == "" {
			return Decision{Action: ActionIgnored, Reason: rule.Event + " rule skipped: no Forager of this Hive owns the PR or branch", Repository: e.Repository, HiveID: hive.ID}, nil
		}
		prNumber := e.Number
		if prNumber == 0 {
			prNumber = owner.PRNumber
		}
		agent.ID = revisionAgentID(hive.ID, rule.Role, owner.ID, e.SHA)
		agent.PRNumber = prNumber
		agent.Revision = e.SHA
		agent.ParentID = owner.ID
		if owner.ChainID != "" {
			agent.ChainID = owner.ChainID
		}
	}
	created, err := r.backend.CreateForager(ctx, agent)
	if errors.Is(err, domain.ErrHiveLimit) || errors.Is(err, domain.ErrChainLimit) {
		return r.toInbox(ctx, hive, e, "cannot start Forager: "+err.Error())
	}
	if revisionEvent && errors.Is(err, domain.ErrConflict) {
		err = nil
		created = domain.Agent{}
	}
	if err != nil {
		return Decision{}, err
	}
	if revisionEvent && rule.Event == MethodologyEventCheckFailed {
		// The owner still hears about its failing CI (ADR section 9); the rule
		// only adds an Agent next to it.
		if _, err := r.deliverToForager(ctx, hive, e, owner, domain.MessageEvent, ""); err != nil {
			return Decision{}, err
		}
	}
	if created.ID == "" {
		return Decision{Action: ActionIgnored, Reason: rule.Event + " rule already started an Agent for this head commit", Repository: e.Repository, HiveID: hive.ID, ForagerID: owner.ID}, nil
	}

	message := domain.Message{
		ID:        messageID(e, domain.ForagerAddress(created.ID)),
		HiveID:    hive.ID,
		From:      domain.AddressSystem,
		To:        domain.ForagerAddress(created.ID),
		Kind:      domain.MessageTask,
		ChainID:   created.ChainID,
		Body:      task,
		Trust:     domain.TrustExternal,
		State:     domain.MessagePending,
		CreatedAt: created.CreatedAt,
	}
	sent, err := r.backend.SendMessage(ctx, message)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Action:     ActionForagerCreated,
		Reason:     "trigger " + rule.Event + " matched",
		Repository: e.Repository,
		HiveID:     hive.ID,
		ForagerID:  created.ID,
		Target:     sent.To,
		MessageID:  sent.ID,
	}, nil
}

// eventRules returns the rules the Hive currently uses. A methodology replaces
// HiveConfig.Triggers entirely, including when it declares no events; the
// legacy triggers are only a fallback while no methodology is active.
func (r *Router) eventRules(ctx context.Context, hiveID string, cfg domain.HiveConfig) ([]eventRule, error) {
	if reader, ok := r.backend.(MethodologyReader); ok {
		m, _, err := reader.ActiveMethodology(ctx, hiveID)
		if err == nil {
			rules := make([]eventRule, 0, len(m.Manifest.Events))
			for _, rule := range m.Manifest.Events {
				rules = append(rules, eventRule{
					Event:         rule.Event,
					Label:         rule.Label,
					CommentPrefix: rule.CommentPrefix,
					Role:          rule.Role,
				})
			}
			return rules, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	rules := make([]eventRule, 0, len(cfg.Triggers))
	for _, rule := range cfg.Triggers {
		rules = append(rules, eventRule{
			Event:         rule.Event,
			Label:         rule.Label,
			CommentPrefix: rule.CommentPrefix,
			Preset:        rule.Preset,
		})
	}
	return rules, nil
}

// owningForager looks up a non-terminal Forager for the event: the owner of
// the event branch first (it is the PR's author even when other Agents were
// spawned for the same PR), then the PR number, then the Issue number. The
// number is interpreted according to the event family so an Issue number is
// never compared against PR numbers.
func (r *Router) owningForager(ctx context.Context, hiveID string, e Event) (domain.Agent, bool, error) {
	if branch := strings.TrimSpace(e.Branch); branch != "" {
		forager, found, err := r.foragerByBranch(ctx, hiveID, branch)
		if err != nil || found {
			return forager, found, err
		}
	}
	if pr := prReference(e); pr > 0 {
		forager, found, err := r.foragerByPR(ctx, hiveID, pr)
		if err != nil || found {
			return forager, found, err
		}
	}
	if issue := issueReference(e); issue > 0 {
		forager, found, err := r.foragerByIssue(ctx, hiveID, issue)
		if err != nil || found {
			return forager, found, err
		}
	}
	return domain.Agent{}, false, nil
}

func (r *Router) foragerByIssue(ctx context.Context, hiveID string, issue int64) (domain.Agent, bool, error) {
	forager, err := r.backend.ForagerByIssue(ctx, hiveID, issue)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Agent{}, false, nil
	}
	if err != nil {
		return domain.Agent{}, false, err
	}
	if isTerminal(forager.State) {
		return domain.Agent{}, false, nil
	}
	return forager, true, nil
}

func (r *Router) foragerByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, bool, error) {
	forager, err := r.backend.ForagerByPR(ctx, hiveID, pr)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Agent{}, false, nil
	}
	if err != nil {
		return domain.Agent{}, false, err
	}
	if isTerminal(forager.State) {
		return domain.Agent{}, false, nil
	}
	return forager, true, nil
}

func (r *Router) foragerByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, bool, error) {
	forager, err := r.backend.ForagerByBranch(ctx, hiveID, branch)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Agent{}, false, nil
	}
	if err != nil {
		return domain.Agent{}, false, err
	}
	if isTerminal(forager.State) {
		return domain.Agent{}, false, nil
	}
	return forager, true, nil
}

func (r *Router) deliverToForager(ctx context.Context, hive domain.Hive, e Event, forager domain.Agent, kind domain.MessageKind, reason string) (Decision, error) {
	message := domain.Message{
		ID:      messageID(e, domain.ForagerAddress(forager.ID)),
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      domain.ForagerAddress(forager.ID),
		Kind:    kind,
		ChainID: forager.ChainID,
		Body:    Digest(e),
		Trust:   domain.TrustExternal,
		State:   domain.MessagePending,
	}
	sent, err := r.backend.SendMessage(ctx, message)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Action:     ActionDelivered,
		Reason:     reason,
		Repository: e.Repository,
		HiveID:     hive.ID,
		ForagerID:  forager.ID,
		Target:     sent.To,
		MessageID:  sent.ID,
	}, nil
}

func (r *Router) toInbox(ctx context.Context, hive domain.Hive, e Event, reason string) (Decision, error) {
	body := Digest(e)
	if reason != "" {
		body = "Not auto-routed: " + reason + "\n\n" + body
	}
	message := domain.Message{
		ID:      messageID(e, domain.AddressHive),
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      domain.AddressHive,
		Kind:    domain.MessageEvent,
		ChainID: chainIDFor(e),
		Body:    body,
		Trust:   domain.TrustExternal,
		State:   domain.MessagePending,
	}
	sent, err := r.backend.SendMessage(ctx, message)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Action:     ActionInbox,
		Reason:     reason,
		Repository: e.Repository,
		HiveID:     hive.ID,
		Target:     sent.To,
		MessageID:  sent.ID,
	}, nil
}

// matchRule returns the first rule that matches the event. platformPR says the
// event was caused by the platform's own push, which is the only case pr_head
// matches; owner delivery stays suppressed either way.
func matchRule(rules []eventRule, e Event, platformPR bool) (eventRule, bool) {
	for _, rule := range rules {
		switch rule.Event {
		case "issue_labeled":
			if e.Type != EventIssues || e.Action != "labeled" {
				continue
			}
			if rule.Label == "" || rule.Label != e.Label {
				continue
			}
			return rule, true
		case "issue_comment":
			if e.Type != EventIssueComment || e.Action != "created" {
				continue
			}
			prefix := strings.TrimSpace(rule.CommentPrefix)
			if prefix == "" {
				continue
			}
			if !strings.HasPrefix(strings.TrimSpace(e.Excerpt), prefix) {
				continue
			}
			return rule, true
		case MethodologyEventPRHead:
			if !platformPR || e.Type != EventPullRequest || !prHeadAction(e.Action) {
				continue
			}
			return rule, true
		case MethodologyEventCheckFailed:
			if !checkFailed(e) {
				continue
			}
			return rule, true
		}
	}
	return eventRule{}, false
}

// prHeadAction lists the platform push actions a pr_head rule may handle. The
// "edited" action is still ignored but is not a new head.
func prHeadAction(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize":
		return true
	default:
		return false
	}
}

// checkFailed reports whether the event is a failing CI result. Only failure
// classes count; successful checks are left to the merge machinery.
func checkFailed(e Event) bool {
	switch e.Type {
	case EventCheckRun, EventCheckSuite:
		switch strings.ToLower(strings.TrimSpace(e.Conclusion)) {
		case "failure", "timed_out", "action_required", "startup_failure":
			return true
		}
	case EventStatus:
		switch strings.ToLower(strings.TrimSpace(e.State)) {
		case "failure", "error":
			return true
		}
	}
	return false
}

// triggerAuthorized keeps the legacy intake rule: only repository members may
// start work from a label or a slash command.
func triggerAuthorized(association string) bool {
	switch strings.ToUpper(strings.TrimSpace(association)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

func isTerminal(state domain.AgentState) bool {
	switch state {
	case domain.AgentCompleted, domain.AgentFailed, domain.AgentCancelled:
		return true
	default:
		return false
	}
}

// prReference reports the PR number an event refers to, if any.
func prReference(e Event) int64 {
	switch e.Type {
	case EventPullRequest, EventPullRequestReview, EventPullRequestReviewComment, EventCheckSuite, EventCheckRun, EventStatus:
		return e.Number
	default:
		return 0
	}
}

// issueReference reports the Issue number an event refers to, if any.
func issueReference(e Event) int64 {
	switch e.Type {
	case EventIssues, EventIssueComment:
		return e.Number
	default:
		return 0
	}
}

// messageID derives the idempotency key from repository + delivery + target, as
// required by the contract. When a delivery ID is unavailable it falls back to
// the event's structured content so a byte-identical redelivery still dedupes.
func messageID(e Event, target domain.Address) string {
	key := e.Repository + "\x00" + e.DeliveryID + "\x00" + string(target)
	if e.DeliveryID == "" {
		key = "content\x00" + e.Repository + "\x00" + e.Type + "\x00" + e.Action + "\x00" +
			strconv.FormatInt(e.Number, 10) + "\x00" + e.Branch + "\x00" + e.Author + "\x00" +
			e.Excerpt + "\x00" + string(target)
	}
	sum := sha256.Sum256([]byte(key))
	return "gh-" + hex.EncodeToString(sum[:16])
}

// revisionAgentID is the stable ID of the Agent a revision rule starts for one
// role, owner and head commit, so repeated deliveries create it only once.
func revisionAgentID(hiveID, role, ownerID, sha string) string {
	sum := sha256.Sum256([]byte("revision-agent\x00" + hiveID + "\x00" + role + "\x00" + ownerID + "\x00" + sha))
	return "forager-" + hex.EncodeToString(sum[:12])
}

// chainIDFor derives a stable chain ID for work started by one delivery.
func chainIDFor(e Event) string {
	key := e.Repository + "\x00" + e.DeliveryID
	if e.DeliveryID == "" {
		key = "content\x00" + e.Repository + "\x00" + e.Type + "\x00" + e.Action + "\x00" +
			strconv.FormatInt(e.Number, 10) + "\x00" + e.Branch
	}
	sum := sha256.Sum256([]byte("chain\x00" + key))
	return "chain-" + hex.EncodeToString(sum[:12])
}

// resolveConfig fills the documented defaults for zero-valued Hive config.
func resolveConfig(c domain.HiveConfig) domain.HiveConfig {
	if c.CLI == "" {
		c.CLI = "opencode"
	}
	if c.MaxConcurrentForagers <= 0 {
		c.MaxConcurrentForagers = 4
	}
	if c.MaxHops <= 0 {
		c.MaxHops = 10
	}
	if c.MaxForagersPerChain <= 0 {
		c.MaxForagersPerChain = 10
	}
	if c.IdleParkMinutes <= 0 {
		c.IdleParkMinutes = 30
	}
	if c.HibernateHours <= 0 {
		c.HibernateHours = 24
	}
	if c.TurnTimeoutMinutes <= 0 {
		c.TurnTimeoutMinutes = 120
	}
	if c.ForagerMaxLifetimeHours <= 0 {
		c.ForagerMaxLifetimeHours = 168
	}
	return c
}
