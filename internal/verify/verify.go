// Package verify schedules the read-only Foragers that verify a pull request
// head and routes their verdicts back to the owner or the dispatcher.
//
// It is deliberately independent of the concrete store and GitHub client: the
// scheduling logic depends on the narrow interfaces below, so the real store
// (round 4 task A) and the tests can provide their own implementations.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Store is the subset of the Hive store the verification scheduler uses. Every
// signature matches the shared round-4 contract verbatim.
type Store interface {
	Hive(ctx context.Context, id string) (domain.Hive, error)
	Agent(ctx context.Context, id string) (domain.Agent, error)
	CreateForager(ctx context.Context, a domain.Agent) (domain.Agent, error)
	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)
	Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error)

	RecordVerdict(ctx context.Context, v domain.Verdict) (domain.Verdict, error)
	SetVerdictComment(ctx context.Context, id, url string) error
	VerdictByVerifier(ctx context.Context, verifierID string) (domain.Verdict, error)
	SupersedeVerdicts(ctx context.Context, hiveID string, pr int64, keepSHA string) error
	CountVerdicts(ctx context.Context, hiveID, ownerID string, result domain.VerdictResult) (int, error)
	Verifiers(ctx context.Context, hiveID string, pr int64) ([]domain.Agent, error)
}

// GitHub is the optional GitHub side of verification: reading the current PR
// head and publishing the verdict comment. A nil GitHub client is allowed; the
// dispatcher then relies on the heads it observed through HeadChanged and skips
// the PR comment.
type GitHub interface {
	PRHead(ctx context.Context, repository string, pr int64) (string, error)
	CreatePRComment(ctx context.Context, repository string, pr int64, body string) (string, error)
}

// Options configures a Dispatcher.
type Options struct {
	Store  Store
	GitHub GitHub
	Logger *slog.Logger
	Now    func() time.Time
	// OnVerdict, when set, is called for every fresh, still-current verdict.
	// The merge entry point uses it to schedule a merge check.
	OnVerdict func(ctx context.Context, hive domain.Hive, verdict domain.Verdict) error
}

// Dispatcher owns the scheduling rules. It is safe for concurrent use when the
// Store is.
type Dispatcher struct {
	store     Store
	github    GitHub
	logger    *slog.Logger
	now       func() time.Time
	onVerdict func(context.Context, domain.Hive, domain.Verdict) error

	mu    sync.Mutex
	heads map[string]string // hive id + NUL + PR number -> latest observed head SHA
}

