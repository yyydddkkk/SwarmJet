package initflow

import (
	"context"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
)

// A connect-step failure must resume at connect, not skip to analysis without
// pinned repository revisions.
func TestRetryAfterConnectFailureReconnects(t *testing.T) {
	f := newFixture(t)
	f.m.Clients = func(domain.Project) (*gitlab.Client, error) { return nil, errors.New("gitlab unreachable") }
	init := f.drive(t, 12)
	if init.Status != domain.InitBlocked || init.RepositoriesConnected {
		t.Fatalf("connect failure not surfaced: %+v", init)
	}
	if err := f.m.Retry(context.Background(), f.project.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := f.m.Get(context.Background(), f.project.ID)
	if err != nil || retried.Stage != "connect" {
		t.Fatalf("retry skipped connect after a connection failure: %+v %v", retried, err)
	}
	f.m.Clients = f.clients
	f.agents.onStart = readyAgent(true)
	done := f.drive(t, 12)
	if done.Status != domain.InitReady || !done.RepositoriesConnected {
		t.Fatalf("recovered retry did not complete: %+v", done)
	}
}
