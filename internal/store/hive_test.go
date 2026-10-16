package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func newTestHive(t *testing.T, s *Store, repository string) domain.Hive {
	t.Helper()
	h, err := s.CreateHive(context.Background(), domain.Hive{Name: "Widgets", Repository: repository})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCreateHiveCreatesDisabledQueenAndDefaults(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Widgets", Repository: "acme/widgets"})
	if err != nil {
		t.Fatal(err)
	}
	if h.ID == "" || h.Version != 1 || h.CreatedAt.IsZero() {
		t.Fatalf("unexpected hive: %+v", h)
	}
	cfg := h.Config
	if cfg.CLI != "opencode" || cfg.MaxConcurrentForagers != 4 || cfg.MaxHops != 10 || cfg.MaxForagersPerChain != 10 ||
		cfg.IdleParkMinutes != 30 || cfg.HibernateHours != 24 || cfg.TurnTimeoutMinutes != 120 || cfg.ForagerMaxLifetimeHours != 168 {
		t.Fatalf("defaults not applied on create: %+v", cfg)
	}
	got, err := s.Hive(ctx, h.ID)
	if err != nil || got.Repository != "acme/widgets" {
		t.Fatalf("%+v %v", got, err)
	}
	if got.Config.MaxConcurrentForagers != 4 || got.Config.MaxHops != 10 {
		t.Fatalf("defaults not applied on read: %+v", got.Config)
	}
	byRepo, err := s.HiveByRepository(ctx, "acme/widgets")
	if err != nil || byRepo.ID != h.ID {
		t.Fatalf("%+v %v", byRepo, err)
	}
	hives, err := s.Hives(ctx)
	if err != nil || len(hives) != 1 {
		t.Fatalf("%+v %v", hives, err)
	}
	queen, err := s.Queen(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queen.Role != domain.RoleQueen || queen.State != domain.AgentDisabled {
		t.Fatalf("queen not created disabled: %+v", queen)
	}
	if _, err = s.CreateHive(ctx, domain.Hive{Name: "Dup", Repository: "acme/widgets"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate repository accepted: %v", err)
	}
	if _, err = s.Hive(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing hive: %v", err)
	}
}

func TestUpdateHiveVersionConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	stale := h
	h.Name = "Renamed"
	h.Config.MaxConcurrentForagers = 7
	saved, err := s.UpdateHive(ctx, h)
	if err != nil || saved.Version != 2 || saved.Config.MaxConcurrentForagers != 7 {
		t.Fatalf("%+v %v", saved, err)
	}
	stale.Name = "Stale"
	if _, err = s.UpdateHive(ctx, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	got, err := s.Hive(ctx, h.ID)
	if err != nil || got.Name != "Renamed" || got.Version != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = s.UpdateHive(ctx, domain.Hive{ID: "missing", Version: 1}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing hive update: %v", err)
	}
}

func TestCreateForagerConcurrencyLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	const attempts = 24
	results := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct chains so only the Hive concurrency limit can trigger.
			_, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: fmt.Sprintf("chain-%d", i)})
			results[i] = err
		}(i)
	}
	wg.Wait()
	ok, limited := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrHiveLimit):
			limited++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 4 || limited != attempts-4 {
		t.Fatalf("ok=%d limited=%d", ok, limited)
	}
	foragers, err := s.Foragers(ctx, h.ID)
	if err != nil || len(foragers) != 4 {
		t.Fatalf("foragers=%d err=%v", len(foragers), err)
	}
}

func TestCreateForagerChainLimitAndBranch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	chain := "chain-shared"
	for i := 0; i < 10; i++ {
		a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: chain})
		if err != nil {
			t.Fatalf("create %d: %v", i+1, err)
		}
		if a.Branch != "swarm/"+h.ID+"/"+a.ID {
			t.Fatalf("branch %q", a.Branch)
		}
		// Finish it so the concurrency limit does not mask the chain limit.
		if err = s.TransitionAgent(ctx, a.ID, domain.AgentCreating, domain.AgentCompleted); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: chain}); !errors.Is(err, domain.ErrChainLimit) {
		t.Fatalf("chain limit not enforced: %v", err)
	}
	if _, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: "other"}); err != nil {
		t.Fatalf("different chain rejected: %v", err)
	}
}

func TestTransitionAgentCompareAndSwap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	if a.State != domain.AgentCreating {
		t.Fatalf("state %s", a.State)
	}
	if err = s.TransitionAgent(ctx, a.ID, domain.AgentCreating, domain.AgentActive); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionAgent(ctx, a.ID, domain.AgentCreating, domain.AgentActive); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale transition accepted: %v", err)
	}
	if err = s.TransitionAgent(ctx, "missing", domain.AgentActive, domain.AgentParked); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing agent: %v", err)
	}
	got, err := s.Agent(ctx, a.ID)
	if err != nil || got.State != domain.AgentActive {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestForagerLookupsAndHandoff(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/a")
	first, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, IssueNumber: 7, ChainID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	byIssue, err := s.ForagerByIssue(ctx, h.ID, 7)
	if err != nil || byIssue.ID != first.ID {
		t.Fatalf("%+v %v", byIssue, err)
	}
	if err = s.TransitionAgent(ctx, first.ID, domain.AgentCreating, domain.AgentCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ForagerByIssue(ctx, h.ID, 7); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("terminal forager returned for issue: %v", err)
	}
	second, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, PRNumber: 42, Branch: "swarm/x/y"})
	if err != nil {
		t.Fatal(err)
	}
	byPR, err := s.ForagerByPR(ctx, h.ID, 42)
	if err != nil || byPR.ID != second.ID {
		t.Fatalf("%+v %v", byPR, err)
	}
	byBranch, err := s.ForagerByBranch(ctx, h.ID, "swarm/x/y")
	if err != nil || byBranch.ID != second.ID {
		t.Fatalf("%+v %v", byBranch, err)
	}
	creating, err := s.Foragers(ctx, h.ID, domain.AgentCreating)
	if err != nil || len(creating) != 1 || creating[0].ID != second.ID {
		t.Fatalf("%+v %v", creating, err)
	}
	all, err := s.Foragers(ctx, h.ID)
	if err != nil || len(all) != 2 {
		t.Fatalf("%+v %v", all, err)
	}
	handoff := domain.Handoff{Goal: "goal", Done: "done", Remaining: "rest", Risks: "risks", Next: "next",
		Artifacts: []domain.ArtifactRef{{Kind: "pr", Ref: "42"}}}
	if err = s.UpdateHandoff(ctx, second.ID, handoff); err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(ctx, second.ID)
	if err != nil || got.Handoff == nil || got.Handoff.Goal != "goal" || len(got.Handoff.Artifacts) != 1 {
		t.Fatalf("%+v %v", got.Handoff, err)
	}
	if err = s.UpdateHandoff(ctx, "missing", handoff); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing handoff: %v", err)
	}
}