// New builds a Dispatcher.
func New(opts Options) *Dispatcher {
	if opts.Store == nil {
		panic("verify: nil store")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Dispatcher{
		store:     opts.Store,
		github:    opts.GitHub,
		logger:    logger,
		now:       now,
		onVerdict: opts.OnVerdict,
		heads:     map[string]string{},
	}
}

// HeadChanged reacts to a new PR head. It is idempotent for the same
// (PR, SHA): repeated calls never create a second verifier.
func (d *Dispatcher) HeadChanged(ctx context.Context, hive domain.Hive, owner domain.Agent, pr int64, sha string) error {
	cfg := resolveVerifyConfig(hive.Config.Verify)
	if cfg.Mode == "off" {
		return nil
	}
	sha = strings.TrimSpace(sha)
	if pr <= 0 || sha == "" {
		return nil
	}
	d.rememberHead(hive.ID, pr, sha)

	// Verdicts and verifiers for any other commit are stale as soon as a new
	// head exists.
	if err := d.store.SupersedeVerdicts(ctx, hive.ID, pr, sha); err != nil {
		return err
	}
	verifiers, err := d.store.Verifiers(ctx, hive.ID, pr)
	if err != nil {
		return err
	}
	matching := false
	for _, verifier := range verifiers {
		if strings.EqualFold(strings.TrimSpace(verifier.VerifySHA), sha) {
			matching = true
			continue
		}
		if err := d.cancelVerifier(ctx, hive, verifier, sha); err != nil {
			return err
		}
	}
	if matching {
		return nil
	}

	failures, err := d.store.CountVerdicts(ctx, hive.ID, owner.ID, domain.VerdictFail)
	if err != nil {
		return err
	}
	if failures >= cfg.MaxRounds {
		return d.notifyRoundLimit(ctx, hive, owner, pr, sha, failures)
	}

	id := stableID("verifier", hive.ID, strconv.FormatInt(pr, 10), sha)
	agent := domain.Agent{
		ID:        id,
		HiveID:    hive.ID,
		Role:      domain.RoleForager,
		State:     domain.AgentCreating,
		Kind:      domain.KindVerifier,
		Task:      verifierTask(hive, owner, pr, sha),
		Branch:    "verify/" + id,
		PRNumber:  pr,
		VerifySHA: sha,
		OwnerID:   owner.ID,
		ChainID:   owner.ChainID,
	}
	created, err := d.store.CreateForager(ctx, agent)
	if err != nil {
		// The deterministic ID makes a concurrent retry land on the same row.
		if errors.Is(err, domain.ErrConflict) {
			return nil
		}
		return err
	}
	taskID := stableID("verify-task", created.ID, sha)
	_, err = d.store.SendMessage(ctx, domain.Message{
		ID:      taskID,
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      domain.ForagerAddress(created.ID),
		Kind:    domain.MessageTask,
		ChainID: created.ChainID,
		Body:    created.Task,
	})
	return err
}

// SubmitVerdict records a verifier's conclusion, comments it on the PR and
// notifies the right person. It returns superseded=true when the verifier's SHA
// is no longer the PR head; the verdict is still recorded, but it is marked as
// superseded and no comment or follow-up is produced.
func (d *Dispatcher) SubmitVerdict(ctx context.Context, verifier domain.Agent, result domain.VerdictResult, summary string, findings []domain.Finding) (domain.Verdict, bool, error) {
	if verifier.Kind != domain.KindVerifier {
		return domain.Verdict{}, false, fmt.Errorf("submit_verdict is only available to verifier Foragers")
	}
	if verifier.HiveID == "" || verifier.PRNumber <= 0 || strings.TrimSpace(verifier.VerifySHA) == "" {
		return domain.Verdict{}, false, fmt.Errorf("verifier %s has no Hive, PR or head commit to submit", verifier.ID)
	}
	switch result {
	case domain.VerdictPass, domain.VerdictPassNotes, domain.VerdictFail, domain.VerdictBlocked:
	default:
		return domain.Verdict{}, false, fmt.Errorf("invalid verdict result %q", result)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return domain.Verdict{}, false, fmt.Errorf("summary is required")
	}
	summary = clampText(summary, maxSummaryRunes)
	findings = clampFindings(findings)

	hive, err := d.store.Hive(ctx, verifier.HiveID)
	if err != nil {
		return domain.Verdict{}, false, err
	}
	sha := strings.TrimSpace(verifier.VerifySHA)
	current, err := d.currentHead(ctx, hive, verifier.PRNumber, sha)
	if err != nil {
		return domain.Verdict{}, false, err
	}
	superseded := !strings.EqualFold(strings.TrimSpace(current), sha)

	existing, err := d.store.VerdictByVerifier(ctx, verifier.ID)
	existed := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.Verdict{}, false, err
	}
	if !existed {
		existing, err = d.store.RecordVerdict(ctx, domain.Verdict{
			ID:         stableID("verdict", hive.ID, strconv.FormatInt(verifier.PRNumber, 10), sha, verifier.ID),
			HiveID:     hive.ID,
			PRNumber:   verifier.PRNumber,
			HeadSHA:    sha,
			OwnerID:    verifier.OwnerID,
			VerifierID: verifier.ID,
			Result:     result,
			Summary:    summary,
			Findings:   findings,
			CreatedAt:  d.now(),
		})
		if err != nil {
			return domain.Verdict{}, false, err
		}
	}

	if superseded {
		if current != "" {
			if err := d.store.SupersedeVerdicts(ctx, hive.ID, verifier.PRNumber, current); err != nil {
				return existing, true, err
			}
		}
		existing.Superseded = true
		if updated, err := d.store.VerdictByVerifier(ctx, verifier.ID); err == nil {
			existing = updated
		}
		return existing, true, nil
	}
	if existing.Result == "" {
		existing.Result = result
	}
	if existing.Summary == "" {
		existing.Summary = summary
	}
	if len(existing.Findings) == 0 {
		existing.Findings = findings
	}

	if existing.CommentURL == "" && d.github != nil {
		body := RenderComment(existing)
		url, err := d.github.CreatePRComment(ctx, hive.Repository, existing.PRNumber, body)
		if err != nil {
			return existing, false, fmt.Errorf("verdict recorded but the PR comment failed: %w", err)
		}
		if err := d.store.SetVerdictComment(ctx, existing.ID, url); err != nil {
			return existing, false, err
		}
		existing.CommentURL = url
	}

	if err := d.followUp(ctx, hive, existing); err != nil {
		return existing, false, err
	}
	if d.onVerdict != nil {
		if err := d.onVerdict(ctx, hive, existing); err != nil {
			return existing, false, err
		}
	}
	return existing, false, nil
}

