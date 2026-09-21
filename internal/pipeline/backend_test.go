package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
)

// A backend with no Docker client or filesystem demonstrates that recovery uses
// the lifecycle contract, including execution IDs unrelated to container names.
type recoveryBackend struct {
	execution.AgentBackend
	executions []domain.Execution
	cancelled  []string
	inspected  []string
	failCancel error
}

func (b *recoveryBackend) TaskExecutions(context.Context, string) ([]domain.Execution, error) {
	return b.executions, nil
}

func (b *recoveryBackend) Cancel(_ context.Context, id string) error {
	b.cancelled = append(b.cancelled, id)
	return b.failCancel
}

func (b *recoveryBackend) Inspect(_ context.Context, id string) (domain.Execution, error) {
	b.inspected = append(b.inspected, id)
	return domain.Execution{ID: id, SessionID: "exact-session", Status: "cancelled"}, nil
}

func TestRecoveryUsesBackendAndOnlyMatchingExecutions(t *testing.T) {
	b := &recoveryBackend{executions: []domain.Execution{
		{ID: "previous-attempt", Status: "running"},
		{ID: "attempt", Status: "succeeded"},
		{ID: "attempt-merge-1", Status: "running"},
		{ID: "unrelated", Status: "running"},
	}}
	// Both an exact ID and its retry/merge suffixes belong to this recovery.
	a := Activities{Agents: b}
	out, err := a.Step(context.Background(), StepInput{
		Phase: "recover", ExecutionID: "attempt",
		Task: domain.Task{ID: "task", Stage: "merging"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.cancelled, []string{"attempt-merge-1"}) || !reflect.DeepEqual(b.inspected, b.cancelled) {
		t.Fatalf("cancelled=%v inspected=%v", b.cancelled, b.inspected)
	}
	ex := out.Task.Executions["merge"]
	if ex.ID != "attempt-merge-1" || ex.Status != "cancelled" || ex.SessionID != "exact-session" {
		t.Fatalf("recovery lost backend state: %+v", ex)
	}
}

func TestRecoveryDoesNotHideBackendCancellationFailure(t *testing.T) {
	failure := errors.New("execution host unavailable")
	b := &recoveryBackend{
		executions: []domain.Execution{{ID: "attempt", Status: "running"}},
		failCancel: failure,
	}
	a := Activities{Agents: b}
	_, err := a.Step(context.Background(), StepInput{
		Phase: "recover", ExecutionID: "attempt", Task: domain.Task{ID: "task"},
	})
	if !errors.Is(err, failure) || len(b.inspected) != 0 {
		t.Fatalf("err=%v inspected=%v", err, b.inspected)
	}
}
