package store

import (
	"context"
	"errors"
	"sync"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestQueenBootstrapPerEnableTransition(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Repository: "acme/bootstrap", QueenEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	assertCount := func(want int) {
		t.Helper()
		messages, err := s.Inbox(ctx, h.ID, domain.AddressQueen, "", 100)
		if err != nil || len(messages) != want {
			t.Fatalf("bootstrap messages = %+v, %v; want %d", messages, err, want)
		}
		for _, m := range messages {
			if m.From != domain.AddressSystem || m.Kind != domain.MessageTask || m.Trust != domain.TrustSystem || m.Body != "新 Hive，请开始首次引导" {
				t.Fatalf("invalid bootstrap: %+v", m)
			}
		}
		if got := len(s.Jobs.(*memoryJobs).delivered()); got != want {
			t.Fatalf("delivery jobs = %d, want %d", got, want)
		}
	}
	assertCount(1)
	h, err = s.UpdateHive(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(1)
	h.QueenEnabled = false
	h, err = s.UpdateHive(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	assertCount(1)
	h.QueenEnabled = true
	// Concurrent retries of the same version admit one transition and one job.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateHive(ctx, h)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var successes int
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, domain.ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful transitions = %d", successes)
	}
	assertCount(2)
}

func TestQueenBootstrapTransactionRollback(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	s.Jobs = jobsWithoutMessages{}
	if _, err := s.CreateHive(ctx, domain.Hive{ID: "rollback", Repository: "acme/rollback", QueenEnabled: true}); !errors.Is(err, ErrMessageQueueUnavailable) {
		t.Fatalf("create error = %v", err)
	}
	if _, err := s.Hive(ctx, "rollback"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed create persisted Hive: %v", err)
	}
	if _, err := s.Queen(ctx, "rollback"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed create persisted Queen: %v", err)
	}
	h := newTestHive(t, s, "acme/enable-rollback")
	h.QueenEnabled = true
	if _, err := s.UpdateHive(ctx, h); !errors.Is(err, ErrMessageQueueUnavailable) {
		t.Fatalf("update error = %v", err)
	}
	got, err := s.Hive(ctx, h.ID)
	if err != nil || got.QueenEnabled || got.Version != h.Version {
		t.Fatalf("failed update persisted: %+v %v", got, err)
	}
	messages, err := s.Inbox(ctx, h.ID, domain.AddressQueen, "", 100)
	if err != nil || len(messages) != 0 {
		t.Fatalf("failed update persisted bootstrap: %+v %v", messages, err)
	}
}

func TestQueenReenableKeepsExistingMethodology(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/reenable")
	publishTestMethodology(t, s, h.ID, "existing")
	h.QueenEnabled = true
	if _, err := s.UpdateHive(ctx, h); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Inbox(ctx, h.ID, domain.AddressQueen, "", 100)
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Body, "主 Agent 已重新开启") || strings.Contains(messages[0].Body, "首次引导") {
		t.Fatalf("reenable messages = %+v %v", messages, err)
	}
	activeVersion(t, s, h.ID, 1)
}
