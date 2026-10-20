package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func methodologyOutcome(t *testing.T, s *Store, hiveID string, outcome domain.AgentState) {
	t.Helper()
	a, err := s.CreateForager(context.Background(), domain.Agent{HiveID: hiveID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionAgent(context.Background(), a.ID, a.State, outcome); err != nil {
		t.Fatal(err)
	}
}

func activeVersion(t *testing.T, s *Store, hiveID string, expected int) {
	t.Helper()
	m, _, err := s.ActiveMethodology(context.Background(), hiveID)
	if err != nil || m.Version != expected {
		t.Fatalf("active = %d, %v; want %d", m.Version, err, expected)
	}
}

func TestMethodologyAutomaticRollback(t *testing.T) {
	for _, queen := range []bool{false, true} {
		t.Run(fmt.Sprint(queen), func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			h := newTestHive(t, s, "acme/rollback")
			if _, err := s.Pool.Exec(ctx, `UPDATE hives SET queen_enabled=$2 WHERE id=$1`, h.ID, queen); err != nil {
				t.Fatal(err)
			}
			publishTestMethodology(t, s, h.ID, "first")
			// Outcomes before the new activation do not count toward its streak.
			methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			publishTestMethodology(t, s, h.ID, "second")
			methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			methodologyOutcome(t, s, h.ID, domain.AgentCancelled)
			methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			activeVersion(t, s, h.ID, 2)
			methodologyOutcome(t, s, h.ID, domain.AgentCompleted) // A success breaks the sequence.
			methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			activeVersion(t, s, h.ID, 2)
			methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			activeVersion(t, s, h.ID, 1)
			var target, trust, sender string
			var count int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE hive_id=$1 AND body LIKE 'Automatically rolled back%'`, h.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("notifications = %d, %v", count, err)
			}
			if err := s.Pool.QueryRow(ctx, `SELECT to_addr,trust,from_addr FROM messages WHERE hive_id=$1 AND body LIKE 'Automatically rolled back%'`, h.ID).Scan(&target, &trust, &sender); err != nil {
				t.Fatal(err)
			}
			want := "hive"
			if queen {
				want = "queen"
			}
			if target != want || trust != "system" || sender != "system" {
				t.Fatalf("notification = %s/%s/%s", target, trust, sender)
			}
			changes, err := s.MethodologyChanges(ctx, h.ID)
			if err != nil || len(changes) != 3 || changes[2].Actor != domain.AddressSystem || changes[2].Note == "" {
				t.Fatalf("changes = %+v, %v", changes, err)
			}
			// A later version must skip the automatically rejected version 2.
			publishTestMethodology(t, s, h.ID, "third")
			for i := 0; i < 3; i++ {
				methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			}
			activeVersion(t, s, h.ID, 1)
			// Manual activation remains allowed, but version 2 is not rolled back again.
			if _, err := s.ActivateMethodology(ctx, h.ID, 2, domain.AddressSystem, "manual"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				methodologyOutcome(t, s, h.ID, domain.AgentFailed)
			}
			activeVersion(t, s, h.ID, 2)
		})
	}
}

func TestMethodologyRollbackTransactionAndConcurrency(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/atomic-rollback")
	publishTestMethodology(t, s, h.ID, "one")
	publishTestMethodology(t, s, h.ID, "two")
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	queue := s.Jobs
	s.Jobs = nil
	if err := s.TransitionAgent(ctx, a.ID, a.State, domain.AgentFailed); !errors.Is(err, ErrMessageQueueUnavailable) {
		t.Fatalf("without queue: %v", err)
	}
	activeVersion(t, s, h.ID, 2)
	unchanged, err := s.Agent(ctx, a.ID)
	if err != nil || unchanged.State != a.State {
		t.Fatalf("agent was committed despite notification error: %+v %v", unchanged, err)
	}
	s.Jobs = queue
	// Concurrent retries of the same terminal event roll back exactly once.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.TransitionAgent(ctx, a.ID, a.State, domain.AgentFailed) }()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, domain.ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful transitions = %d", successes)
	}
	activeVersion(t, s, h.ID, 1)
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM methodology_events WHERE hive_id=$1 AND actor='system' AND action='activate'`, h.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback audit count = %d, %v", count, err)
	}
}

func TestMethodologyRollbackNoPreviousAndQueenExcluded(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/only-version")
	publishTestMethodology(t, s, h.ID, "one")
	for i := 0; i < 3; i++ {
		methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	}
	activeVersion(t, s, h.ID, 1)
	publishTestMethodology(t, s, h.ID, "two")
	q, err := s.Queen(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionAgent(ctx, q.ID, q.State, domain.AgentFailed); err != nil {
		t.Fatal(err)
	}
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	activeVersion(t, s, h.ID, 2)
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	activeVersion(t, s, h.ID, 1)
}
