package store

import (
	"context"
	"testing"

	"swarmjet/internal/domain"
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