// cancelVerifier asks the supervisor to stop a verifier for an old commit.
// The message id is deterministic so a retried synchronize does not queue the
// same cancellation twice.
func (d *Dispatcher) cancelVerifier(ctx context.Context, hive domain.Hive, verifier domain.Agent, keepSHA string) error {
	_, err := d.store.SendMessage(ctx, domain.Message{
		ID:      stableID("verify-cancel", verifier.ID, keepSHA),
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      domain.ForagerAddress(verifier.ID),
		Kind:    domain.MessageControl,
		ChainID: verifier.ChainID,
		Body:    "cancel",
	})
	if err != nil && !errors.Is(err, domain.ErrConflict) {
		return err
	}
	return nil
}

func (d *Dispatcher) notifyRoundLimit(ctx context.Context, hive domain.Hive, owner domain.Agent, pr int64, sha string, rounds int) error {
	target := d.dispatcher(ctx, hive, owner.ID)
	report := roundLimitReport(hive, owner, pr, sha, rounds)
	if _, err := d.send(ctx, hive, target, domain.MessageReport, "", report, stableID("verify-limit-report", hive.ID, strconv.FormatInt(pr, 10), sha, string(target))); err != nil {
		return err
	}
	if owner.ID != "" {
		control := roundLimitControl(pr, sha, rounds)
		if _, err := d.send(ctx, hive, domain.ForagerAddress(owner.ID), domain.MessageControl, owner.ChainID, control, stableID("verify-limit-control", hive.ID, strconv.FormatInt(pr, 10), sha)); err != nil {
			return err
		}
	}
	return nil
}

func (d *Dispatcher) followUp(ctx context.Context, hive domain.Hive, verdict domain.Verdict) error {
	owner := d.owner(ctx, hive.ID, verdict.OwnerID)
	prLink := pullURL(hive, verdict.PRNumber)
	switch verdict.Result {
	case domain.VerdictFail:
		if verdict.OwnerID == "" {
			return nil
		}
		body := failMessage(hive, verdict, prLink)
		_, err := d.send(ctx, hive, domain.ForagerAddress(verdict.OwnerID), domain.MessageTask, owner.ChainID, body, stableID("verify-fail", verdict.ID, "owner"))
		return err
	case domain.VerdictPass, domain.VerdictPassNotes:
		target := d.dispatcher(ctx, hive, verdict.OwnerID)
		body := passMessage(verdict, prLink)
		_, err := d.send(ctx, hive, target, domain.MessageReport, "", body, stableID("verify-pass", verdict.ID, string(target)))
		return err
	case domain.VerdictBlocked:
		target := d.dispatcher(ctx, hive, verdict.OwnerID)
		body := blockedMessage(verdict, prLink)
		_, err := d.send(ctx, hive, target, domain.MessageReport, "", body, stableID("verify-blocked", verdict.ID, string(target)))
		return err
	default:
		return nil
	}
}

