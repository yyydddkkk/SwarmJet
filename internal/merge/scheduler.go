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

// Inserter enqueues the merge_check River job. *jobs.Client implements it.
type Inserter interface {
	InsertMergeCheck(ctx context.Context, job jobs.MergeCheck) error
}

// Scheduler turns CI and verdict events into one deduplicated merge_check job
// and performs the action that job decided: merge when the Hive opted in, or
// notify the dispatcher once per (PR, SHA) when it did not.
type Scheduler struct {
	store    Store
	gate     *Gate
	inserter Inserter
	opts     Options
}

// NewScheduler builds a scheduler. A nil inserter keeps the notification and
// merge path usable in tests but disables enqueueing.
func NewScheduler(store Store, gate *Gate, inserter Inserter, opts Options) *Scheduler {
	return &Scheduler{store: store, gate: gate, inserter: inserter, opts: opts}
}

// OnChecks matches router.Options.OnChecks (contract section 3). The conclusion
// is deliberately ignored: the gate re-reads the full CI state, so a stale or
// partial webhook can never make a PR ready.
func (s *Scheduler) OnChecks(ctx context.Context, hive domain.Hive, pr int64, sha, _ string) error {
	return s.enqueue(ctx, hive.ID, pr, sha)
}

// OnVerdict is the wiring point used by the verdict handler after a verdict is
// stored and its PR comment is written.
func (s *Scheduler) OnVerdict(ctx context.Context, hive domain.Hive, verdict domain.Verdict) error {
	return s.enqueue(ctx, hive.ID, verdict.PRNumber, verdict.HeadSHA)
}

func (s *Scheduler) enqueue(ctx context.Context, hiveID string, pr int64, sha string) error {
	if s.inserter == nil || hiveID == "" || pr <= 0 || sha == "" {
		return nil
	}
	return s.inserter.InsertMergeCheck(ctx, jobs.MergeCheck{HiveID: hiveID, PR: pr, SHA: sha})
}

// Handle runs one merge_check job. A stale job (the PR moved on) is a no-op.
func (s *Scheduler) Handle(ctx context.Context, job jobs.MergeCheck) error {
	if s.store == nil || s.gate == nil || job.HiveID == "" || job.PR <= 0 || job.SHA == "" {
		return nil
	}
	hive, err := s.store.Hive(ctx, job.HiveID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	readiness, err := s.gate.Check(ctx, hive, job.PR)
	if err != nil {
		return err
	}
	if !readiness.Ready || readiness.HeadSHA != job.SHA {
		return nil
	}
	if ResolveVerify(hive.Config.Verify).AutoMerge {
		_, err := s.gate.Merge(ctx, hive, job.PR)
		return err
	}
	return s.notifyReady(ctx, hive, readiness)
}

// notifyReady sends the single "可以合并" report per (PR, SHA). The stable
// message id makes retries and repeated job deliveries idempotent; the store
// returns the existing row for an identical retry.
func (s *Scheduler) notifyReady(ctx context.Context, hive domain.Hive, readiness Readiness) error {
	to, chainID, err := s.dispatcher(ctx, hive, readiness.Pull.Number)
	if err != nil {
		return err
	}
	url := readiness.Pull.URL
	if url == "" {
		url = fmt.Sprintf("https://github.com/%s/pull/%d", hive.Repository, readiness.Pull.Number)
	}
	body := fmt.Sprintf("PR #%d（%s）已通过独立验证且 CI 全绿，可以合并：%s",
		readiness.Pull.Number, shortSHA(readiness.HeadSHA), url)
	message := domain.Message{
		ID:      eventMessageID(hive.ID, readiness.Pull.Number, readiness.HeadSHA, "mergeable"),
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      to,
		Kind:    domain.MessageReport,
		ChainID: chainID,
		Body:    body,
	}
	if _, err := s.store.SendMessage(ctx, message); err != nil && !errors.Is(err, domain.ErrConflict) {
		return err
	}
	return nil
}

// dispatcher mirrors the round-3 send default: with a Queen enabled the Queen
// is the dispatcher; otherwise it is the sender of the owner's latest task
// message, falling back to the Hive Inbox.
func (s *Scheduler) dispatcher(ctx context.Context, hive domain.Hive, pr int64) (domain.Address, string, error) {
	if hive.QueenEnabled {
		return domain.AddressQueen, "", nil
	}
	owner, err := s.store.ForagerByPR(ctx, hive.ID, pr)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.AddressHive, "", nil
	}
	if err != nil {
		return "", "", err
	}
	chainID := owner.ChainID
	sender, err := s.latestTaskSender(ctx, hive.ID, domain.ForagerAddress(owner.ID))
	if err != nil {
		return "", "", err
	}
	if sender == "" || sender == domain.AddressSystem {
		return domain.AddressHive, chainID, nil
	}
	return sender, chainID, nil
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
// the "can merge" and conflict notifications send-once.
func eventMessageID(hiveID string, pr int64, sha, kind string) string {
	sum := sha256.Sum256([]byte("merge\x00" + hiveID + "\x00" + strconv.FormatInt(pr, 10) + "\x00" + sha + "\x00" + kind))
	return "merge-" + hex.EncodeToString(sum[:16])
}
