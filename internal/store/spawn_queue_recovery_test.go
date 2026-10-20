package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

func TestSpawnQueueRejectsInvalidHopBeforeEnqueue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Queue", Repository: "acme/invalid-hop", Config: domain.HiveConfig{MaxHops: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.EnqueueSpawn(ctx, domain.Agent{ID: "invalid", HiveID: h.ID}, domain.Message{Body: "task", Hop: 2}, domain.Limits{})
	if !errors.Is(err, domain.ErrHopLimit) {
		t.Fatalf("enqueue invalid hop = %v", err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM spawn_queue`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid requests persisted = %d, %v", count, err)
	}
}

func TestSpawnQueueInvalidLegacyShellDoesNotBlockFIFO(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Queue", Repository: "acme/legacy-shell", Config: domain.HiveConfig{MaxHops: 2, MaxConcurrentForagers: 1}})
	if err != nil {
		t.Fatal(err)
	}
	invalid := domain.Agent{ID: "invalid", HiveID: h.ID, Task: "old task"}
	if _, _, err = s.EnqueueSpawn(ctx, invalid, domain.Message{From: domain.AddressSystem, Kind: domain.MessageTask, Body: invalid.Task, Hop: 2}, domain.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateForager(context.WithValue(ctx, spawnQueueDrainKey{}, true), invalid); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueSpawn(ctx, domain.Agent{ID: "valid", HiveID: h.ID, Task: "next task"}, domain.Message{From: domain.AddressSystem, Kind: domain.MessageTask, Body: "next task"}, domain.Limits{}); err != nil {
		t.Fatal(err)
	}
	h.Config.MaxHops = 1
	if _, err = s.UpdateHive(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE spawn_queue SET created_at=now()-interval '25 hours' WHERE agent_id='invalid'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.DrainSpawnQueue(ctx, h.ID); err != nil {
			t.Fatal(err)
		}
	}
	if a, err := s.Agent(ctx, invalid.ID); err != nil || a.State != domain.AgentCancelled {
		t.Fatalf("invalid shell = %+v, %v", a, err)
	}
	if _, err := s.Agent(ctx, "valid"); err != nil {
		t.Fatalf("valid request did not start: %v", err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE id='spawn-expired-invalid'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("abandon notifications = %d, %v", count, err)
	}
}

type failedSpawnDelivery struct{ err error }

func (f failedSpawnDelivery) InsertWebhook(context.Context, pgx.Tx, jobs.Webhook) error {
	return nil
}
func (f failedSpawnDelivery) InsertMessage(context.Context, pgx.Tx, jobs.Message) error {
	return f.err
}

func TestSpawnQueueDeliveryFailureRollsBackAgentCreation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Queue", Repository: "acme/delivery-failure"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueSpawn(ctx, domain.Agent{ID: "queued", HiveID: h.ID, Task: "task"}, domain.Message{From: domain.AddressSystem, Kind: domain.MessageTask, Body: "task"}, domain.Limits{}); err != nil {
		t.Fatal(err)
	}
	originalJobs := s.Jobs
	failure := errors.New("injected delivery failure")
	s.Jobs = failedSpawnDelivery{err: failure}
	if err = s.DrainSpawnQueue(ctx, h.ID); !errors.Is(err, failure) {
		t.Fatalf("drain error = %v", err)
	}
	if _, err = s.Agent(ctx, "queued"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Agent survived failed task delivery: %v", err)
	}
	var state string
	if err := s.Pool.QueryRow(ctx, `SELECT state FROM spawn_queue WHERE agent_id='queued'`).Scan(&state); err != nil || state != "waiting" {
		t.Fatalf("queue state = %q, %v", state, err)
	}
	s.Jobs = originalJobs
	if err = s.DrainSpawnQueue(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Agent(ctx, "queued"); err != nil {
		t.Fatalf("retry did not create Agent: %v", err)
	}
}

func TestSpawnQueueDrainsWithOnePoolConnection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Queue", Repository: "acme/single-connection"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueSpawn(ctx, domain.Agent{ID: "queued", HiveID: h.ID, Task: "task"}, domain.Message{From: domain.AddressSystem, Kind: domain.MessageTask, Body: "task"}, domain.Limits{}); err != nil {
		t.Fatal(err)
	}
	cfg := s.Pool.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	single := &Store{Pool: pool, Jobs: s.Jobs}
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := single.DrainSpawnQueue(deadline, h.ID); err != nil {
		t.Fatalf("single-connection drain failed: %v", err)
	}
	if _, err := single.Agent(ctx, "queued"); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnQueueConcurrentDrainsDoNotDuplicate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Queue", Repository: "acme/concurrent-drain", Config: domain.HiveConfig{MaxConcurrentForagers: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if _, _, err = s.EnqueueSpawn(ctx, domain.Agent{ID: id, HiveID: h.ID, Task: id}, domain.Message{From: domain.AddressSystem, Kind: domain.MessageTask, Body: id}, domain.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	drain := func() {
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.DrainSpawnQueue(ctx, h.ID); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	drain()
	first, err := s.Agent(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Agent(ctx, "second"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second request passed capacity: %v", err)
	}
	if err = s.TransitionAgent(ctx, first.ID, first.State, domain.AgentCompleted); err != nil {
		t.Fatal(err)
	}
	drain()
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE id IN ('queued-task-first','queued-task-second')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("task messages = %d, %v", count, err)
	}
}

func TestSpawnQueueHiveLockAllowsConcurrentMessageForeignKey(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Queue", Repository: "acme/queue-message-lock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.EnqueueSpawn(ctx, domain.Agent{ID: "queued", HiveID: h.ID, Task: "task"}, domain.Message{From: domain.AddressSystem, Kind: domain.MessageTask, Body: "task"}, domain.Limits{}); err != nil {
		t.Fatal(err)
	}
	sender, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Rollback(context.Background())
	if _, err = sender.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "inbox:"+h.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.DrainSpawnQueue(ctx, h.ID) }()
	waiting := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		err := s.Pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM pg_locks queue_lock JOIN pg_locks inbox_lock USING(pid)
			WHERE queue_lock.locktype='advisory' AND queue_lock.classid=1205
			AND queue_lock.objid=hashtext($1)::oid AND queue_lock.granted
			AND inbox_lock.locktype='advisory' AND NOT inbox_lock.granted)`, h.ID).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("queue never reached task delivery while holding the Hive row lock")
	}
	// The sender owns the Inbox lock; task delivery owns the Hive row lock.
	// Its FK check must still acquire KEY SHARE, or these transactions deadlock.
	if _, err = s.sendMessageTx(ctx, sender, domain.Message{HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive, Kind: domain.MessageEvent, Body: "concurrent event"}); err != nil {
		t.Fatalf("concurrent message FK was blocked by the Hive lock: %v", err)
	}
	if err = sender.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatalf("queue failed after concurrent message: %v", err)
	}
}
