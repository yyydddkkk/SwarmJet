package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
)

// Gate evaluates the merge contract for one Hive and PR.
type Gate struct {
	verdicts VerdictReader
	store    Store
	github   GitHubSource
	opts     Options
}

// NewGate builds a gate. All three dependencies are required.
func NewGate(verdicts VerdictReader, store Store, source GitHubSource, opts Options) *Gate {
	return &Gate{verdicts: verdicts, store: store, github: source, opts: opts}
}

// Check returns the readiness of one PR. A non-ready result always carries at
// least one concrete reason. GitHub/API failures are returned as errors so the
// caller can distinguish "not allowed" from "could not determine".
func (g *Gate) Check(ctx context.Context, hive domain.Hive, pr int64) (Readiness, error) {
	var readiness Readiness
	if pr <= 0 {
		return readiness, fmt.Errorf("invalid pull request number %d", pr)
	}
	if strings.TrimSpace(hive.Repository) == "" {
		return readiness, errors.New("hive repository is empty")
	}
	if g.verdicts == nil || g.store == nil || g.github == nil {
		return readiness, errors.New("merge gate is not configured")
	}
	client, err := g.github.GitHub(ctx, hive.Repository)
	if err != nil {
		return readiness, fmt.Errorf("resolve GitHub client: %w", err)
	}
	repositoryID, err := client.RepositoryID(ctx, hive.Repository)
	if err != nil {
		return readiness, fmt.Errorf("resolve repository id: %w", err)
	}
	pull, err := client.GetPullRequest(ctx, repositoryID, pr)
	if err != nil {
		return readiness, fmt.Errorf("read pull request: %w", err)
	}
	readiness = Readiness{
		HeadSHA: pull.HeadSHA,
		Pull: Pull{
			Number:         pull.Number,
			State:          pull.State,
			Draft:          pull.Draft,
			Merged:         pull.Merged,
			URL:            pull.URL,
			HeadSHA:        pull.HeadSHA,
			HeadRef:        pull.HeadRef,
			BaseRef:        pull.BaseRef,
			BaseRepository: pull.BaseRepository,
			Mergeable:      pull.Mergeable,
			MergeableState: pull.MergeableState,
		},
		Checks:  []Check{},
		Reasons: []string{},
	}

	if pull.Merged {
		readiness.Reasons = append(readiness.Reasons, "pull request is already merged")
	} else if pull.State != "open" {
		readiness.Reasons = append(readiness.Reasons, "pull request is not open")
	}
	if pull.Draft {
		readiness.Reasons = append(readiness.Reasons, "pull request is a draft")
	}
	if pull.BaseRepository != "" && !strings.EqualFold(pull.BaseRepository, hive.Repository) {
		readiness.Reasons = append(readiness.Reasons, fmt.Sprintf(
			"pull request targets %s, not the Hive repository %s", pull.BaseRepository, hive.Repository))
	}
	if pull.HeadSHA == "" {
		readiness.Reasons = append(readiness.Reasons, "pull request has no head commit")
	}

	verdict, verdictErr := g.verdicts.LatestVerdict(ctx, hive.ID, pr)
	if verdictErr == nil {
		readiness.VerdictFound = true
		readiness.Verdict = verdict
		switch {
		case verdict.HeadSHA != pull.HeadSHA:
			readiness.Reasons = append(readiness.Reasons, fmt.Sprintf(
				"latest verdict is for %s, pull request head is %s", shortSHA(verdict.HeadSHA), shortSHA(pull.HeadSHA)))
		case verdict.Result != domain.VerdictPass && verdict.Result != domain.VerdictPassNotes:
			readiness.Reasons = append(readiness.Reasons, fmt.Sprintf("latest verdict is %s", verdict.Result))
		}
	} else if errors.Is(verdictErr, domain.ErrNotFound) {
		readiness.Reasons = append(readiness.Reasons, "no verdict for the current head")
	} else {
		return readiness, fmt.Errorf("read latest verdict: %w", verdictErr)
	}

	if pull.HeadSHA != "" {
		checks, checkErr := g.checkCI(ctx, client, repositoryID, pr, pull.HeadSHA)
		if checkErr != nil {
			return readiness, checkErr
		}
		readiness.Checks = checks.checks
		readiness.Reasons = append(readiness.Reasons, checks.reasons...)
	}

	if hasConflict(pull) {
		readiness.Reasons = append(readiness.Reasons, "pull request has a conflict with its base branch")
		g.notifyConflict(ctx, hive, pull, verdict)
	}

	readiness.Ready = len(readiness.Reasons) == 0
	return readiness, nil
}

