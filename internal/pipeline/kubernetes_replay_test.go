package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// This requires a disposable local Temporal namespace. The workflow and all
// activities are stubs; it never reads production data or calls a code host.
func TestLiveKubernetesWorkflowHistoryReplayAndWorkerReplacement(t *testing.T) {
	address := os.Getenv("SWARMJET_REPLAY_TEMPORAL_ADDRESS")
	if address == "" {
		t.Skip("set SWARMJET_REPLAY_TEMPORAL_ADDRESS to an isolated local Temporal server")
	}
	namespace := os.Getenv("SWARMJET_REPLAY_TEMPORAL_NAMESPACE")
	if namespace == "" {
		t.Fatal("SWARMJET_REPLAY_TEMPORAL_NAMESPACE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id := fmt.Sprintf("kubernetes-history-%d", time.Now().UnixNano())
	queue := id + "-queue"
	var attempts atomic.Int32
	var current worker.Worker
	start := func() {
		t.Helper()
		current = worker.New(c, queue, worker.Options{WorkerStopTimeout: time.Second})
		current.RegisterWorkflowWithOptions(KubernetesTask, workflow.RegisterOptions{Name: KubernetesWorkflowName})
		current.RegisterActivityWithOptions(func(context.Context, domain.Task) error { return nil }, activity.RegisterOptions{Name: "SaveTask"})
		current.RegisterActivityWithOptions(func(_ context.Context, task domain.Task) (domain.Task, error) {
			if attempts.Add(1) == 1 {
				return task, errors.New("injected Runner interruption")
			}
			task.Executions = map[string]domain.Execution{"dev": {ID: task.ID + "-dev", Status: "succeeded"}}
			return task, nil
		}, activity.RegisterOptions{Name: "RunKubernetesTask"})
		current.RegisterActivityWithOptions(func(context.Context, string) error { return nil }, activity.RegisterOptions{Name: "StopKubernetesTask"})
		if err := current.Start(); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if current != nil {
			current.Stop()
		}
	}()
	start()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: queue, WorkflowExecutionTimeout: 40 * time.Second}, KubernetesWorkflowName, domain.Task{ID: id, AdmissionBackend: "kubernetes", Stage: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = c.CancelWorkflow(cleanup, id, run.GetRunID())
	}()
	for {
		value, queryErr := c.QueryWorkflow(ctx, id, run.GetRunID(), "state")
		if queryErr == nil {
			var task domain.Task
			if value.Get(&task) == nil && task.Stage == "blocked" {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("blocked state: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	current.Stop()
	current = nil
	if err = c.SignalWorkflow(ctx, id, run.GetRunID(), SignalName, domain.Command{ID: "retry-after-worker-replacement", Kind: "retry"}); err != nil {
		t.Fatal(err)
	}
	start()
	if err = run.Get(ctx, nil); err != nil {
		t.Fatalf("replacement worker could not complete old history: %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("Runner attempts=%d want=2", attempts.Load())
	}
	iter := c.GetWorkflowHistory(ctx, id, run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	history := &historypb.History{}
	for iter.HasNext() {
		event, nextErr := iter.Next()
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		history.Events = append(history.Events, event)
	}
	if len(history.Events) < 10 {
		t.Fatalf("too few history events: %d", len(history.Events))
	}
	if output := os.Getenv("SWARMJET_REPLAY_HISTORY_OUTPUT"); output != "" {
		data, marshalErr := protojson.Marshal(history)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(output, data, 0644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(KubernetesTask, workflow.RegisterOptions{Name: KubernetesWorkflowName})
	if err = replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatalf("KubernetesTaskV1 replay failed: %v", err)
	}
	t.Logf("replayed %d real Temporal events after worker replacement in namespace %s", len(history.Events), namespace)
}

func TestKubernetesWorkflowHistoryReplayFixture(t *testing.T) {
	path := filepath.Join("testdata", "kubernetes_v1_history.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	history, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Events) < 10 {
		t.Fatalf("too few history events: %d", len(history.Events))
	}
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(KubernetesTask, workflow.RegisterOptions{Name: KubernetesWorkflowName})
	if err = replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatalf("stored KubernetesTaskV1 history is incompatible: %v", err)
	}
}
