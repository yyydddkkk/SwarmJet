package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func scrape(t *testing.T, metrics *Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", recorder.Code)
	}
	return recorder.Body.String()
}

func TestHTTPMetricsDoNotExposeDynamicPaths(t *testing.T) {
	metrics := New()
	handler := metrics.HTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/tasks/private-task-id", nil))
	body := scrape(t, metrics)
	if !strings.Contains(body, `swarmjet_http_requests_total{method="GET",route="/api/*",status_class="5xx"} 1`) {
		t.Fatalf("missing bounded HTTP metric: %s", body)
	}
	if strings.Contains(body, "private-task-id") {
		t.Fatal("dynamic task ID leaked into metrics")
	}
}

func TestHTTPMetricsKeepWebSocketUpgrade(t *testing.T) {
	metrics := New()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(metrics.HTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, payload, err := conn.ReadMessage()
		if err == nil {
			_ = conn.WriteMessage(websocket.TextMessage, payload)
		}
	})))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/terminal/private-task-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := conn.ReadMessage()
	if err != nil || string(payload) != "hello" {
		t.Fatalf("websocket echo=%q err=%v", payload, err)
	}
	if body := scrape(t, metrics); !strings.Contains(body, `status_class="1xx"`) {
		t.Fatalf("upgrade not recorded: %s", body)
	}
}

func TestStateSnapshotFailureIsVisible(t *testing.T) {
	metrics := New()
	metrics.State(StateSnapshot{AdmissionWaiting: 3, CommandPending: 2}, nil)
	metrics.State(StateSnapshot{}, errors.New("database unavailable"))
	body := scrape(t, metrics)
	for _, wanted := range []string{"swarmjet_admission_waiting 3", "swarmjet_command_pending 2", "swarmjet_state_snapshot_success 0"} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("missing %q in metrics", wanted)
		}
	}
}

func TestListenerHealthAndReadiness(t *testing.T) {
	metrics := New()
	metrics.SetReadiness(func(context.Context) error { return errors.New("secret dependency detail") })
	check := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		metrics.listenerHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}
	if got := check("/healthz"); got.Code != http.StatusOK {
		t.Fatalf("health status=%d", got.Code)
	}
	if got := check("/readyz"); got.Code != http.StatusServiceUnavailable || strings.Contains(got.Body.String(), "secret dependency detail") {
		t.Fatalf("unready response=%d %q", got.Code, got.Body.String())
	}
	metrics.SetReadiness(func(context.Context) error { return nil })
	if got := check("/readyz"); got.Code != http.StatusOK {
		t.Fatalf("ready status=%d", got.Code)
	}
}

func TestImmediateAdmissionCountsOnlyKnownBackend(t *testing.T) {
	metrics := New()
	metrics.Admitted("local")
	metrics.Admitted("node")
	metrics.Admitted("unexpected")
	body := scrape(t, metrics)
	if !strings.Contains(body, `swarmjet_admitted_tasks_total{backend="local_or_node"} 2`) || strings.Contains(body, `backend="unexpected"`) {
		t.Fatalf("unexpected admission series: %s", body)
	}
}
