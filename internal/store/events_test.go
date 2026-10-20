package store

import (
	"context"
	"errors"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"testing"
	"time"
)

func TestMigration14RepeatFrom13(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `DELETE FROM schema_version WHERE version=14`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"messages", "agents", "attestations", "methodology_events", "spawn_queue", "hives"} {
		if _, err := s.Pool.Exec(ctx, `DROP TRIGGER swarmjet_events ON `+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `DROP FUNCTION swarmjet_notify_event()`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := applySchemaUpgrade14(ctx, tx, 13); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, schemaUpgrade14); err != nil {
		t.Fatal("SQL not repeatable:", err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname='swarmjet_events' AND tgrelid IN ('messages'::regclass,'agents'::regclass,'attestations'::regclass,'methodology_events'::regclass,'spawn_queue'::regclass,'hives'::regclass)`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("triggers=%d err=%v", count, err)
	}
}

func TestPublishPREvent(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	refs, unsubscribe, err := s.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if err := s.PublishPREvent(ctx, "hive-event", 42); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-refs:
		if got != (EventRef{Type: "pr", HiveID: "hive-event", ID: "42"}) {
			t.Fatalf("ref=%+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestEventSubscriptionsShareListenerAndClose(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, removeFirst, err := s.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer removeFirst()
	listener := s.events
	second, removeSecond, err := s.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer removeSecond()
	if s.events != listener {
		t.Fatal("multiple shared listeners")
	}
	listener.mu.Lock()
	count := len(listener.waiters)
	listener.mu.Unlock()
	if count != 2 {
		t.Fatalf("subscribers=%d", count)
	}
	listener.stop()
	for _, events := range []<-chan EventRef{first, second} {
		select {
		case _, ok := <-events:
			if ok {
				t.Fatal("subscription not closed")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestEventDataChecksHiveOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	hive := newTestHive(t, s, "acme/event-ownership")
	queen, err := s.Queen(ctx, hive.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []EventRef{{Type: "agent", HiveID: "other", ID: queen.ID}, {Type: "hive", HiveID: hive.ID, ID: "other"}} {
		if _, err := s.EventData(ctx, ref); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("wrong hive accepted: %+v err=%v", ref, err)
		}
	}
}
