package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

// Checker evaluates the merge conditions. *Service implements it.
type Checker interface {
	Check(ctx context.Context, hive domain.Hive, pr int64) (Readiness, error)
}

// Merger performs a merge after the conditions hold. *Service implements it.
type Merger interface {
	Merge(ctx context.Context, hive domain.Hive, pr int64) (Outcome, error)
}

// PolicyReader returns the active merge policy. *Service implements it.
type PolicyReader interface {
	Policy(ctx context.Context, hiveID string) (domain.MergePolicy, error)
}

// Scheduler turns attestation and CI events into one deduplicated merge_check
// job and performs what that job decided: merge when the policy opts in, or
// notify the owner's dispatcher once per (PR, SHA) when it does not.
type Scheduler struct {
	store    Store
	checker  Checker
	merger   Merger
	policies PolicyReader
	inserter Inserter
}

func NewScheduler(store Store, checker Checker, merger Merger, policies PolicyReader, inserter Inserter) *Scheduler {
	return &Scheduler{store: store, checker: checker, merger: merger, policies: policies, inserter: inserter}
}

// OnChecks matches router.Options.OnChecks. The conclusion is deliberately not
// trusted as a readiness signal: the check re-reads the full CI state, so a
// stale or partial webhook can never make a PR ready.
func (s *Scheduler) OnChecks(ctx context.Context, hive domain.Hive, pr int64, sha, _ string) error {
	if s == nil {
		return nil
	}
	return s.enqueue(ctx, hive.ID, pr, sha)
}

// OnAttestation is the wiring point used after an attestation is stored and its
// PR comment is written.
func (s *Scheduler) OnAttestation(ctx context.Context, hive domain.Hive, attestation domain.Attestation) error {
	if s == nil {
		return nil
	}
	return s.enqueue(ctx, hive.ID, attestation.PRNumber, attestation.HeadSHA)
}

func (s *Scheduler) enqueue(ctx context.Context, hiveID string, pr int64, sha string) error {
	if s.inserter == nil || hiveID == "" || pr <= 0 || sha == "" {
		return nil
	}
	return s.inserter.InsertMergeCheck(ctx, jobs.MergeCheck{HiveID: hiveID, PR: pr, SHA: sha})
}

// Handle runs one merge_check job. A stale job (the PR moved on) is a no-op.
func (s *Scheduler) Handle(ctx context.Context, job jobs.MergeCheck) error {
	if s == nil || s.store == nil || s.checker == nil || job.HiveID == "" || job.PR <= 0 || job.SHA == "" {
		return nil
	}
	hive, err := s.store.Hive(ctx, job.HiveID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	readiness, err := s.checker.Check(ctx, hive, job.PR)
	if err != nil {
		return err
	}
	if !readiness.Ready || readiness.HeadSHA != job.SHA {
		return nil
	}
	if s.policies == nil {
		return nil
	}
	policy, err := s.policies.Policy(ctx, hive.ID)
	if err != nil {
		return err
	}
	if policy.Auto {
		if s.merger == nil {
			return nil
		}
		_, err = s.merger.Merge(ctx, hive, job.PR)
		return err
	}
	return s.notifyReady(ctx, hive, readiness)
}

// notifyReady sends the single "可以合并" report per (PR, SHA). The stable
// message id makes retries and repeated job deliveries idempotent.
func (s *Scheduler) notifyReady(ctx context.Context, hive domain.Hive, readiness Readiness) error {
	to, chainID, err := s.dispatcher(ctx, hive, readiness.Pull.Number, readiness.Pull.HeadRef)
	if err != nil {
		return err
	}
	url := readiness.Pull.URL
	if url == "" {
		url = fmt.Sprintf("https://github.com/%s/pull/%d", hive.Repository, readiness.Pull.Number)
	}
	body := fmt.Sprintf("PR #%d（%s）的合并条件已满足，可以合并：%s",
		readiness.Pull.Number, shortSHA(readiness.HeadSHA), url)
	message := domain.Message{
		ID:      eventMessageID(hive.ID, readiness.Pull.Number, readiness.HeadSHA, "mergeable"),
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      to,
		Kind:    domain.MessageReport,
		ChainID: chainID,
		Body:    body,
		Trust:   domain.TrustSystem,
	}
	if _, err := s.store.SendMessage(ctx, message); err != nil && !errors.Is(err, domain.ErrConflict) {
		return err
	}
	return nil
}

// dispatcher reports who should hear "可以合并": the sender of the PR owner's
// newest task, then the Queen when enabled, then the Hive Inbox.
func (s *Scheduler) dispatcher(ctx context.Context, hive domain.Hive, pr int64, headRef string) (domain.Address, string, error) {
	owner, err := s.store.PRAuthorByBranch(ctx, hive.ID, headRef)
	if errors.Is(err, domain.ErrNotFound) {
		owner, err = s.store.PRAuthor(ctx, hive.ID, pr)
	}
	if err == nil && owner.ID != "" {
		sender, sendErr := s.latestTaskSender(ctx, hive.ID, domain.ForagerAddress(owner.ID))
		if sendErr != nil {
			return "", "", sendErr
		}
		if sender != "" && sender != domain.AddressSystem {
			return sender, owner.ChainID, nil
		}
		if hive.QueenEnabled {
			return domain.AddressQueen, owner.ChainID, nil
		}
		return domain.AddressHive, owner.ChainID, nil
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return "", "", err
	}
	if hive.QueenEnabled {
		return domain.AddressQueen, "", nil
	}
	return domain.AddressHive, "", nil
}

// latestTaskSender walks the owner's Inbox from oldest to newest and returns
// the sender of the newest kind=task message.
func (s *Scheduler) latestTaskSender(ctx context.Context, hiveID string, to domain.Address) (domain.Address, error) {
	const (
		pageSize = 100
		maxPages = 10
	)
	afterID := ""
	var sender domain.Address
	for page := 0; page < maxPages; page++ {
		msgs, err := s.store.Inbox(ctx, hiveID, to, afterID, pageSize)
		if err != nil {
			return "", err
		}
		for _, msg := range msgs {
			if msg.Kind == domain.MessageTask {
				sender = msg.From
			}
		}
		if len(msgs) < pageSize {
			break
		}
		afterID = msgs[len(msgs)-1].ID
	}
	return sender, nil
}

// eventMessageID is stable for one (hive, PR, SHA, kind), which is what makes
// the "可以合并" notification send-once.
func eventMessageID(hiveID string, pr int64, sha, kind string) string {
	sum := sha256.Sum256([]byte("merge\x00" + hiveID + "\x00" + strconv.FormatInt(pr, 10) + "\x00" + sha + "\x00" + kind))
	return "merge-" + hex.EncodeToString(sum[:16])
}
