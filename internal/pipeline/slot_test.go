package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

func TestNodeResourceSlotUsesNodeCapacity(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "pipeline_slot_" + hex.EncodeToString(random[:])
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg := admin.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &store.Store{Pool: pool}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "node", Address: "http://node", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO node_configs(id,version,body) VALUES('node',1,'{"capacity":1}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	a := &Activities{Store: s, Concurrency: 4}
	_, release, err := a.resourceSlot(ctx, "first", "node")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, _, err := a.resourceSlot(waitCtx, "second", "node"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second execution crossed capacity one: %v", err)
	}
	release()
	_, releaseAgain, err := a.resourceSlot(ctx, "second", "node")
	if err != nil {
		t.Fatal(err)
	}
	releaseAgain()
}
