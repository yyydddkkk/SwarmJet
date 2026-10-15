package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/proto"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/mq"
	"github.com/yyydddkkk/SwarmJet/internal/observability"
	"github.com/yyydddkkk/SwarmJet/internal/pipeline"
)

type fixtureLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

type fixtureTraceExports struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (e *fixtureTraceExports) Add(body []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bodies = append(e.bodies, append([]byte(nil), body...))
}

func (e *fixtureTraceExports) Bodies() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]byte(nil), e.bodies...)
}

func (b *fixtureLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *fixtureLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// TestObservabilityTaskFixture follows one task through the real RabbitMQ,
// PostgreSQL, and Temporal boundaries. The code host is an HTTP fixture whose
// dependency call fails, so no Runner or external repository can be changed.
// Use a disposable RabbitMQ broker and an isolated Temporal namespace with no
// other consumers: queue names match the application queues and task queue.
func TestObservabilityTaskFixture(t *testing.T) {
	if os.Getenv("SWARMJET_OBSERVABILITY_FIXTURE") != "1" {
		t.Skip("set SWARMJET_OBSERVABILITY_FIXTURE=1 with isolated dependencies")
	}
	brokerURL := os.Getenv("SWARMJET_RABBITMQ_TEST_URL")
	temporalAddress := os.Getenv("SWARMJET_TEMPORAL_ADDRESS")
	namespace := os.Getenv("SWARMJET_TEMPORAL_NAMESPACE")
	if brokerURL == "" || temporalAddress == "" || namespace == "" || namespace == "swarmjet" {
		t.Fatal("disposable RabbitMQ and a non-production Temporal namespace are required")
	}
	s := scanTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var platformCalls atomic.Int32
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeScanLabelEvent(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues"):
			_, _ = w.Write([]byte(`[{"project_id":7,"iid":1,"title":"fixture","state":"opened","labels":["autonomous"]}]`))
		case strings.HasSuffix(r.URL.Path, "/links"):
			platformCalls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"injected dependency outage"}`))
		default:
			t.Errorf("unexpected GitLab fixture path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gitlab.Close()
	if _, err := s.SaveProject(ctx, scanProject(gitlab.URL)); err != nil {
		t.Fatal(err)
	}
	grantScanActor(t, s, scanProject(gitlab.URL))
	metrics := observability.New()
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	defer otel.SetTracerProvider(previousProvider)
	defer otel.SetTextMapPropagator(previousPropagator)
	var exports fixtureTraceExports
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		exports.Add(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", receiver.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	traceBridge, shutdownTracing, err := observability.StartTracing(ctx, "swarmjet-fixture")
	if err != nil || traceBridge == nil {
		t.Fatalf("tracing setup: bridge=%v err=%v", traceBridge, err)
	}
	defer shutdownTracing(context.Background())
	var logs fixtureLogBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	broker := mq.New(brokerURL)
	defer broker.Close()
	tc, err := client.DialContext(ctx, client.Options{HostPort: temporalAddress, Namespace: namespace, Interceptors: []interceptor.ClientInterceptor{traceBridge}})
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	svc := &Service{Store: s, Metrics: metrics, WebhookPublisher: broker, CommandPublisher: broker, Temporal: tc, RequireNode: true, ResolveCredential: func(string) (string, error) { return "fixture-token", nil }}
	workerCtx, stopConsumers := context.WithCancel(ctx)
	defer stopConsumers()
	go broker.ConsumeBatch(workerCtx, mq.WebhookQueue, func(ctx context.Context, bodies [][]byte) []error {
		results := svc.ProcessWebhookDeliveries(ctx, bodies)
		for _, result := range results {
			if result == nil {
				metrics.Message("webhook", "acked")
			} else {
				metrics.Message("webhook", "retry")
			}
		}
		return results
	})
	go broker.Consume(workerCtx, mq.CommandQueue, func(ctx context.Context, body []byte) error {
		err := svc.ConsumeCommand(ctx, body)
		if err == nil {
			metrics.Message("command", "acked")
		} else {
			metrics.Message("command", "retry")
		}
		return err
	})
	a := &pipeline.Activities{Store: s, Metrics: metrics, Credential: func(string) (string, error) { return "fixture-token", nil }}
	w := worker.New(tc, pipeline.Queue, worker.Options{WorkerStopTimeout: time.Second})
	w.RegisterWorkflowWithOptions(pipeline.WorkTask, workflow.RegisterOptions{Name: pipeline.WorkflowName})
	w.RegisterActivityWithOptions(a.Step, activity.RegisterOptions{Name: "Step"})
	w.RegisterActivityWithOptions(a.SaveTask, activity.RegisterOptions{Name: "SaveTask"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	const deliveryID = "observability-fixture-delivery"
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/gitlab", strings.NewReader(`{"project":{"id":7}}`))
	request.Header.Set("X-Gitlab-Token", "fixture-secret")
	request.Header.Set("webhook-id", deliveryID)
	response := httptest.NewRecorder()
	observability.TraceHTTP(metrics.HTTP(svc.Webhook("fixture-secret"))).ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("webhook status=%d", response.Code)
	}
	var taskID string
	until := func(name string, check func() bool) {
		t.Helper()
		for !check() {
			if ctx.Err() != nil {
				t.Fatalf("%s timeout: %v", name, ctx.Err())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	until("queued task", func() bool {
		tasks, err := s.Tasks(ctx)
		if err != nil || len(tasks) != 1 || tasks[0].AdmissionStatus != "waiting" {
			return false
		}
		taskID = tasks[0].ID
		return true
	})
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = tc.TerminateWorkflow(cleanup, taskID, "", "fixture complete")
	}()
	if err := s.HeartbeatNode(ctx, domain.Node{ID: "fixture-node", Address: "http://fixture.invalid", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	admitted, err := s.AdmitPendingTasks(ctx, 20)
	metrics.Admission("local_or_node", admitted, err)
	if err != nil || admitted != 1 {
		t.Fatalf("admission=%d: %v", admitted, err)
	}
	if err := svc.PublishCommands(ctx, broker); err != nil {
		t.Fatal(err)
	}
	until("Activity and external call", func() bool {
		return platformCalls.Load() > 0 && strings.Contains(logs.String(), `"task_id":"`+taskID+`"`) && strings.Contains(logs.String(), `"phase":"waiting_dependencies"`)
	})
	scrape := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, metric := range []string{
		`swarmjet_http_requests_total{method="POST",route="/api/webhooks/gitlab",status_class="2xx"} 1`,
		`swarmjet_messages_processed_total{queue="webhook",result="acked"} 1`,
		`swarmjet_admitted_tasks_total{backend="local_or_node"} 1`,
		`swarmjet_messages_processed_total{queue="command",result="acked"} 1`,
		`swarmjet_activity_steps_total{phase="waiting_dependencies",result="error"}`,
	} {
		until("metric "+metric, func() bool {
			scrape = httptest.NewRecorder()
			metrics.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			return strings.Contains(scrape.Body.String(), metric)
		})
	}
	if strings.Contains(scrape.Body.String(), taskID) || strings.Contains(scrape.Body.String(), deliveryID) || strings.Contains(scrape.Body.String(), "fixture-token") {
		t.Fatal("high-cardinality ID or credential leaked into metrics")
	}
	for _, value := range []string{taskID, deliveryID, `"phase":"waiting_dependencies"`} {
		if !strings.Contains(logs.String(), value) {
			t.Fatalf("missing correlation value %s in structured logs", value)
		}
	}
	var delivered int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM commands WHERE delivered`).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	var receipt int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM webhook_receipts WHERE delivery_id=$1`, deliveryID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if receipt != 1 || delivered != 1 {
		t.Fatalf("durable receipt=%d delivered commands=%d", receipt, delivered)
	}
	if err := shutdownTracing(ctx); err != nil {
		t.Fatal(err)
	}
	spanNames := map[string]bool{}
	traceIDs := map[string]bool{}
	for _, body := range exports.Bodies() {
		if bytes.Contains(body, []byte("fixture-token")) || bytes.Contains(body, []byte("injected dependency outage")) {
			t.Fatal("credential or platform response leaked into trace export")
		}
		var exported collectortracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &exported); err != nil {
			t.Fatalf("decode OTLP export: %v", err)
		}
		for _, resource := range exported.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					spanNames[span.Name] = true
					traceIDs[string(span.TraceId)] = true
				}
			}
		}
	}
	for _, name := range []string{"http.request", "rabbitmq.publish", "rabbitmq.consume_batch", "StartWorkflow:WorkTask", "RunActivity:Step", "platform.http"} {
		if !spanNames[name] {
			t.Fatalf("missing trace span %q; spans=%v", name, spanNames)
		}
	}
	if len(traceIDs) != 1 {
		t.Fatalf("task spans split across %d traces: spans=%v", len(traceIDs), spanNames)
	}
	for id := range traceIDs {
		if !strings.Contains(logs.String(), `"trace_id":"`+hex.EncodeToString([]byte(id))+`"`) {
			t.Fatal("structured task logs do not carry the exported trace ID")
		}
	}
	data, _ := json.Marshal(map[string]any{"task_id": taskID, "delivery_id": deliveryID, "webhook_receipts": receipt, "delivered_commands": delivered, "platform_calls": platformCalls.Load()})
	t.Logf("fixture evidence: %s trace_spans=%d trace_ids=%d", data, len(spanNames), len(traceIDs))
}
