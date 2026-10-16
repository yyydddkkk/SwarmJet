// Package observability exposes bounded-cardinality operational metrics.
// Task, execution, delivery, and repository IDs belong in logs and traces,
// never in metric labels.
package observability

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry           *prometheus.Registry
	readinessMu        sync.RWMutex
	readiness          func(context.Context) error
	httpRequests       *prometheus.CounterVec
	httpDuration       *prometheus.HistogramVec
	messages           *prometheus.CounterVec
	admissionRuns      *prometheus.CounterVec
	admittedTasks      *prometheus.CounterVec
	activitySteps      *prometheus.CounterVec
	activityDuration   *prometheus.HistogramVec
	scanRuns           *prometheus.CounterVec
	scanDuration       *prometheus.HistogramVec
	admissionWaiting   prometheus.Gauge
	admissionOldest    prometheus.Gauge
	commandPending     prometheus.Gauge
	commandOldest      prometheus.Gauge
	runnerSlots        prometheus.Gauge
	legacyInboxPending prometheus.Gauge
	snapshotSuccess    prometheus.Gauge
}

func New() *Metrics {
	r := prometheus.NewRegistry()
	m := &Metrics{registry: r}
	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "swarmjet_http_requests_total", Help: "HTTP requests by bounded route and status class."}, []string{"route", "method", "status_class"})
	m.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "swarmjet_http_request_duration_seconds", Help: "HTTP request duration by bounded route.", Buckets: prometheus.DefBuckets}, []string{"route", "method"})
	m.messages = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "swarmjet_messages_processed_total", Help: "Durable message processing attempts."}, []string{"queue", "result"})
	m.admissionRuns = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "swarmjet_admission_runs_total", Help: "Admission cycle results."}, []string{"backend", "result"})
	m.admittedTasks = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "swarmjet_admitted_tasks_total", Help: "Tasks admitted by execution backend."}, []string{"backend"})
	m.activitySteps = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "swarmjet_activity_steps_total", Help: "Task activity step results."}, []string{"phase", "result"})
	m.activityDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "swarmjet_activity_step_duration_seconds", Help: "Task activity step duration.", Buckets: []float64{0.1, 0.5, 1, 5, 15, 60, 300, 900, 3600, 7200}}, []string{"phase"})
	m.scanRuns = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "swarmjet_scan_runs_total", Help: "Repository reconciliation scan results."}, []string{"scope", "result"})
	m.scanDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "swarmjet_scan_duration_seconds", Help: "Repository reconciliation scan duration.", Buckets: []float64{0.1, 0.5, 1, 5, 15, 60, 300, 900}}, []string{"scope"})
	m.admissionWaiting = prometheus.NewGauge(prometheus.GaugeOpts{Name: "swarmjet_admission_waiting", Help: "Tasks awaiting admission."})
	m.admissionOldest = prometheus.NewGauge(prometheus.GaugeOpts{Name: "swarmjet_admission_oldest_wait_seconds", Help: "Age of the oldest task awaiting admission."})
	m.commandPending = prometheus.NewGauge(prometheus.GaugeOpts{Name: "swarmjet_command_pending", Help: "Commands not yet delivered to Temporal."})
	m.commandOldest = prometheus.NewGauge(prometheus.GaugeOpts{Name: "swarmjet_command_oldest_wait_seconds", Help: "Age of the oldest undelivered command."})
	m.runnerSlots = prometheus.NewGauge(prometheus.GaugeOpts{Name: "swarmjet_runner_slots_reserved", Help: "Currently reserved Runner slots."})
	m.legacyInboxPending = prometheus.NewGauge(prometheus.GaugeOpts{Name: "swarmjet_legacy_inbox_pending", Help: "Unprocessed records in the legacy PostgreSQL webhook inbox."})
	m.snapshotSuccess = prometheus.NewGauge(prometheus.GaugeOpts{Name: "swarmjet_state_snapshot_success", Help: "Whether the most recent PostgreSQL state snapshot succeeded."})
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), m.httpRequests, m.httpDuration, m.messages, m.admissionRuns, m.admittedTasks, m.activitySteps, m.activityDuration, m.scanRuns, m.scanDuration, m.admissionWaiting, m.admissionOldest, m.commandPending, m.commandOldest, m.runnerSlots, m.legacyInboxPending, m.snapshotSuccess)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// SetReadiness installs a process-specific dependency check for /readyz.
func (m *Metrics) SetReadiness(check func(context.Context) error) {
	m.readinessMu.Lock()
	m.readiness = check
	m.readinessMu.Unlock()
}

