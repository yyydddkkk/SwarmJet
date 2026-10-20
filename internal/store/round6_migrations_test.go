package store

import (
	"context"
	"fmt"
	"testing"
)

func TestRound6MigrationsFromActual13And14(t *testing.T) {
	for _, baseline := range []int{13, 14} {
		t.Run(fmt.Sprint(baseline), func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			if _, err := s.Pool.Exec(ctx, `ALTER TABLE agents DROP COLUMN methodology_version_id; DELETE FROM schema_version WHERE version=15`); err != nil {
				t.Fatal(err)
			}
			if baseline == 13 {
				for _, table := range []string{"messages", "agents", "attestations", "methodology_events", "spawn_queue", "hives"} {
					if _, err := s.Pool.Exec(ctx, `DROP TRIGGER swarmjet_events ON `+table); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.Pool.Exec(ctx, `DROP FUNCTION swarmjet_notify_event(); DELETE FROM schema_version WHERE version=14`); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := s.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var versions, triggers int
			var startupColumn bool
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_version WHERE version IN (14,15)`).Scan(&versions); err != nil {
				t.Fatal(err)
			}
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname='swarmjet_events' AND tgrelid IN ('messages'::regclass,'agents'::regclass,'attestations'::regclass,'methodology_events'::regclass,'spawn_queue'::regclass,'hives'::regclass)`).Scan(&triggers); err != nil {
				t.Fatal(err)
			}
			if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='agents' AND column_name='methodology_version_id')`).Scan(&startupColumn); err != nil {
				t.Fatal(err)
			}
			if versions != 2 || triggers != 6 || !startupColumn {
				t.Fatalf("baseline %d: versions=%d triggers=%d startupColumn=%t", baseline, versions, triggers, startupColumn)
			}
		})
	}
}
