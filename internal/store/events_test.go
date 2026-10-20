package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
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
	subscription, err := s.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if err := s.PublishPREvent(ctx, "hive-event", 42); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-subscription.Events:
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
	first, err := s.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	listener := s.events
	second, err := s.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
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
	for _, events := range []<-chan EventRef{first.Events, second.Events} {
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

func TestEventDataNormalizesNonUTCTimestamps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	hive := newTestHive(t, s, "acme/event-timezone")
	queen, err := s.Queen(ctx, hive.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := s.Pool.Config()
	// Simulate a process decoding PostgreSQL timestamptz in its non-UTC zone
	// without changing time.Local, which would race with unrelated tests.
	config.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.FixedZone("test-zone", 8*60*60)}})
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	zoned := &Store{Pool: pool}
	defer zoned.Close()
	var raw time.Time
	err = zoned.Pool.QueryRow(ctx, `SELECT created_at FROM agents WHERE id=$1`, queen.ID).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := raw.Zone(); offset != 8*60*60 {
		t.Fatal("fixture did not decode non-UTC time")
	}
	for _, ref := range []EventRef{{Type: "hive", HiveID: hive.ID, ID: hive.ID}, {Type: "agent", HiveID: hive.ID, ID: queen.ID}} {
		value, err := zoned.EventData(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		var created, updated time.Time
		switch entity := value.(type) {
		case domain.Hive:
			created, updated = entity.CreatedAt, entity.UpdatedAt
		case domain.Agent:
			created, updated = entity.CreatedAt, entity.UpdatedAt
		}
		if created.Location() != time.UTC || updated.Location() != time.UTC || created.IsZero() || updated.IsZero() {
			t.Fatalf("non-UTC event: %+v", value)
		}
	}
}

func TestEventSubscriptionOverflowCancelsBeforeDrain(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subscription, err := s.SubscribeEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	listener := s.events
	payload := `{"type":"message","hiveId":"hive","id":"message"}`
	for i := 0; i < 257; i++ {
		listener.dispatch(payload)
	}
	select {
	case <-subscription.Done():
	default:
		t.Fatal("full buffer did not cancel subscription")
	}
	if len(subscription.Events) != 256 {
		t.Fatalf("buffer unexpectedly drained: %d", len(subscription.Events))
	}
	if err := subscription.Context().Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("hydration context remains live: %v", err)
	}
}
