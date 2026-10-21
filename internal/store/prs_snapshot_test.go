package store

import (
	"context"
	"testing"
	"time"
)

func TestPRSnapshotMigrationAndWebhookOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/snapshots")
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var version int
	if err := s.Pool.QueryRow(ctx, `SELECT max(version) FROM schema_version`).Scan(&version); err != nil || version != 16 {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	newer := time.Now().UTC().Truncate(time.Second)
	if err := s.SaveWebhookPR(ctx, h.ID, 7, "merged", "closed", true, newer, "https://github.test/pr/7", "new-head", newer); err != nil {
		t.Fatal(err)
	}
	changed, err := s.SavePRSnapshot(ctx, PRSnapshot{HiveID: h.ID, Number: 7, Title: "old open", State: "open", HeadSHA: "old-head", UpdatedAt: newer.Add(-time.Minute)})
	if err != nil || changed {
		t.Fatalf("old refresh changed snapshot=%t err=%v", changed, err)
	}
	items, err := s.PRSnapshots(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if items[7].State != "closed" || items[7].HeadSHA != "new-head" || !items[7].Merged {
		t.Fatalf("stale refresh overwrote webhook: %+v", items[7])
	}
	if err := s.SaveWebhookPR(ctx, h.ID, 7, "reopened", "open", false, time.Time{}, "https://github.test/pr/7", "reopened-head", newer.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	items, err = s.PRSnapshots(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if items[7].State != "open" || items[7].Merged || !items[7].ClosedAt.IsZero() {
		t.Fatalf("reopen=%+v", items[7])
	}
}