func (d *Dispatcher) send(ctx context.Context, hive domain.Hive, to domain.Address, kind domain.MessageKind, chainID, body, id string) (domain.Message, error) {
	return d.store.SendMessage(ctx, domain.Message{
		ID:      id,
		HiveID:  hive.ID,
		From:    domain.AddressSystem,
		To:      to,
		Kind:    kind,
		ChainID: chainID,
		Body:    clampText(body, domain.MaxMessageBody),
	})
}

func (d *Dispatcher) owner(ctx context.Context, hiveID, ownerID string) domain.Agent {
	if ownerID == "" {
		return domain.Agent{}
	}
	owner, err := d.store.Agent(ctx, ownerID)
	if err != nil {
		return domain.Agent{ID: ownerID, HiveID: hiveID}
	}
	return owner
}

// dispatcher resolves "the person who dispatched the owner" the same way the
// send tool does: the sender of the newest kind=task message in the owner's
// Inbox, then the Hive Inbox. The platform address is not a readable Inbox.
func (d *Dispatcher) dispatcher(ctx context.Context, hive domain.Hive, ownerID string) domain.Address {
	if ownerID == "" {
		return domain.AddressHive
	}
	sender, err := latestTaskSender(ctx, d.store, hive.ID, domain.ForagerAddress(ownerID))
	if err != nil {
		d.logger.Warn("verification: cannot read the owner Inbox", "owner", ownerID, "error", err)
		return domain.AddressHive
	}
	if sender == "" || sender == domain.AddressSystem {
		return domain.AddressHive
	}
	return sender
}

func latestTaskSender(ctx context.Context, store Store, hiveID string, to domain.Address) (domain.Address, error) {
	const (
		pageSize = 100
		maxPages = 10
	)
	afterID := ""
	var sender domain.Address
	for page := 0; page < maxPages; page++ {
		msgs, err := store.Inbox(ctx, hiveID, to, afterID, pageSize)
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

func (d *Dispatcher) currentHead(ctx context.Context, hive domain.Hive, pr int64, fallback string) (string, error) {
	// GitHub is authoritative when it is available: a webhook may have been
	// missed, and a verdict for an old commit must still be detected as stale.
	if d.github != nil {
		sha, err := d.github.PRHead(ctx, hive.Repository, pr)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(sha) != "" {
			d.rememberHead(hive.ID, pr, sha)
			return sha, nil
		}
	}
	if sha, ok := d.knownHead(hive.ID, pr); ok {
		return sha, nil
	}
	return fallback, nil
}

func (d *Dispatcher) rememberHead(hiveID string, pr int64, sha string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.heads[headKey(hiveID, pr)] = strings.TrimSpace(sha)
}

func (d *Dispatcher) knownHead(hiveID string, pr int64) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sha, ok := d.heads[headKey(hiveID, pr)]
	return sha, ok && sha != ""
}

func headKey(hiveID string, pr int64) string {
	return hiveID + "\x00" + strconv.FormatInt(pr, 10)
}

func resolveVerifyConfig(c domain.VerifyConfig) domain.VerifyConfig {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = "on"
	}
	if c.MaxRounds <= 0 {
		c.MaxRounds = 3
	}
	if c.MergeMethod == "" {
		c.MergeMethod = "squash"
	}
	return c
}

// stableID derives an idempotency key from the parts. The same call therefore
// targets the same row or message on every retry.
func stableID(prefix string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func clampText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}
