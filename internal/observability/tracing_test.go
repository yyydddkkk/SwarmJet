package observability

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestStartTracingExportsHTTPSpan(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	defer otel.SetTracerProvider(previousProvider)
	defer otel.SetTextMapPropagator(previousPropagator)
	received := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", receiver.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bridge, shutdown, err := StartTracing(ctx, "swarmjet-test")
	if err != nil {
		t.Fatal(err)
	}
	if bridge == nil {
		t.Fatal("Temporal interceptor missing")
	}
	handler := TraceHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/test?secret=hidden", nil))
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received:
		if len(body) == 0 {
			t.Fatal("empty OTLP export")
		}
		if bytes.Contains(body, []byte("hidden")) {
			t.Fatal("request query leaked into trace export")
		}
	case <-ctx.Done():
		t.Fatal("OTLP export not received")
	}
}
