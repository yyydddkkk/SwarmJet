package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Check evaluates the active methodology's merge conditions for one PR. A
// non-ready result always carries at least one concrete reason. GitHub failures
// are returned as errors so callers can distinguish "not allowed" from "could
// not determine".
func (s *Service) Check(ctx context.Context, hive domain.Hive, pr int64) (Readiness, error) {
	var readiness Readiness
	if pr <= 0 {
		return readiness, fmt.Errorf("invalid pull request number %d", pr)
	}
	if strings.TrimSpace(hive.Repository) == "" {
		return readiness, errors.New("hive repository is empty")
	}
	if s.store == nil || s.attestations == nil || s.github == nil {
		return readiness, errors.New("merge service is not configured")
	}
	policy, err := s.Policy(ctx, hive.ID)
	if err != nil {
		return readiness, err
	}
	client, repositoryID, err := s.github.GitHub(ctx, hive.Repository)
	if err != nil {
		return readiness, fmt.Errorf("resolve GitHub client: %w", err)
	}
	pull, err := client.GetPullState(ctx, repositoryID, pr)
	if err != nil {
		return readiness, fmt.Errorf("read pull request: %w", err)
	}
	if pull.Number == 0 {
		pull.Number = pr
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

	switch {
	case pull.Merged:
		readiness.Reasons = append(readiness.Reasons, "pull request is already merged")
	case pull.State != "open":
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

	needAttestation := policy.RequireAttestation || policy.RequireNonAuthor
	if needAttestation {
		attestation, attestErr := s.attestations.LatestAttestation(ctx, hive.ID, pr)
		switch {
		case errors.Is(attestErr, domain.ErrNotFound):
			if policy.RequireAttestation {
				readiness.Reasons = append(readiness.Reasons, "no attestation for the current head")
			} else {
				readiness.Reasons = append(readiness.Reasons, "no non-author attestation for the current head")
			}
		case attestErr != nil:
			return readiness, fmt.Errorf("read latest attestation: %w", attestErr)
		default:
			readiness.AttestationFound = true
			readiness.Attestation = attestation
			// IsAuthor is computed by the platform when recording, but derive it
			// again here so a malformed or stale row can never bypass
			// requireNonAuthor.
			isAuthor := attestation.IsAuthor ||
				(attestation.AuthorID != "" && attestation.AuthorID == attestation.AttesterID)
			switch {
			case attestation.HeadSHA != pull.HeadSHA:
				readiness.Reasons = append(readiness.Reasons, fmt.Sprintf(
					"latest attestation is for %s, pull request head is %s",
					shortSHA(attestation.HeadSHA), shortSHA(pull.HeadSHA)))
			case !policyAccepts(policy, attestation.Result):
				readiness.Reasons = append(readiness.Reasons, fmt.Sprintf("latest attestation is %s", attestation.Result))
			case policy.RequireNonAuthor && isAuthor:
				readiness.Reasons = append(readiness.Reasons, "latest attestation is from the pull request author")
			}
		}
	}

	if pull.HeadSHA != "" {
		checks, reasons, checkErr := s.checkCI(ctx, client, repositoryID, pr, pull.HeadSHA)
		if checkErr != nil {
			// CI isn't a merge condition for this methodology, so an unreadable
			// CI endpoint must not block the merge entry. pr_status simply shows
			// no checks in that case.
			if policy.RequireChecks {
				return readiness, checkErr
			}
		} else {
			readiness.Checks = checks
			if policy.RequireChecks {
				readiness.Reasons = append(readiness.Reasons, reasons...)
			}
		}
	}

	if hasConflict(pull) {
		readiness.Reasons = append(readiness.Reasons, "pull request has a conflict with its base branch")
	}
	readiness.Ready = len(readiness.Reasons) == 0
	return readiness, nil
}

// checkCI reads both the branch's required checks and the head's check runs.
// With configured required checks GitHub's own answer is authoritative. With
// none, the fallback required by the contract is that no check on the head may
// have failed.
func (s *Service) checkCI(ctx context.Context, client GitHub, repositoryID, pr int64, sha string) ([]Check, []string, error) {
	required, err := client.RequiredChecks(ctx, repositoryID, pr, sha)
	if err != nil {
		return nil, nil, fmt.Errorf("read required checks: %w", err)
	}
	runs, err := client.CheckRuns(ctx, repositoryID, sha)
	if err != nil {
		return nil, nil, fmt.Errorf("read check runs: %w", err)
	}
	checks := make([]Check, 0, len(runs))
	for _, run := range runs {
		checks = append(checks, Check{
			Name:       run.Name,
			Status:     run.Status,
			Conclusion: run.Conclusion,
			URL:        firstNonEmpty(run.DetailsURL, run.HTMLURL),
			Output:     firstNonEmpty(run.Output.Summary, run.Output.Text, run.Output.Title),
		})
	}
	var reasons []string
	if required.RequiredCount > 0 {
		if !required.Ready {
			reason := required.Reason
			if reason == "" {
				reason = "required checks are not green"
			}
			reasons = append(reasons, reason)
		}
		return checks, reasons, nil
	}
	// RequiredChecks reports a moved head as Failed even when the branch has no
	// required checks. Treat that as a hard stop rather than falling through.
	if required.Failed && strings.Contains(required.Reason, "head changed") {
		return checks, []string{required.Reason}, nil
	}
	for _, run := range runs {
		if run.HeadSHA != "" && run.HeadSHA != sha {
			continue
		}
		if failedConclusion(run.Conclusion) {
			reasons = append(reasons, fmt.Sprintf("check %s failed (%s)", run.Name, run.Conclusion))
		}
	}
	return checks, reasons, nil
}

// Merge re-checks readiness, submits the merge pinned to the current head SHA
// and reads the result back. A merge queue entry is reported as queued, not
// merged. GitHub refusals are returned with their status so the caller can
// report them honestly.
func (s *Service) Merge(ctx context.Context, hive domain.Hive, pr int64) (Outcome, error) {
	if s.github == nil {
		return Outcome{}, errors.New("merge service is not configured")
	}
	readiness, err := s.Check(ctx, hive, pr)
	if err != nil {
		return Outcome{}, err
	}
	if !readiness.Ready {
		return Outcome{HeadSHA: readiness.HeadSHA}, &NotReadyError{Readiness: readiness}
	}
	policy, err := s.Policy(ctx, hive.ID)
	if err != nil {
		return Outcome{HeadSHA: readiness.HeadSHA}, err
	}
	method := policy.Method
	switch method {
	case "merge", "squash", "rebase":
	default:
		return Outcome{HeadSHA: readiness.HeadSHA}, fmt.Errorf("invalid merge method %q", method)
	}
	client, repositoryID, err := s.github.GitHub(ctx, hive.Repository)
	if err != nil {
		return Outcome{}, fmt.Errorf("resolve GitHub client: %w", err)
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
