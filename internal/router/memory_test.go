package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestMemoryBackendMessageValidation(t *testing.T) {
	ctx := context.Background()
	newBackend := func(t *testing.T) (*MemoryBackend, domain.Hive) {
		t.Helper()
		b := NewMemoryBackend()
		hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive", Config: domain.HiveConfig{MaxHops: 3}})
		return b, hive
	}

	t.Run("drone to drone is rejected", func(t *testing.T) {
		b, hive := newBackend(t)
		_, err := b.SendMessage(ctx, domain.Message{
			HiveID: hive.ID,
			From:   domain.DroneAddress("a"),
			To:     domain.DroneAddress("b"),
			Kind:   domain.MessageEvent,
			Body:   "hello",
		})
		if !errors.Is(err, domain.ErrPeerMessage) {
			t.Fatalf("err = %v, want ErrPeerMessage", err)
		}
	})

	t.Run("hop limit is enforced", func(t *testing.T) {
		b, hive := newBackend(t)
		_, err := b.SendMessage(ctx, domain.Message{
			HiveID: hive.ID,
			From:   domain.AddressSystem,
			To:     domain.AddressHive,
			Kind:   domain.MessageEvent,
			Hop:    4,
			Body:   "forwarded too far",
		})
		if !errors.Is(err, domain.ErrHopLimit) {
			t.Fatalf("err = %v, want ErrHopLimit", err)
		}
	})

	t.Run("body length limit is enforced", func(t *testing.T) {
		b, hive := newBackend(t)
		_, err := b.SendMessage(ctx, domain.Message{
			HiveID: hive.ID,
			From:   domain.AddressSystem,
			To:     domain.AddressHive,
			Kind:   domain.MessageEvent,
			Body:   strings.Repeat("x", domain.MaxMessageBody+1),
		})
		if !errors.Is(err, domain.ErrMessageTooLarge) {
			t.Fatalf("err = %v, want ErrMessageTooLarge", err)
		}
	})

	t.Run("same id and content is idempotent", func(t *testing.T) {
		b, hive := newBackend(t)
		message := domain.Message{
			ID:      "msg-fixed",
			HiveID:  hive.ID,
			From:    domain.AddressSystem,
			To:      domain.AddressHive,
			Kind:    domain.MessageEvent,
			ChainID: "chain-1",
			Body:    "hello",
		}
		first, err := b.SendMessage(ctx, message)
		if err != nil {
			t.Fatalf("first send: %v", err)
		}
		second, err := b.SendMessage(ctx, message)
		if err != nil {
			t.Fatalf("second send: %v", err)
		}
		if first.ID != second.ID {
			t.Fatalf("ids differ: %q vs %q", first.ID, second.ID)
		}
		if got := len(b.Messages()); got != 1 {
			t.Fatalf("message count = %d, want 1", got)
		}
	})

	t.Run("same id with different content conflicts", func(t *testing.T) {
		b, hive := newBackend(t)
		message := domain.Message{
			ID:     "msg-fixed",
			HiveID: hive.ID,
			From:   domain.AddressSystem,
			To:     domain.AddressHive,
			Kind:   domain.MessageEvent,
			Body:   "hello",
		}
		if _, err := b.SendMessage(ctx, message); err != nil {
			t.Fatalf("first send: %v", err)
		}
		message.Body = "different"
		if _, err := b.SendMessage(ctx, message); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
	})
}

