package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel/propagation"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Replay the pre-tracing history with the production tracing interceptor.
func TestKubernetesHistoryReplayWithTracing(t *testing.T) {
	bridge, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{AllowInvalidParentSpans: true, DisableBaggage: true, TextMapPropagator: propagation.TraceContext{}})
	if err != nil {
		t.Fatal(err)
	}
	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{Interceptors: []interceptor.WorkerInterceptor{bridge}})
	if err != nil {
		t.Fatal(err)
	}
	replayer.RegisterWorkflowWithOptions(KubernetesTask, workflow.RegisterOptions{Name: KubernetesWorkflowName})
	f, err := os.Open(filepath.Join("testdata", "kubernetes_v1_history.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	history, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatal(err)
	}
}
