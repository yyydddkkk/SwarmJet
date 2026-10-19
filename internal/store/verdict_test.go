package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func newTestVerifier(t *testing.T, s *Store, hiveID, ownerID, branch string, pr int64, sha string) domain.Agent {
	t.Helper()
	verifier, err := s.CreateForager(context.Background(), domain.Agent{
		HiveID:      hiveID,
		Kind:        domain.KindVerifier,
		State:       domain.AgentActive,
		PRNumber:    pr,
		IssueNumber: pr,
		VerifySHA:   sha,
		OwnerID:     ownerID,
		ChainID:     "chain-1",
		Branch:      branch,
	})
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	return verifier
}

func TestHiveVerifyDefaults(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/verify-defaults")
	got, err := s.Hive(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	verify := got.Config.Verify
	if verify.Mode != "on" || verify.MaxRounds != 3 || verify.MergeMethod != "squash" || verify.AutoMerge {
		t.Fatalf("verify defaults = %+v", verify)
	}
	if verify.Model != "" {
		t.Fatalf("verify model default = %q, want empty so the Forager model is used", verify.Model)
	}
}

func TestRecordVerdictIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/verdict-idempotent")
	owner, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: "chain-1"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := newTestVerifier(t, s, h.ID, owner.ID, "verify/idempotent", 7, "aaaa")

	first := domain.Verdict{
		HiveID:     h.ID,
		PRNumber:   7,
		HeadSHA:    "aaaa",
		OwnerID:    owner.ID,
		VerifierID: verifier.ID,
		Result:     domain.VerdictFail,
		Summary:    "first summary",
		Findings:   []domain.Finding{{Severity: "critical", Location: "pkg/x.go:12", Finding: "nil deref", Evidence: "test output"}},
		CreatedAt:  time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC),
	}
	stored, err := s.RecordVerdict(ctx, first)
	if err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}
	if stored.ID == "" || stored.Summary != first.Summary || len(stored.Findings) != 1 {
		t.Fatalf("stored verdict = %+v", stored)
	}

	duplicate, err := s.RecordVerdict(ctx, domain.Verdict{
		HiveID:     h.ID,
		PRNumber:   7,
		HeadSHA:    "aaaa",
		OwnerID:    owner.ID,
		VerifierID: verifier.ID,
		Result:     domain.VerdictPass,
		Summary:    "second summary",
	})
	if err != nil {
		t.Fatalf("duplicate RecordVerdict: %v", err)
	}
	if duplicate.ID != stored.ID || duplicate.Result != domain.VerdictFail || duplicate.Summary != first.Summary {
		t.Fatalf("duplicate did not return the stored verdict: %+v", duplicate)
	}

	var rows int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM verdicts WHERE verifier_id=$1`, verifier.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("verdict rows = %d, want 1", rows)
	}

	byVerifier, err := s.VerdictByVerifier(ctx, verifier.ID)
	if err != nil || byVerifier.ID != stored.ID {
		t.Fatalf("VerdictByVerifier = %+v, %v", byVerifier, err)
	}
	latest, err := s.LatestVerdict(ctx, h.ID, 7)
	if err != nil || latest.ID != stored.ID {
		t.Fatalf("LatestVerdict = %+v, %v", latest, err)
	}
}

func TestLatestVerdictSetCommentAndSupersede(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/verdict-supersede")
	owner, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	oldVerifier := newTestVerifier(t, s, h.ID, owner.ID, "verify/old", 9, "oldsha")
	newVerifier := newTestVerifier(t, s, h.ID, owner.ID, "verify/new", 9, "newsha")

	oldVerdict, err := s.RecordVerdict(ctx, domain.Verdict{
		HiveID: h.ID, PRNumber: 9, HeadSHA: "oldsha", OwnerID: owner.ID,
		VerifierID: oldVerifier.ID, Result: domain.VerdictFail, Summary: "old",
		CreatedAt: time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	newVerdict, err := s.RecordVerdict(ctx, domain.Verdict{
		HiveID: h.ID, PRNumber: 9, HeadSHA: "newsha", OwnerID: owner.ID,
		VerifierID: newVerifier.ID, Result: domain.VerdictPass, Summary: "new",
		CreatedAt: time.Date(2026, 10, 19, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err = s.SetVerdictComment(ctx, oldVerdict.ID, "https://github.com/acme/x/pull/9#issuecomment-1"); err != nil {
		t.Fatalf("SetVerdictComment: %v", err)
	}
	if err = s.SetVerdictComment(ctx, "missing", "https://example.invalid"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing comment update: %v", err)
	}
	latest, err := s.LatestVerdict(ctx, h.ID, 9)
	if err != nil || latest.ID != newVerdict.ID || latest.Result != domain.VerdictPass {
		t.Fatalf("LatestVerdict = %+v, %v", latest, err)
	}

	if err = s.SupersedeVerdicts(ctx, h.ID, 9, "newsha"); err != nil {
		t.Fatalf("SupersedeVerdicts: %v", err)
	}
	all, err := s.Verdicts(ctx, h.ID, 9)
	if err != nil || len(all) != 2 {
		t.Fatalf("Verdicts = %+v, %v", all, err)
	}
	byID := map[string]domain.Verdict{}
	for _, v := range all {
		byID[v.ID] = v
	}
	if !byID[oldVerdict.ID].Superseded {
		t.Error("old verdict was not superseded")
	}
	if byID[newVerdict.ID].Superseded {
		t.Error("verdict for the kept head was superseded")
	}
	if byID[oldVerdict.ID].CommentURL == "" {
		t.Error("comment URL was not persisted")
	}
	latest, err = s.LatestVerdict(ctx, h.ID, 9)
	if err != nil || latest.ID != newVerdict.ID {
		t.Fatalf("LatestVerdict after supersede = %+v, %v", latest, err)
	}

	if err = s.SupersedeVerdicts(ctx, h.ID, 9, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LatestVerdict(ctx, h.ID, 9); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("LatestVerdict with everything superseded: %v", err)
	}
}

func TestCountVerdictsIncludesSuperseded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/verdict-count")
	owner, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Free the Hive's concurrent slots; only the verifiers need to be live.
	for _, worker := range []domain.Agent{owner, other} {
		if err = s.TransitionAgent(ctx, worker.ID, domain.AgentCreating, domain.AgentCompleted); err != nil {
			t.Fatal(err)
		}
	}
	first := newTestVerifier(t, s, h.ID, owner.ID, "verify/count-1", 11, "sha1")
	second := newTestVerifier(t, s, h.ID, owner.ID, "verify/count-2", 11, "sha2")
	third := newTestVerifier(t, s, h.ID, other.ID, "verify/count-3", 12, "sha3")

	for _, v := range []domain.Verdict{
		{HiveID: h.ID, PRNumber: 11, HeadSHA: "sha1", OwnerID: owner.ID, VerifierID: first.ID, Result: domain.VerdictFail},
		{HiveID: h.ID, PRNumber: 11, HeadSHA: "sha2", OwnerID: owner.ID, VerifierID: second.ID, Result: domain.VerdictFail},
		{HiveID: h.ID, PRNumber: 12, HeadSHA: "sha3", OwnerID: other.ID, VerifierID: third.ID, Result: domain.VerdictPass},
	} {
		if _, err := s.RecordVerdict(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SupersedeVerdicts(ctx, h.ID, 11, "sha2"); err != nil {
		t.Fatal(err)
	}
	fails, err := s.CountVerdicts(ctx, h.ID, owner.ID, domain.VerdictFail)
	if err != nil || fails != 2 {
		t.Fatalf("fail count = %d, %v; want 2 (superseded included)", fails, err)
	}
	passes, err := s.CountVerdicts(ctx, h.ID, owner.ID, domain.VerdictPass)
	if err != nil || passes != 0 {
		t.Fatalf("owner pass count = %d, %v; want 0", passes, err)
	}
}

func TestVerifierDoesNotUseChainBudgetAndLookupsSkipVerifiers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/verifier-limits")
	const chain = "chain-shared"
	var lastWorker domain.Agent
	for i := 0; i < 10; i++ {
		a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: chain, PRNumber: 20})
		if err != nil {
			t.Fatalf("worker %d: %v", i+1, err)
		}
		if a.Kind != domain.KindWorker {
			t.Fatalf("worker kind = %q", a.Kind)
		}
		if err = s.TransitionAgent(ctx, a.ID, domain.AgentCreating, domain.AgentCompleted); err != nil {
			t.Fatal(err)
		}
		lastWorker = a
	}
	if _, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: chain}); !errors.Is(err, domain.ErrChainLimit) {
		t.Fatalf("worker chain limit not enforced: %v", err)
	}

	verifier := newTestVerifier(t, s, h.ID, lastWorker.ID, "verify/limits", 20, "deadbeef")
	if verifier.Kind != domain.KindVerifier || verifier.VerifySHA != "deadbeef" || verifier.OwnerID != lastWorker.ID {
		t.Fatalf("verifier round trip = %+v", verifier)
	}
	if _, err := s.ForagerByPR(ctx, h.ID, 20); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ForagerByPR returned a verifier or error: %v", err)
	}
	if _, err := s.ForagerByBranch(ctx, h.ID, verifier.Branch); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ForagerByBranch returned a verifier: %v", err)
	}
	if _, err := s.ForagerByIssue(ctx, h.ID, 20); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ForagerByIssue returned a verifier: %v", err)
	}

	liveVerifiers, err := s.Verifiers(ctx, h.ID, 20)
	if err != nil || len(liveVerifiers) != 1 || liveVerifiers[0].ID != verifier.ID {
		t.Fatalf("Verifiers = %+v, %v", liveVerifiers, err)
	}
	if err = s.TransitionAgent(ctx, verifier.ID, domain.AgentActive, domain.AgentCompleted); err != nil {
		t.Fatal(err)
	}
	if liveVerifiers, err = s.Verifiers(ctx, h.ID, 20); err != nil || len(liveVerifiers) != 0 {
		t.Fatalf("finished verifier still live: %+v, %v", liveVerifiers, err)
	}

	// A real worker for the same PR is still found by the event lookups.
	worker, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, PRNumber: 21, ChainID: "other-chain"})
	if err != nil {
		t.Fatalf("worker after verifier: %v", err)
	}
	byPR, err := s.ForagerByPR(ctx, h.ID, 21)
	if err != nil || byPR.ID != worker.ID {
		t.Fatalf("ForagerByPR = %+v, %v", byPR, err)
	}
	foragers, err := s.Foragers(ctx, h.ID)
	if err != nil || len(foragers) != 12 {
		t.Fatalf("Foragers = %d, %v; want 12 (workers plus verifier)", len(foragers), err)
	}
}

func TestVerifierCountsTowardConcurrentLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/verifier-concurrency")
	for i := 0; i < 4; i++ {
		if _, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: fmt.Sprintf("chain-%d", i)}); err != nil {
			t.Fatalf("live forager %d: %v", i+1, err)
		}
	}
	_, err := s.CreateForager(ctx, domain.Agent{
		HiveID: h.ID, Kind: domain.KindVerifier, PRNumber: 3, VerifySHA: "aaaa", Branch: "verify/limit",
	})
	if !errors.Is(err, domain.ErrHiveLimit) {
		t.Fatalf("verifier was not counted against MaxConcurrentForagers: %v", err)
	}
}