func TestMemoryBackendCreateDroneLimits(t *testing.T) {
	ctx := context.Background()

	t.Run("hive concurrency limit", func(t *testing.T) {
		b := NewMemoryBackend()
		hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive", Config: domain.HiveConfig{MaxConcurrentDrones: 2, MaxDronesPerChain: 10}})
		for i := 0; i < 2; i++ {
			if _, err := b.CreateDrone(ctx, domain.Agent{HiveID: hive.ID, Role: domain.RoleDrone, ChainID: "chain-1", IssueNumber: int64(i + 1)}); err != nil {
				t.Fatalf("create %d: %v", i, err)
			}
		}
		if _, err := b.CreateDrone(ctx, domain.Agent{HiveID: hive.ID, Role: domain.RoleDrone, ChainID: "chain-1", IssueNumber: 3}); !errors.Is(err, domain.ErrHiveLimit) {
			t.Fatalf("err = %v, want ErrHiveLimit", err)
		}
	})

	t.Run("chain limit", func(t *testing.T) {
		b := NewMemoryBackend()
		hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive", Config: domain.HiveConfig{MaxConcurrentDrones: 10, MaxDronesPerChain: 2}})
		for i := 0; i < 2; i++ {
			if _, err := b.CreateDrone(ctx, domain.Agent{HiveID: hive.ID, Role: domain.RoleDrone, ChainID: "chain-1", IssueNumber: int64(i + 1)}); err != nil {
				t.Fatalf("create %d: %v", i, err)
			}
		}
		if _, err := b.CreateDrone(ctx, domain.Agent{HiveID: hive.ID, Role: domain.RoleDrone, ChainID: "chain-1", IssueNumber: 3}); !errors.Is(err, domain.ErrChainLimit) {
			t.Fatalf("err = %v, want ErrChainLimit", err)
		}
	})

	t.Run("terminal drones do not count against the limit", func(t *testing.T) {
		b := NewMemoryBackend()
		hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive", Config: domain.HiveConfig{MaxConcurrentDrones: 1}})
		b.AddDrone(domain.Agent{HiveID: hive.ID, Role: domain.RoleDrone, State: domain.AgentCompleted, Branch: "swarm/hive-1/done"})
		if _, err := b.CreateDrone(ctx, domain.Agent{HiveID: hive.ID, Role: domain.RoleDrone, ChainID: "chain-1"}); err != nil {
			t.Fatalf("CreateDrone: %v", err)
		}
	})

	t.Run("generates id and branch and defaults", func(t *testing.T) {
		b := NewMemoryBackend()
		hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive"})
		drone, err := b.CreateDrone(ctx, domain.Agent{HiveID: hive.ID})
		if err != nil {
			t.Fatalf("CreateDrone: %v", err)
		}
		if drone.ID == "" {
			t.Fatal("expected generated ID")
		}
		if drone.Branch != "swarm/hive-1/"+drone.ID {
			t.Fatalf("Branch = %q, want swarm/hive-1/%s", drone.Branch, drone.ID)
		}
		if drone.State != domain.AgentCreating {
			t.Fatalf("State = %q, want creating", drone.State)
		}
		if drone.CLI != "opencode" {
			t.Fatalf("CLI = %q, want opencode", drone.CLI)
		}
	})
}

func TestMemoryBackendDroneLookups(t *testing.T) {
	b := NewMemoryBackend()
	hive := b.AddHive(domain.Hive{ID: "hive-1", Repository: "acme/hive"})
	base := time.Now().UTC()
	older := b.AddDrone(domain.Agent{
		HiveID: hive.ID, Role: domain.RoleDrone, State: domain.AgentParked,
		IssueNumber: 5, PRNumber: 0, Branch: "swarm/hive-1/older", CreatedAt: base,
	})
	newer := b.AddDrone(domain.Agent{
		HiveID: hive.ID, Role: domain.RoleDrone, State: domain.AgentActive,
		IssueNumber: 5, PRNumber: 34, Branch: "swarm/hive-1/newer", CreatedAt: base.Add(time.Minute),
	})

	ctx := context.Background()
	if got, err := b.DroneByIssue(ctx, hive.ID, 5); err != nil || got.ID != newer.ID {
		t.Fatalf("DroneByIssue = %q, %v; want newest %q", got.ID, err, newer.ID)
	}
	if got, err := b.DroneByPR(ctx, hive.ID, 34); err != nil || got.ID != newer.ID {
		t.Fatalf("DroneByPR = %q, %v; want %q", got.ID, err, newer.ID)
	}
	if got, err := b.DroneByBranch(ctx, hive.ID, "swarm/hive-1/older"); err != nil || got.ID != older.ID {
		t.Fatalf("DroneByBranch = %q, %v; want %q", got.ID, err, older.ID)
	}
	if _, err := b.DroneByPR(ctx, hive.ID, 999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	// Completing the newest Drone falls back to the older one.
	newer.State = domain.AgentCompleted
	b.AddDrone(newer)
	if got, err := b.DroneByIssue(ctx, hive.ID, 5); err != nil || got.ID != older.ID {
		t.Fatalf("DroneByIssue after completion = %q, %v; want %q", got.ID, err, older.ID)
	}
}
