package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

// memoryJobs is the in-memory delivery queue used by tests instead of the
// real River client. SendMessage fails loudly when no queue is installed; this
// keeps database tests exercising the same contract as production.
type memoryJobs struct {
	mu       sync.Mutex
	messages []string
}

func (m *memoryJobs) InsertWebhook(context.Context, pgx.Tx, jobs.Webhook) error { return nil }
func (m *memoryJobs) InsertMessage(_ context.Context, _ pgx.Tx, job jobs.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, job.ID)
	return nil
}

func (m *memoryJobs) delivered() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.messages...)
}

// testStore creates an isolated schema and applies the whole migration chain.
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "store_test_" + hex.EncodeToString(random)
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg := admin.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Pool: pool, Jobs: &memoryJobs{}}
	t.Cleanup(func() {
		s.Close()
		_, err := admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
