package pipeline

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// TestLiveTemporalWorkerRestart uses only its uniquely named workflow and queue.
// Business activities are local stubs: no model, GitLab or deployment mutation.
func TestLiveTemporalWorkerRestart(t *testing.T) {
	address := os.Getenv("SWARMJET_TEMPORAL_ADDRESS")
	if address == "" {
		t.Skip("set SWARMJET_TEMPORAL_ADDRESS to test a real Temporal server")
	}
	namespace := os.Getenv("SWARMJET_TEMPORAL_NAMESPACE")
	if namespace == "" {
		namespace = "swarmjet"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	temporalClient, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(temporalClient.Close)
	unique := fmt.Sprintf("swarmjet-restart-test-%d", time.Now().UnixNano())
	queue := unique + "-queue"
	var ready atomic.Bool
	var dependencyChecks, developerExecutions atomic.Int32
	var currentWorker worker.Worker
	t.Cleanup(func() {
		if currentWorker != nil {
			currentWorker.Stop()
		}
	})
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = temporalClient.CancelWorkflow(cleanup, unique, "")
	})
	startWorker := func() {
		t.Helper()
		currentWorker = worker.New(temporalClient, queue, worker.Options{WorkerStopTimeout: time.Second})
		currentWorker.RegisterWorkflowWithOptions(WorkTask, workflow.RegisterOptions{Name: WorkflowName})
		currentWorker.RegisterActivityWithOptions(func(context.Context, domain.Task) error { return nil }, activity.RegisterOptions{Name: "SaveTask"})
		currentWorker.RegisterActivityWithOptions(func(_ context.Context, input StepInput) (StepResult, error) {
			out := successfulStep(input)
			if input.Phase == "waiting_dependencies" {
				dependencyChecks.Add(1)
				out.Ready = ready.Load()
			}
			if input.Phase == "developing" {
				developerExecutions.Add(1)
			}
			return out, nil
		}, activity.RegisterOptions{Name: "Step"})
		if err := currentWorker.Start(); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(description string, condition func() bool) {
		t.Helper()
		for !condition() {
			select {
			case <-ctx.Done():
				t.Fatalf("%s: %v", description, ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	startWorker()
	task := workflowTask()
	task.ID = unique
	task.Workspace.TaskID = unique
	execution, err := temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: unique, TaskQueue: queue, WorkflowExecutionTimeout: 40 * time.Second}, WorkflowName, task)
	if err != nil {
		t.Fatal(err)
	}
	waitFor("first dependency check", func() bool { return dependencyChecks.Load() > 0 })
	currentWorker.Stop()
	currentWorker = nil
	// Successful delivery while there is no worker proves these commands are held
	// by the server; the next worker must obtain them through durable history.
	for _, command := range []domain.Command{{ID: "pause-during-outage", Kind: "pause"}, {ID: "note-during-outage", Kind: "note", Text: "recorded while worker was stopped"}} {
		if err = temporalClient.SignalWorkflow(ctx, unique, "", SignalName, command); err != nil {
			t.Fatal(err)
		}
	}
	startWorker()
	var recovered domain.Task
	waitFor("replayed paused state and note", func() bool {
		queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		value, queryErr := temporalClient.QueryWorkflow(queryCtx, unique, "", "state")
		if queryErr != nil {
			return false
		}
		if queryErr = value.Get(&recovered); queryErr != nil {
			t.Errorf("decode state query: %v", queryErr)
			return false
		}
		return recovered.Paused && len(recovered.Notes) == 1
	})
	if recovered.Stage != "waiting_dependencies" || recovered.Notes[0] != "recorded while worker was stopped" {
		t.Fatalf("worker restart lost checkpoint/signals: %+v", recovered)
	}
	if developerExecutions.Load() != 0 {
		t.Fatal("development started while dependencies blocked and task paused")
	}
	ready.Store(true)
	if err = temporalClient.SignalWorkflow(ctx, unique, "", SignalName, domain.Command{ID: "resume-after-restart", Kind: "resume"}); err != nil {
		t.Fatal(err)
	}
	if err = temporalClient.SignalWorkflow(ctx, unique, "", SignalName, domain.Command{ID: "wake-after-restart", Kind: "dependencies", Dependencies: []domain.IssueRef{}}); err != nil {
		t.Fatal(err)
	}
	if err = execution.Get(ctx, nil); err != nil {
		t.Fatalf("resumed workflow failed: %v", err)
	}
	if developerExecutions.Load() != 1 {
		t.Fatalf("developer executions=%d want=1", developerExecutions.Load())
	}
	t.Logf("real Temporal %s namespace=%s workflow=%s queue=%s survived worker restart, preserved pause/note and completed with one developer execution", address, namespace, unique, queue)
}
