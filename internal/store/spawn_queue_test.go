package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestSpawnQueueFIFOAndDeduplication(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Queue", Repository: "acme/queue", Config: domain.HiveConfig{MaxConcurrentForagers: 1}})
	if err != nil {
		t.Fatal(err)
	}
	busy, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: "busy"})
	if err != nil {
		t.Fatal(err)
	}
	enqueue := func(id string) int {
		t.Helper()
		a := domain.Agent{ID: id, HiveID: h.ID, Role: domain.RoleForager, State: domain.AgentCreating, Task: id, ChainID: id}
		m := domain.Message{ID: "task-" + id, From: domain.AddressSystem, Kind: domain.MessageTask, Body: id, Trust: domain.TrustSystem}
		gotID, position, err := s.EnqueueSpawn(ctx, a, m, domain.Limits{})
		if err != nil || gotID != id {
			t.Fatalf("enqueue %s: id=%q position=%d err=%v", id, gotID, position, err)
		}
		return position
	}
	if n := enqueue("first"); n != 1 {
		t.Fatalf("first position = %d", n)
	}
	if n := enqueue("second"); n != 2 {
		t.Fatalf("second position = %d", n)
	}
	if n := enqueue("first"); n != 1 {
		t.Fatalf("duplicate position = %d", n)
	}
	if err := s.DrainSpawnQueue(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Agent(ctx, "first"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("created while full: %v", err)
	}
	if err := s.TransitionAgent(ctx, busy.ID, busy.State, domain.AgentCompleted); err != nil {
		t.Fatal(err)
	}
	// A fresh request may not take the slot ahead of the waiting requests.
	if _, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, ChainID: "late"}); !errors.Is(err, domain.ErrHiveLimit) {
		t.Fatalf("late request overtook queue: %v", err)
	}
	if err := s.DrainSpawnQueue(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	first, err := s.Agent(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnqueueSpawn(ctx, domain.Agent{ID: "first", HiveID: h.ID, Task: "first"}, domain.Message{ID: "task-first", Body: "first"}, domain.Limits{}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("already created request requeued: %v", err)
	}
	if _, err := s.Agent(ctx, "second"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second started ahead of first: %v", err)
	}
	if err := s.TransitionAgent(ctx, first.ID, first.State, domain.AgentCompleted); err != nil {
		t.Fatal(err)
	}
	if err := s.DrainSpawnQueue(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Agent(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		var n int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE id=$1`, "task-"+id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("task message %s count=%d err=%v", id, n, err)
		}
	}
}

func TestSpawnQueueExpiresAndNotifies(t *testing.T) {
	for _, queen := range []bool{false, true} {
		t.Run(map[bool]string{false: "hive", true: "queen"}[queen], func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			h, err := s.CreateHive(ctx, domain.Hive{Name: "Queue", Repository: "acme/expire", QueenEnabled: queen, Config: domain.HiveConfig{MaxConcurrentForagers: 1}})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = s.EnqueueSpawn(ctx, domain.Agent{ID: "expired", HiveID: h.ID, Task: "task"}, domain.Message{From: domain.AddressSystem, Kind: domain.MessageTask, Body: "task"}, domain.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE spawn_queue SET created_at=$1 WHERE agent_id='expired'`, time.Now().Add(-25*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := s.DrainSpawnQueue(ctx, h.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.DrainSpawnQueue(ctx, h.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Agent(ctx, "expired"); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("expired request started: %v", err)
			}
			if _, err := s.CreateForager(ctx, domain.Agent{ID: "expired", HiveID: h.ID}); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("expired request retry = %v", err)
			}
			to := domain.AddressHive
			if queen {
				to = domain.AddressQueen
			}
			var count int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE id='spawn-expired-expired' AND to_addr=$1`, to).Scan(&count); err != nil || count != 1 {
				t.Fatalf("expiry notification to %s count=%d err=%v", to, count, err)
			}
		})
	}
}

func TestSpawnQueueRecoversAgentCreatedBeforeTaskMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Name: "Recover", Repository: "acme/recover", Config: domain.HiveConfig{MaxConcurrentForagers: 1}})
	if err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{ID: "recovered", HiveID: h.ID, Task: "recover task", ChainID: "recover"}
	message := domain.Message{ID: "recover-message", From: domain.AddressSystem, Kind: domain.MessageTask, Body: agent.Task}
	if _, _, err = s.EnqueueSpawn(ctx, agent, message, domain.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateForager(context.WithValue(ctx, spawnQueueDrainKey{}, true), agent); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE spawn_queue SET created_at=$1 WHERE agent_id=$2`, time.Now().Add(-25*time.Hour), agent.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DrainSpawnQueue(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.Pool.QueryRow(ctx, `SELECT state FROM spawn_queue WHERE agent_id=$1`, agent.ID).Scan(&state); err != nil || state != "started" {
		t.Fatalf("queue state=%q err=%v", state, err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE id=$1`, message.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("task message count=%d err=%v", count, err)
	}
}
