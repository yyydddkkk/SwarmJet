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
	DroneByIssue(ctx context.Context, hiveID string, issue int64) (domain.Agent, error)
	DroneByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, error)
	DroneByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, error)
	CreateDrone(ctx context.Context, a domain.Agent) (domain.Agent, error)
	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)
}

// Action classifies the outcome of one routing decision.
type Action string

const (
	// ActionIgnored means the event was dropped without side effects.
	ActionIgnored Action = "ignored"
	// ActionDelivered means a message was written to a Drone Inbox.
	ActionDelivered Action = "delivered"
	// ActionDroneCreated means a trigger rule started a new Drone.
	ActionDroneCreated Action = "drone_created"
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
	DroneID    string         `json:"droneId,omitempty"`
	Target     domain.Address `json:"target,omitempty"`
	MessageID  string         `json:"messageId,omitempty"`
}

// Router routes normalized GitHub events. It is safe for concurrent use when
// the Backend is.
type Router struct {
	backend Backend
}

// New builds a Router over the given backend.
func New(backend Backend) *Router {
	return &Router{backend: backend}
}

// Route applies the deterministic routing rules from ADR section 9:
//
//  1. find the Hive for the repository (ignore unknown repositories);
//  2. a matching trigger rule starts a Drone (or reuses the Issue's Drone);
//  3. an event that belongs to an existing non-terminal Drone is delivered to it;
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

	if rule, ok := matchTrigger(cfg.Triggers, e); ok {
		return r.routeTrigger(ctx, hive, cfg, e, rule)
	}

	drone, found, err := r.owningDrone(ctx, hive.ID, e)
	if err != nil {
		return Decision{}, err
	}
	if found {
		return r.deliverToDrone(ctx, hive, e, drone, domain.MessageEvent, "")
	}
	return r.toInbox(ctx, hive, e, "")
}

// routeTrigger handles an event that matched a trigger rule. Authorization is
// checked first: a denied trigger never starts a Drone and is filed in the Hive
// Inbox with the reason attached.
func (r *Router) routeTrigger(ctx context.Context, hive domain.Hive, cfg domain.HiveConfig, e Event, rule domain.TriggerRule) (Decision, error) {
	if !triggerAuthorized(e.AuthorAssociation) {
		reason := fmt.Sprintf("author %q has association %q, need OWNER, MEMBER or COLLABORATOR", e.Author, e.AuthorAssociation)
		return r.toInbox(ctx, hive, e, reason)
	}
	if e.Number > 0 {
		drone, found, err := r.droneByIssue(ctx, hive.ID, e.Number)
		if err != nil {
			return Decision{}, err
		}
		if found {
			return r.deliverToDrone(ctx, hive, e, drone, domain.MessageTask, "reused existing Drone for issue")
		}
	}

	task := Digest(e)
	agent := domain.Agent{
		HiveID:      hive.ID,
		Role:        domain.RoleDrone,
		State:       domain.AgentCreating,
		Preset:      rule.Preset,
		CLI:         cfg.CLI,
		Task:        task,
		IssueNumber: e.Number,
		ChainID:     chainIDFor(e),
	}
	created, err := r.backend.CreateDrone(ctx, agent)
	if errors.Is(err, domain.ErrHiveLimit) || errors.Is(err, domain.ErrChainLimit) {
		return r.toInbox(ctx, hive, e, "cannot start Drone: "+err.Error())
	}
	if err != nil {
		return Decision{}, err
	}

	message := domain.Message{
		ID:        messageID(e, domain.DroneAddress(created.ID)),
		HiveID:    hive.ID,
		From:      domain.AddressSystem,
		To:        domain.DroneAddress(created.ID),
		Kind:      domain.MessageTask,
		ChainID:   created.ChainID,
		Body:      task,
		State:     domain.MessagePending,
		CreatedAt: created.CreatedAt,
	}
	sent, err := r.backend.SendMessage(ctx, message)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Action:     ActionDroneCreated,
		Reason:     "trigger " + rule.Event + " matched",
		Repository: e.Repository,
		HiveID:     hive.ID,
		DroneID:    created.ID,
		Target:     sent.To,
		MessageID:  sent.ID,
	}, nil
}

// owningDrone looks up a non-terminal Drone for the event, in the order the ADR
// gives: PR number, then branch, then Issue number. The number is interpreted
// according to the event family so an Issue number is never compared against
// PR numbers.
func (r *Router) owningDrone(ctx context.Context, hiveID string, e Event) (domain.Agent, bool, error) {
	if pr := prReference(e); pr > 0 {
		drone, found, err := r.droneByPR(ctx, hiveID, pr)
		if err != nil || found {
			return drone, found, err
		}
	}
	if branch := strings.TrimSpace(e.Branch); branch != "" {
		drone, found, err := r.droneByBranch(ctx, hiveID, branch)
		if err != nil || found {
			return drone, found, err
		}
	}
	if issue := issueReference(e); issue > 0 {
		drone, found, err := r.droneByIssue(ctx, hiveID, issue)
		if err != nil || found {
			return drone, found, err
		}
	}
	return domain.Agent{}, false, nil
}

func (r *Router) droneByIssue(ctx context.Context, hiveID string, issue int64) (domain.Agent, bool, error) {
	drone, err := r.backend.DroneByIssue(ctx, hiveID, issue)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Agent{}, false, nil
	}
	if err != nil {
		return domain.Agent{}, false, err
	}
	if isTerminal(drone.State) {
		return domain.Agent{}, false, nil
	}
	return drone, true, nil
}

func (r *Router) droneByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, bool, error) {
	drone, err := r.backend.DroneByPR(ctx, hiveID, pr)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Agent{}, false, nil
	}
	if err != nil {
		return domain.Agent{}, false, err
	}
	if isTerminal(drone.State) {
		return domain.Agent{}, false, nil
	}
	return drone, true, nil
}

func (r *Router) droneByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, bool, error) {
	drone, err := r.backend.DroneByBranch(ctx, hiveID, branch)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Agent{}, false, nil
	}
	if err != nil {
		return domain.Agent{}, false, err
	}
	if isTerminal(drone.State) {
		return domain.Agent{}, false, nil
	}
	return drone, true, nil
}

func (r *Router) deliverToDrone(ctx context.Context, hive domain.Hive, e Event, drone domain.Agent, kind domain.MessageKind, reason string) (Decision, error) {
	message := domain.Message{
		ID:      messageID(e, domain.DroneAddress(drone.ID)),
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      domain.DroneAddress(drone.ID),
		Kind:    kind,
		ChainID: drone.ChainID,
		Body:    Digest(e),
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
		DroneID:    drone.ID,
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

// matchTrigger returns the first trigger rule that matches the event.
func matchTrigger(rules []domain.TriggerRule, e Event) (domain.TriggerRule, bool) {
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
		}
	}
	return domain.TriggerRule{}, false
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
	if c.MaxConcurrentDrones <= 0 {
		c.MaxConcurrentDrones = 4
	}
	if c.MaxHops <= 0 {
		c.MaxHops = 10
	}
	if c.MaxDronesPerChain <= 0 {
		c.MaxDronesPerChain = 10
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
	if c.DroneMaxLifetimeHours <= 0 {
		c.DroneMaxLifetimeHours = 168
	}
	return c
}
