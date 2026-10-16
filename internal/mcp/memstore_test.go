package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func newHive(t *testing.T, config domain.HiveConfig) (*MemoryBackend, domain.Hive) {
	t.Helper()
	backend := NewMemoryBackend()
	h, err := backend.CreateHive(context.Background(), domain.Hive{
		Name: "alpha", Repository: "acme/alpha", Config: config,
	})
	if err != nil {
		t.Fatalf("CreateHive: %v", err)
	}
	return backend, h
}

func newDrone(t *testing.T, backend *MemoryBackend, hiveID, chainID string) domain.Agent {
	t.Helper()
	a, err := backend.CreateDrone(context.Background(), domain.Agent{
		HiveID: hiveID, Role: domain.RoleDrone, ChainID: chainID,
	})
	if err != nil {
		t.Fatalf("CreateDrone: %v", err)
	}
	return a
}

func TestMemoryBackendMessageValidation(t *testing.T) {
	backend, h := newHive(t, domain.HiveConfig{})
	ctx := context.Background()
	droneA := newDrone(t, backend, h.ID, "chain-1")
	droneB := newDrone(t, backend, h.ID, "chain-1")

	t.Run("peer message", func(t *testing.T) {
		_, err := backend.SendMessage(ctx, domain.Message{
			HiveID: h.ID, From: domain.DroneAddress(droneA.ID), To: domain.DroneAddress(droneB.ID),
			Kind: domain.MessageReport, Body: "hi peer",
		})
		if !errors.Is(err, domain.ErrPeerMessage) {
			t.Fatalf("err = %v, want ErrPeerMessage", err)
		}
	})

	t.Run("hop limit", func(t *testing.T) {
		_, err := backend.SendMessage(ctx, domain.Message{
			HiveID: h.ID, From: domain.AddressQueen, To: domain.DroneAddress(droneA.ID),
			Kind: domain.MessageTask, Body: "deep", Hop: 11,
		})
		if !errors.Is(err, domain.ErrHopLimit) {
			t.Fatalf("err = %v, want ErrHopLimit", err)
		}
	})

	t.Run("message too large", func(t *testing.T) {
		_, err := backend.SendMessage(ctx, domain.Message{
			HiveID: h.ID, From: domain.AddressQueen, To: domain.AddressHive,
			Kind: domain.MessageEvent, Body: strings.Repeat("x", domain.MaxMessageBody+1),
		})
		if !errors.Is(err, domain.ErrMessageTooLarge) {
			t.Fatalf("err = %v, want ErrMessageTooLarge", err)
		}
	})

	t.Run("unknown hive", func(t *testing.T) {
		_, err := backend.SendMessage(ctx, domain.Message{
			HiveID: "missing", From: domain.AddressQueen, To: domain.AddressHive, Body: "x",
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("queen to drone is allowed", func(t *testing.T) {
		if _, err := backend.SendMessage(ctx, domain.Message{
			HiveID: h.ID, From: domain.AddressQueen, To: domain.DroneAddress(droneA.ID),
			Kind: domain.MessageTask, Body: "go",
		}); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	})
}

func TestMemoryBackendIdempotency(t *testing.T) {
	backend, h := newHive(t, domain.HiveConfig{})
	ctx := context.Background()
	msg := domain.Message{
		ID: "delivery-1", HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive,
		Kind: domain.MessageEvent, Body: "first",
	}
	first, err := backend.SendMessage(ctx, msg)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	again, err := backend.SendMessage(ctx, msg)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("replay returned %q, want %q", again.ID, first.ID)
	}
	inbox, err := backend.Inbox(ctx, h.ID, domain.AddressHive, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox has %d messages, want 1", len(inbox))
	}

	conflicting := msg
	conflicting.Body = "changed"
	if _, err := backend.SendMessage(ctx, conflicting); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestMemoryBackendConcurrentDroneLimit(t *testing.T) {
	backend, h := newHive(t, domain.HiveConfig{MaxConcurrentDrones: 2})

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		limited   int
	)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := backend.CreateDrone(context.Background(), domain.Agent{
				HiveID: h.ID, Role: domain.RoleDrone,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, domain.ErrHiveLimit):
				limited++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if succeeded != 2 {
		t.Fatalf("created %d drones, want 2", succeeded)
	}
	if limited != 6 {
		t.Fatalf("limited %d drones, want 6", limited)
	}
}

func TestMemoryBackendChainDroneLimit(t *testing.T) {
	backend, h := newHive(t, domain.HiveConfig{MaxConcurrentDrones: 50, MaxDronesPerChain: 2})
	for i := 0; i < 5; i++ {
		_, err := backend.CreateDrone(context.Background(), domain.Agent{
			HiveID: h.ID, Role: domain.RoleDrone, ChainID: "chain-same",
		})
		if i < 2 && err != nil {
			t.Fatalf("drone %d: %v", i, err)
		}
		if i >= 2 && !errors.Is(err, domain.ErrChainLimit) {
			t.Fatalf("drone %d err = %v, want ErrChainLimit", i, err)
		}
	}
}

func TestMemoryBackendTransitionIsCompareAndSet(t *testing.T) {
	backend, h := newHive(t, domain.HiveConfig{})
	drone := newDrone(t, backend, h.ID, "")
	ctx := context.Background()

	if err := backend.TransitionAgent(ctx, drone.ID, domain.AgentActive, domain.AgentParked); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if err := backend.TransitionAgent(ctx, drone.ID, domain.AgentCreating, domain.AgentActive); err != nil {
		t.Fatalf("TransitionAgent: %v", err)
	}
	got, err := backend.Agent(ctx, drone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.AgentActive {
		t.Fatalf("state = %s, want active", got.State)
	}
}

func TestMemoryBackendClaimLease(t *testing.T) {
	backend, h := newHive(t, domain.HiveConfig{})
	ctx := context.Background()
	base := time.Now()
	backend.SetClock(func() time.Time { return base })

	msg, err := backend.SendMessage(ctx, domain.Message{
		HiveID: h.ID, From: domain.AddressSystem, To: domain.AddressHive, Kind: domain.MessageEvent, Body: "e",
	})
	if err != nil {
		t.Fatal(err)
	}

	ok, err := backend.ClaimMessage(ctx, msg.ID, "local:a", 50*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	ok, err = backend.ClaimMessage(ctx, msg.ID, "local:b", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("a live lease was stolen")
	}

	backend.SetClock(func() time.Time { return base.Add(time.Minute) })
	ok, err = backend.ClaimMessage(ctx, msg.ID, "local:b", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expired lease could not be taken over")
	}
}

func TestMemoryBackendWaitInbox(t *testing.T) {
	backend, h := newHive(t, domain.HiveConfig{})
	ctx := context.Background()
	to := domain.AddressHive

	// An existing message wakes the waiter immediately.
	if _, err := backend.SendMessage(ctx, domain.Message{
		HiveID: h.ID, From: domain.AddressSystem, To: to, Kind: domain.MessageEvent, Body: "already here",
	}); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := backend.WaitInbox(waitCtx, h.ID, to, ""); err != nil {
		t.Fatalf("WaitInbox with an existing message: %v", err)
	}

	// Nothing newer: the wait ends with the context.
	msgs, err := backend.Inbox(ctx, h.ID, to, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	shortCtx, shortCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer shortCancel()
	if err := backend.WaitInbox(shortCtx, h.ID, to, msgs[len(msgs)-1].ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestMemoryBackendTokenHashing(t *testing.T) {
	backend := NewMemoryBackend()
	ctx := context.Background()
	plaintext, token, err := backend.CreateToken(ctx, "user", domain.TokenDefault)
	if err != nil {
		t.Fatal(err)
	}
	got, err := backend.TokenByPlaintext(ctx, plaintext)
	if err != nil {
		t.Fatalf("TokenByPlaintext: %v", err)
	}
	if got.ID != token.ID || got.Scope != domain.TokenDefault {
		t.Fatalf("got %+v, want %+v", got, token)
	}
	if _, err := backend.TokenByPlaintext(ctx, "swj_wrong"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.tokens) != 1 {
		t.Fatalf("stored %d tokens, want 1", len(backend.tokens))
	}
	for key := range backend.tokens {
		if strings.Contains(key, plaintext) || key == plaintext {
			t.Fatalf("token plaintext is stored directly: %q", key)
		}
		if len(key) != 64 {
			t.Fatalf("token key %q is not a sha256 hex digest", key)
		}
	}
}

func TestHiveConfigDefaults(t *testing.T) {
	backend, h := newHive(t, domain.HiveConfig{})
	got, err := backend.Hive(context.Background(), h.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.HiveConfig{
		CLI:                   "opencode",
		MaxConcurrentDrones:   4,
		MaxHops:               10,
		MaxDronesPerChain:     10,
		IdleParkMinutes:       30,
		HibernateHours:        24,
		TurnTimeoutMinutes:    120,
		DroneMaxLifetimeHours: 168,
	}
	if got.Config.CLI != want.CLI ||
		got.Config.MaxConcurrentDrones != want.MaxConcurrentDrones ||
		got.Config.MaxHops != want.MaxHops ||
		got.Config.MaxDronesPerChain != want.MaxDronesPerChain ||
		got.Config.IdleParkMinutes != want.IdleParkMinutes ||
		got.Config.HibernateHours != want.HibernateHours ||
		got.Config.TurnTimeoutMinutes != want.TurnTimeoutMinutes ||
		got.Config.DroneMaxLifetimeHours != want.DroneMaxLifetimeHours {
		t.Fatalf("config = %+v, want %+v", got.Config, want)
	}
}
