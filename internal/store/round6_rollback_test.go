package store

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestOldAgentFailuresBelongToStartupVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/startup-version")
	publishTestMethodology(t, s, h.ID, "one")
	publishTestMethodology(t, s, h.ID, "two")
	var old []domain.Agent
	for i := 0; i < 3; i++ {
		a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
		if err != nil {
			t.Fatal(err)
		}
		old = append(old, a)
	}
	publishTestMethodology(t, s, h.ID, "three")
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	for _, a := range old {
		if err := s.TransitionAgent(ctx, a.ID, a.State, domain.AgentFailed); err != nil {
			t.Fatal(err)
		}
	}
	activeVersion(t, s, h.ID, 3)
	var oldFailures, newFailures int
	if err := s.Pool.QueryRow(ctx, `SELECT consecutive_failures FROM methodologies WHERE hive_id=$1 AND version=2`, h.ID).Scan(&oldFailures); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT consecutive_failures FROM methodologies WHERE hive_id=$1 AND version=3`, h.ID).Scan(&newFailures); err != nil {
		t.Fatal(err)
	}
	if oldFailures != 3 || newFailures != 1 {
		t.Fatalf("failure ownership: old=%d new=%d", oldFailures, newFailures)
	}
	// The active version still rolls back only after its own third failure.
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	activeVersion(t, s, h.ID, 3)
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	activeVersion(t, s, h.ID, 2)
}

func TestHistoricalAgentWithoutVersionDoesNotCount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/legacy-agent")
	a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	publishTestMethodology(t, s, h.ID, "one")
	publishTestMethodology(t, s, h.ID, "two")
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	methodologyOutcome(t, s, h.ID, domain.AgentFailed)
	if err := s.TransitionAgent(ctx, a.ID, a.State, domain.AgentFailed); err != nil {
		t.Fatal(err)
	}
	activeVersion(t, s, h.ID, 2)
	var failures int
	if err := s.Pool.QueryRow(ctx, `SELECT consecutive_failures FROM methodologies WHERE hive_id=$1 AND active`, h.ID).Scan(&failures); err != nil || failures != 2 {
		t.Fatalf("legacy failure counted: %d %v", failures, err)
	}
}

func TestMethodologyVersionMigrationFrom13And14(t *testing.T) {
	for _, with14 := range []bool{false, true} {
		s := testStore(t)
		ctx := context.Background()
		if _, err := s.Pool.Exec(ctx, `DELETE FROM schema_version WHERE version=15; ALTER TABLE agents DROP COLUMN methodology_version_id`); err != nil {
			t.Fatal(err)
		}
		if with14 {
			if _, err := s.Pool.Exec(ctx, `INSERT INTO schema_version(version) VALUES(14) ON CONFLICT DO NOTHING`); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 2; i++ {
			if err := s.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
		}
		var exists bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='agents' AND column_name='methodology_version_id')`).Scan(&exists); err != nil || !exists {
			t.Fatalf("startup version missing: %t %v", exists, err)
		}
	}
}