func (m *Metrics) listenerHandler() http.Handler {
	routes := http.NewServeMux()
	routes.Handle("/metrics", m.Handler())
	routes.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	routes.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		m.readinessMu.RLock()
		check := m.readiness
		m.readinessMu.RUnlock()
		if check != nil && check(r.Context()) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
	return routes
}

// Serve exposes metrics on a separate listener. Deployments must keep this
// listener on a trusted network; it intentionally has no browser session auth.
func (m *Metrics) Serve(ctx context.Context, address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: m.listenerHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics listener stopped", "error", err)
		}
	}()
	return nil
}

func (m *Metrics) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorded := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorded, r)
		route := routeLabel(r.URL.Path)
		method := r.Method
		if method != http.MethodGet && method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch && method != http.MethodDelete && method != http.MethodHead {
			method = "OTHER"
		}
		m.httpRequests.WithLabelValues(route, method, fmt.Sprintf("%dxx", recorded.status/100)).Inc()
		m.httpDuration.WithLabelValues(route, method).Observe(time.Since(start).Seconds())
	})
}

func routeLabel(path string) string {
	switch path {
	case "/healthz":
		return "/healthz"
	case "/api/webhooks/gitlab", "/api/webhooks/github", "/api/operations", "/api/login", "/api/session":
		return path
	default:
		if strings.HasPrefix(path, "/api/") {
			return "/api/*"
		}
		return "static"
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status, w.wrote = status, true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		w.status, w.wrote = http.StatusSwitchingProtocols, true
	}
	return conn, rw, err
}

func (w *statusWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (w *statusWriter) ReadFrom(src io.Reader) (int64, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(w.ResponseWriter, src)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (m *Metrics) Message(queue, result string) {
	if queue != "webhook" && queue != "command" {
		return
	}
	if result != "acked" && result != "retry" && result != "dead_letter" {
		return
	}
	m.messages.WithLabelValues(queue, result).Inc()
}

func (m *Metrics) Admission(backend string, count int, err error) {
	if backend != "local_or_node" {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.admissionRuns.WithLabelValues(backend, result).Inc()
	if count > 0 {
		m.admittedTasks.WithLabelValues(backend).Add(float64(count))
	}
}

// Admitted counts an immediate admission performed while scheduling a task.
func (m *Metrics) Admitted(backend string) {
	if backend == "local" || backend == "node" {
		backend = "local_or_node"
	}
	if backend != "local_or_node" {
		return
	}
	m.admittedTasks.WithLabelValues(backend).Inc()
}

func (m *Metrics) Activity(phase string, elapsed time.Duration, err error) {
	phase = phaseLabel(phase)
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.activitySteps.WithLabelValues(phase, result).Inc()
	m.activityDuration.WithLabelValues(phase).Observe(elapsed.Seconds())
}

func (m *Metrics) Scan(scope string, elapsed time.Duration, err error) {
	if scope != "all" && scope != "repository" {
		return
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.scanRuns.WithLabelValues(scope, result).Inc()
	m.scanDuration.WithLabelValues(scope).Observe(elapsed.Seconds())
}

func phaseLabel(phase string) string {
	switch phase {
	case "waiting_dependencies", "preparing", "developing", "verifying", "reviewing", "publishing", "acquiring_merge", "waiting_manual_merge", "merging", "partial_merge", "finalizing", "stop", "pause_queue", "recover", "cancel":
		return phase
	default:
		return "other"
	}
}

type StateSnapshot struct {
	AdmissionWaiting       int
	AdmissionOldestWaitSec float64
	CommandPending         int
	CommandOldestWaitSec   float64
	RunnerSlotsReserved    int
	LegacyInboxPending     int
}

func (m *Metrics) State(snapshot StateSnapshot, err error) {
	if err != nil {
		m.snapshotSuccess.Set(0)
		return
	}
	m.admissionWaiting.Set(float64(snapshot.AdmissionWaiting))
	m.admissionOldest.Set(snapshot.AdmissionOldestWaitSec)
	m.commandPending.Set(float64(snapshot.CommandPending))
	m.commandOldest.Set(snapshot.CommandOldestWaitSec)
	m.runnerSlots.Set(float64(snapshot.RunnerSlotsReserved))
	m.legacyInboxPending.Set(float64(snapshot.LegacyInboxPending))
	m.snapshotSuccess.Set(1)
}
