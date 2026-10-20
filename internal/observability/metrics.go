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

// Metrics keeps only the signals that still have a data source after the
// pipeline, node and admission code was removed: HTTP requests and the two
// remaining River queues (webhook and message delivery).
type Metrics struct {
	registry     *prometheus.Registry
	readinessMu  sync.RWMutex
	readiness    func(context.Context) error
	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	messages     *prometheus.CounterVec
}

func New() *Metrics {
	r := prometheus.NewRegistry()
	m := &Metrics{registry: r}
	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "swarmjet_http_requests_total", Help: "HTTP requests by bounded route and status class."}, []string{"route", "method", "status_class"})
	m.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "swarmjet_http_request_duration_seconds", Help: "HTTP request duration by bounded route.", Buckets: prometheus.DefBuckets}, []string{"route", "method"})
	m.messages = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "swarmjet_messages_processed_total", Help: "Durable message processing attempts."}, []string{"queue", "result"})
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), m.httpRequests, m.httpDuration, m.messages)
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
	case "/healthz", "/readyz", "/mcp", "/api/webhooks/github", "/api/login", "/api/session":
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
	if queue != "webhook" && queue != "message" {
		return
	}
	if result != "acked" && result != "retry" && result != "dead_letter" {
		return
	}
	m.messages.WithLabelValues(queue, result).Inc()
}
