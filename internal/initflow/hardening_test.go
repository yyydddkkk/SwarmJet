package initflow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"swarmjet/internal/domain"
)

// Polling must not restart an agent that is waiting for an operator answer or
// for an explicit retry.
func TestSweepDoesNotAdvanceWaitingStates(t *testing.T) {
	f := newFixture(t)
	f.repo.inits[InitID("p1")] = domain.Initialization{ID: InitID("p1"), ProjectID: "p1", Status: domain.InitNeedsInput, Stage: "analyze", Version: 1, RepositorySHAs: map[string]string{}}
	f.repo.inits[InitID("p2")] = domain.Initialization{ID: InitID("p2"), ProjectID: "p2", Status: domain.InitBlocked, Stage: "verify", Version: 1, RepositorySHAs: map[string]string{}}
	f.m.sweep(context.Background())
	if len(f.agents.requests) != 0 {
		t.Fatalf("polling advanced a waiting state: %d agent starts", len(f.agents.requests))
	}
}

// Two workers with different owners must not execute the same initialization.
func TestLeaseAllowsOnlyOneExecutor(t *testing.T) {
	f := newFixture(t)
	f.repo.inits[InitID("p1")] = domain.Initialization{ID: InitID("p1"), ProjectID: "p1", Status: domain.InitRunning, Stage: "analyze", Version: 1, RepositorySHAs: map[string]string{"svc": "sha-dev"}}
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	f.agents.onStart = func(req domain.AgentRequest) (domain.Execution, error) {
		once.Do(func() { close(started) })
		<-release
		return domain.Execution{ID: req.ExecutionID, Status: "running"}, nil
	}
	done := make(chan struct{})
	go func() {
		f.m.sweep(context.Background())
		close(done)
	}()
	<-started
	other := &Manager{Store: f.repo, DataDir: f.m.DataDir, Agents: f.agents, Managed: f.m.Managed, Credential: f.m.Credential, Cloner: f.m.Cloner, Runner: f.m.Runner}
	other.sweep(context.Background())
	if len(f.agents.requests) != 1 {
		t.Fatalf("a second worker entered the same initialization: %d", len(f.agents.requests))
	}
	close(release)
	<-done
}

// A step holding stale state must not overwrite a cancellation written by the
// console in the meantime.
func TestStaleStepCannotOverwriteCancellation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.m.Ensure(ctx, f.project.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := f.m.Get(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.Cancel(ctx, f.project.ID); err != nil {
		t.Fatal(err)
	}
	stale.Status = domain.InitRunning
	if _, err := f.m.Advance(ctx, stale); err == nil {
		t.Fatal("a stale step overwrote the cancellation")
	}
	current, err := f.m.Get(ctx, f.project.ID)
	if err != nil || current.Status != domain.InitCancelled {
		t.Fatalf("cancellation was lost: %+v %v", current, err)
	}
}

// Verification must not execute when no isolated runtime is configured.
func TestVerificationIsBlockedWithoutIsolatedRuntime(t *testing.T) {
	f := newFixture(t)
	f.agents.onStart = readyAgent(true)
	f.m.Runner = nil
	f.m.VerifyImage = ""
	init := f.drive(t, 12)
	if init.Status != domain.InitBlocked || init.ReadyForTasks || !strings.Contains(init.Error, "INIT_VERIFY_IMAGE") {
		t.Fatalf("verification ran without isolation: %+v", init)
	}
}

// A failed verification stays retryable and a retry can complete.
func TestRetryAfterFailedVerification(t *testing.T) {
	f := newFixture(t)
	f.agents.onStart = readyAgent(true)
	f.m.Runner = func(context.Context, string, []string) (int, string, error) { return 1, "boom", nil }
	init := f.drive(t, 12)
	if init.Status != domain.InitBlocked || init.ReadyForTasks {
		t.Fatalf("failed verification not blocked: %+v", init)
	}
	if err := f.m.Retry(context.Background(), f.project.ID); err != nil {
		t.Fatal(err)
	}
	retried, _ := f.m.Get(context.Background(), f.project.ID)
	if retried.Status != domain.InitPending || retried.Stage != "verify" {
		t.Fatalf("retry did not resume verification: %+v", retried)
	}
	f.m.Runner = func(context.Context, string, []string) (int, string, error) { return 0, "ok", nil }
	done := f.drive(t, 12)
	if done.Status != domain.InitReady || !done.ReadyForTasks {
		t.Fatalf("retry did not complete: %+v", done)
	}
	if !f.m.ReadyForTasks(context.Background(), f.project.ID) {
		t.Fatal("ReadyForTasks did not persist")
	}
}