// Verdicts lists the full verdict history for a PR, including superseded rows.
func (g *Gate) Verdicts(ctx context.Context, hiveID string, pr int64) ([]domain.Verdict, error) {
	if g.verdicts == nil {
		return nil, errors.New("merge gate is not configured")
	}
	verdicts, err := g.verdicts.Verdicts(ctx, hiveID, pr)
	if errors.Is(err, domain.ErrNotFound) {
		return []domain.Verdict{}, nil
	}
	return verdicts, err
}

type checkEvaluation struct {
	checks  []Check
	reasons []string
}

// checkCI applies rule 3. With configured required checks GitHub's own answer is
// authoritative; without any, every completed check on the head must have
// passed and nothing may still be running.
func (g *Gate) checkCI(ctx context.Context, client GitHub, repositoryID, pr int64, sha string) (checkEvaluation, error) {
	required, err := client.RequiredChecks(ctx, repositoryID, pr, sha)
	if err != nil {
		return checkEvaluation{}, fmt.Errorf("read required checks: %w", err)
	}
	runs, err := client.CheckRuns(ctx, repositoryID, sha)
	if err != nil {
		return checkEvaluation{}, fmt.Errorf("read check runs: %w", err)
	}
	out := checkEvaluation{checks: make([]Check, 0, len(runs))}
	for _, run := range runs {
		out.checks = append(out.checks, Check{
			Name:       run.Name,
			Status:     run.Status,
			Conclusion: run.Conclusion,
			URL:        firstNonEmpty(run.DetailsURL, run.HTMLURL),
			Output:     firstNonEmpty(run.Output.Summary, run.Output.Text, run.Output.Title),
		})
	}

	if required.RequiredCount > 0 {
		if !required.Ready {
			reason := required.Reason
			if reason == "" {
				reason = "required checks are not green"
			}
			out.reasons = append(out.reasons, reason)
		}
		return out, nil
	}
	// RequiredChecks reports a moved head as Failed with no required checks.
	// Treat that as a hard stop instead of falling through to the fallback,
	// which could otherwise pass checks from the old head.
	if required.Failed && strings.Contains(required.Reason, "head changed") {
		out.reasons = append(out.reasons, required.Reason)
		return out, nil
	}

	completed := 0
	for _, run := range runs {
		if run.HeadSHA != "" && run.HeadSHA != sha {
			continue
		}
		if run.Status != "completed" {
			out.reasons = append(out.reasons, fmt.Sprintf("check %s is still %s", run.Name, run.Status))
			continue
		}
		completed++
		if !passedConclusion(run.Conclusion) {
			out.reasons = append(out.reasons, fmt.Sprintf("check %s failed (%s)", run.Name, run.Conclusion))
		}
	}
	if len(runs) == 0 {
		out.reasons = append(out.reasons, "no check runs for the current head")
	} else if completed == 0 {
		out.reasons = append(out.reasons, "no check has completed for the current head")
	}
	return out, nil
}

