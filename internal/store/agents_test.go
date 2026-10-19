package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestEnabledAgentsExplicitAndPersistent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ids, err := s.EnabledAgents(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatal("agents implicitly enabled", ids, err)
	}
	if err = s.SetEnabledAgents(ctx, []string{"codex", "dst"}); err != nil {
		t.Fatal(err)
	}
	other := &Store{Pool: s.Pool}
	ids, err = other.EnabledAgents(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{"codex", "dst"}) {
		t.Fatal(ids, err)
	}
	for _, invalid := range [][]string{{"unknown"}, {"codex", "codex"}} {
		if err = s.SetEnabledAgents(ctx, invalid); err == nil {
			t.Fatal("invalid enabled list accepted")
		}
	}
	if err = s.SetEnabledAgents(ctx, nil); err != nil {
		t.Fatal(err)
	}
	ids, err = s.EnabledAgents(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatal("disable all failed", ids, err)
	}
}

func TestAgentSessionAndPRUpdates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/session")
	a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID, SessionRef: "session-on-create"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionRef != "session-on-create" {
		t.Fatalf("sessionRef lost on create: %+v", a)
	}
	if err = s.SetAgentSession(ctx, a.ID, "session-2"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetAgentPR(ctx, a.ID, 77); err != nil {
		t.Fatal(err)
	}
	got, err := s.Agent(ctx, a.ID)
	if err != nil || got.SessionRef != "session-2" || got.PRNumber != 77 {
		t.Fatalf("%+v %v", got, err)
	}
	// Lookup helpers carry the session too.
	byBranch, err := s.ForagerByBranch(ctx, h.ID, a.Branch)
	if err != nil || byBranch.SessionRef != "session-2" {
		t.Fatalf("%+v %v", byBranch, err)
	}
	if err = s.SetAgentSession(ctx, "missing", "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
	if err = s.SetAgentPR(ctx, "missing", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing PR: %v", err)
	}
}
