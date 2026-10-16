package fakeagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func request(id, taskID, role string) domain.AgentRequest {
	return domain.AgentRequest{ExecutionID: id, TaskID: taskID, Role: role}
}

func TestStartWithoutScriptFailsLoudly(t *testing.T) {
	b := New()
	_, err := b.Start(context.Background(), request("exec-1", "task", "dev"))
	if !errors.Is(err, ErrNoScript) {
		t.Fatalf("missing script returned %v, want ErrNoScript", err)
	}
	if len(b.Calls()) != 0 {
		t.Fatal("a refused start must not be recorded as a call")
	}
}

func TestSuccessWithoutResultIsRejected(t *testing.T) {
	b := New().Script("dev", Step{})
	_, err := b.Start(context.Background(), request("exec-1", "task", "dev"))
	if !errors.Is(err, ErrMissingResult) {
		t.Fatalf("empty success returned %v, want ErrMissingResult", err)
	}
}

func TestScriptsAreConsumedPerRoleInOrder(t *testing.T) {
	b := New().
		Script("dev", Changed("first"), Changed("second")).
		Script("review", ReviewApproved("looks good"))
	ctx := context.Background()
	first, err := b.Start(ctx, request("dev-1", "task", "dev"))
	if err != nil || first.Status != StatusSucceeded || first.Result.Outcome != "changed" {
		t.Fatalf("first dev = %+v err=%v", first, err)
	}
	second, err := b.Start(ctx, request("dev-2", "task", "dev"))
	if err != nil || second.Result.Summary != "second" {
		t.Fatalf("second dev = %+v err=%v", second, err)
	}
	review, err := b.Start(ctx, request("review-1", "task", "review"))
	if err != nil || review.Result.Outcome != "approved" {
		t.Fatalf("review = %+v err=%v", review, err)
	}
	if _, err = b.Start(ctx, request("dev-3", "task", "dev")); !errors.Is(err, ErrNoScript) {
		t.Fatalf("exhausted dev script returned %v", err)
	}
	if roles := b.Roles(); !reflect.DeepEqual(roles, []string{"dev", "dev", "review"}) {
		t.Fatalf("roles = %v", roles)
	}
	if len(b.CallsForRole("dev")) != 2 || b.CallsForRole("dev")[1].Index != 2 {
		t.Fatalf("call bookkeeping = %+v", b.CallsForRole("dev"))
	}
}

func TestFailureTimeoutAndInvalidResultStatuses(t *testing.T) {
	b := New().Script("dev", Failed("model crashed"), TimedOut(), InvalidResult())
	ctx := context.Background()
	failed, err := b.Start(ctx, request("dev-1", "task", "dev"))
	if err != nil || failed.Status != StatusFailed || failed.FailureReason != "model crashed" {
		t.Fatalf("failed = %+v err=%v", failed, err)
	}
	timedOut, err := b.Start(ctx, request("dev-2", "task", "dev"))
	if err != nil || timedOut.FailureCode != FailureTimeout {
		t.Fatalf("timeout = %+v err=%v", timedOut, err)
	}
	invalid, err := b.Start(ctx, request("dev-3", "task", "dev"))
	if err != nil || invalid.FailureCode != FailureInvalidResult || invalid.ExitCode != 0 {
		t.Fatalf("invalid result = %+v err=%v", invalid, err)
	}
}

func TestRetryAdoptsExistingExecution(t *testing.T) {
	b := New().Script("dev", Changed("first"), Changed("second"))
	ctx := context.Background()
	first, err := b.Start(ctx, request("dev-1", "task", "dev"))
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := b.Start(ctx, request("dev-1", "task", "dev"))
	if err != nil || adopted.Result.Summary != first.Result.Summary {
		t.Fatalf("retry did not adopt the durable execution: %+v err=%v", adopted, err)
	}
	if len(b.CallsForRole("dev")) != 1 {
		t.Fatalf("adoption consumed a script step: %+v", b.CallsForRole("dev"))
	}
}