// Merge re-checks readiness, then submits the merge pinned to the current head
// SHA and reads the result back. A merge queue entry is reported as queued, not
// merged. GitHub refusals are returned unchanged (wrapped for context).
func (g *Gate) Merge(ctx context.Context, hive domain.Hive, pr int64) (Outcome, error) {
	if g.github == nil {
		return Outcome{}, errors.New("merge gate is not configured")
	}
	readiness, err := g.Check(ctx, hive, pr)
	if err != nil {
		return Outcome{}, err
	}
	if !readiness.Ready {
		return Outcome{HeadSHA: readiness.HeadSHA}, &NotReadyError{Readiness: readiness}
	}
	method, err := defaultMergeMethod(hive.Config.Verify)
	if err != nil {
		return Outcome{HeadSHA: readiness.HeadSHA}, err
	}
	client, err := g.github.GitHub(ctx, hive.Repository)
	if err != nil {
		return Outcome{}, fmt.Errorf("resolve GitHub client: %w", err)
	}
	repositoryID, err := client.RepositoryID(ctx, hive.Repository)
	if err != nil {
		return Outcome{}, fmt.Errorf("resolve repository id: %w", err)
	}
	result, err := client.RequestMerge(ctx, repositoryID, pr, readiness.HeadSHA, method)
	if err != nil {
		return Outcome{HeadSHA: readiness.HeadSHA, Method: method}, fmt.Errorf("GitHub refused the merge: %w", err)
	}
	outcome := Outcome{
		Merged:       result.Merged,
		Queued:       result.Queued,
		QueueEntryID: result.QueueEntryID,
		HeadSHA:      readiness.HeadSHA,
		Method:       method,
	}
	state, err := client.GetMergeState(ctx, repositoryID, pr, readiness.HeadSHA)
	if err != nil {
		return outcome, fmt.Errorf("merge was submitted but its state could not be read: %w", err)
	}
	outcome.Merged = outcome.Merged || state.Merged
	outcome.Queued = outcome.Queued || state.Queued
	if outcome.QueueEntryID == "" {
		outcome.QueueEntryID = state.QueueEntryID
	}
	if outcome.Merged {
		outcome.Queued = false
		return outcome, nil
	}
	if !outcome.Queued {
		return outcome, errors.New("GitHub did not merge or queue the pull request")
	}
	return outcome, nil
}

// notifyConflict tells the owner once per (PR, SHA) that it must merge the base
// branch before the PR can become ready. The deterministic message id makes
// repeated checks (including pr_status reads) idempotent.
func (g *Gate) notifyConflict(ctx context.Context, hive domain.Hive, pull github.PullRequest, verdict domain.Verdict) {
	owner, err := g.store.ForagerByPR(ctx, hive.ID, pull.Number)
	if err != nil && verdict.OwnerID != "" {
		owner, err = g.store.Agent(ctx, verdict.OwnerID)
	}
	if err != nil || owner.ID == "" {
		// No known owner: the conflict is still reported in readiness reasons.
		return
	}
	body := fmt.Sprintf("PR #%d 和 base 有冲突，请把 base 合进你的分支后结束本轮。", pull.Number)
	if pull.URL != "" {
		body += "\n" + pull.URL
	}
	message := domain.Message{
		ID:      eventMessageID(hive.ID, pull.Number, pull.HeadSHA, "conflict"),
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      domain.ForagerAddress(owner.ID),
		Kind:    domain.MessageTask,
		ChainID: owner.ChainID,
		Body:    body,
	}
	if _, err := g.store.SendMessage(ctx, message); err != nil && !errors.Is(err, domain.ErrConflict) {
		// Notification is best effort: failure must not turn a "not ready"
		// answer into an unrelated infrastructure error. The next check retries
		// with the same message id.
		return
	}
}

func hasConflict(pull github.PullRequest) bool {
	if pull.Mergeable != nil && !*pull.Mergeable {
		return true
	}
	switch strings.ToLower(pull.MergeableState) {
	case "dirty", "conflicting":
		return true
	default:
		return false
	}
}

func passedConclusion(conclusion string) bool {
	switch strings.ToLower(conclusion) {
	case "success", "neutral", "skipped":
		return true
	default:
		return false
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
