package store

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestMigrateRepairsVersionTwoNodesWithoutProtocolVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `ALTER TABLE nodes DROP COLUMN protocol_version`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM schema_version WHERE version > 2`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "restored", Address: "http://node:8092", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	statuses, err := s.NodeStatuses(ctx)
	if err != nil || len(statuses) != 1 || statuses[0].Node.ID != "restored" {
		t.Fatalf("statuses=%+v err=%v", statuses, err)
	}
}

func TestMigrateRemovesPlatformUpgradeTaskGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var gates int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname='platform_upgrade_task_gate'`).Scan(&gates); err != nil {
		t.Fatal(err)
	}
	if gates != 0 {
		t.Fatalf("platform upgrade task gate still installed: %d", gates)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
}