func TestTransientErrorConsumesNextStep(t *testing.T) {
	failure := errors.New("docker unavailable")
	b := New().Script("dev", Transient(failure), Changed("recovered"))
	ctx := context.Background()
	if _, err := b.Start(ctx, request("dev-1", "task", "dev")); !errors.Is(err, failure) {
		t.Fatalf("transient error = %v", err)
	}
	// The failed Start left no execution record, so the retried activity with
	// the same ID must consume the next scripted step.
	recovered, err := b.Start(ctx, request("dev-1", "task", "dev"))
	if err != nil || recovered.Result.Summary != "recovered" {
		t.Fatalf("retry after transient error = %+v err=%v", recovered, err)
	}
	calls := b.CallsForRole("dev")
	if len(calls) != 2 || calls[0].Err == nil {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestRunningExecutionIsCancellableAndRecoverable(t *testing.T) {
	b := New().Script("dev", Running())
	ctx := context.Background()
	ex, err := b.Start(ctx, request("dev-1", "task", "dev"))
	if err != nil || ex.Status != StatusRunning {
		t.Fatalf("start = %+v err=%v", ex, err)
	}
	live, err := b.TaskExecutions(ctx, "task")
	if err != nil || len(live) != 1 || live[0].Status != StatusRunning {
		t.Fatalf("task executions = %+v err=%v", live, err)
	}
	if err = b.CancelTask(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	cancelled, err := b.Inspect(ctx, "dev-1")
	if err != nil || cancelled.Status != StatusCancelled {
		t.Fatalf("after cancel = %+v err=%v", cancelled, err)
	}
}

func TestCancelUnknownExecutionFails(t *testing.T) {
	b := New()
	if err := b.Cancel(context.Background(), "missing"); !errors.Is(err, ErrUnknownExecution) {
		t.Fatalf("cancel unknown = %v", err)
	}
	if _, err := b.Inspect(context.Background(), "missing"); !errors.Is(err, ErrUnknownExecution) {
		t.Fatalf("inspect unknown = %v", err)
	}
}

func TestFilesAndCommitsAreRecorded(t *testing.T) {
	dir := t.TempDir()
	b := New().Script("dev", WithCommits(
		WithFiles(Changed("implemented"), map[string]string{"service/main.go": "package main\n"}),
		"feat: implement endpoint",
	))
	req := request("dev-1", "task", "dev")
	req.Workspace = dir
	if _, err := b.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "service", "main.go"))
	if err != nil || string(content) != "package main\n" {
		t.Fatalf("workspace file = %q err=%v", content, err)
	}
	if files := b.Files(); files[filepath.Join(dir, "service", "main.go")] != "package main\n" {
		t.Fatalf("recorded files = %+v", files)
	}
	if commits := b.Commits("dev"); !reflect.DeepEqual(commits, []string{"feat: implement endpoint"}) {
		t.Fatalf("commits = %v", commits)
	}
}

func TestReadOnlyWorkspaceIsNotWritten(t *testing.T) {
	dir := t.TempDir()
	b := New().Script("dev", WithFiles(Succeeded(domain.AgentResult{Outcome: "changed"}), map[string]string{"leak.txt": "x"}))
	req := request("dev-1", "task", "dev")
	req.Workspace = dir
	req.ReadOnlyWorkspace = true
	if _, err := b.Start(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "leak.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only workspace was modified: %v", err)
	}
}

func TestWildcardRoleIsOnlyUsedAsFallback(t *testing.T) {
	b := New().Script(WildcardRole, Changed("fallback")).Script("review", ReviewApproved("explicit"))
	ctx := context.Background()
	explicit, err := b.Start(ctx, request("review-1", "task", "review"))
	if err != nil || explicit.Result.Summary != "explicit" {
		t.Fatalf("explicit script = %+v err=%v", explicit, err)
	}
	fallback, err := b.Start(ctx, request("dev-1", "task", "dev"))
	if err != nil || fallback.Result.Summary != "fallback" {
		t.Fatalf("wildcard script = %+v err=%v", fallback, err)
	}
}

func TestAtIndexesByRoleAndCallNumber(t *testing.T) {
	b := New().
		At("dev", 2, Changed("second call")).
		At("dev", 1, Changed("first call"))
	ctx := context.Background()
	first, err := b.Start(ctx, request("dev-1", "task", "dev"))
	if err != nil || first.Result.Summary != "first call" {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	second, err := b.Start(ctx, request("dev-2", "task", "dev"))
	if err != nil || second.Result.Summary != "second call" {
		t.Fatalf("second = %+v err=%v", second, err)
	}
}

func TestAtFillsUnscriptedGapsLoudly(t *testing.T) {
	b := New().At("dev", 2, Changed("second call"))
	ex, err := b.Start(context.Background(), request("dev-1", "task", "dev"))
	if err != nil {
		t.Fatal(err)
	}
	if ex.Status != StatusFailed || ex.FailureCode != "script_gap" {
		t.Fatalf("unscripted call silently succeeded: %+v", ex)
	}
}

func TestUnsafeWorkspacePathIsRejected(t *testing.T) {
	dir := t.TempDir()
	b := New().Script("dev", WithFiles(Changed("bad"), map[string]string{"../escape.txt": "x"}))
	req := request("dev-1", "task", "dev")
	req.Workspace = dir
	if _, err := b.Start(context.Background(), req); err == nil {
		t.Fatal("path escape was accepted")
	}
}
