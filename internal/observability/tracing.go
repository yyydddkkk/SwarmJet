package observability

import (
	"context"
	"net/http"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
)

// StartTracing enables OTLP export when an endpoint is configured. The same
// propagator is used by HTTP, RabbitMQ, and Temporal's tracing interceptor.
func StartTracing(ctx context.Context, serviceName string) (interceptor.Interceptor, func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return nil, func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, nil, err
	}
	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	propagator := propagation.TraceContext{}
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagator)
	bridge, err := opentelemetry.NewTracingInterceptor(opentelemetry.TracerOptions{
		Tracer: provider.Tracer("temporal-sdk-go"), TextMapPropagator: propagator,
		AllowInvalidParentSpans: true, DisableBaggage: true,
	})
	if err != nil {
		_ = provider.Shutdown(ctx)
		return nil, nil, err
	}
	return bridge, provider.Shutdown, nil
}

// TraceHTTP uses only fixed attributes; paths, query strings, bodies, and
// credentials are deliberately absent from exported spans.
func TraceHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("swarmjet/http").Start(ctx, "http.request", trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.request.method", r.Method)))
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TraceOutboundHTTP starts a client span and injects only traceparent into the
// request headers. Callers end the span after the response has been processed.
func TraceOutboundHTTP(req *http.Request, peer string) (*http.Request, trace.Span) {
	ctx, span := otel.Tracer("swarmjet/platform-http").Start(req.Context(), "platform.http", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("peer.service", peer), attribute.String("http.request.method", req.Method)))
	req = req.WithContext(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	return req, span
}

// TraceParent returns the W3C traceparent for a durable task record. It is
// empty when tracing is disabled or the context has no valid sampled span.
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// ContinueTaskTrace restores a validated traceparent after an outbox delay.
// The parent is treated as remote so the next producer span is a true child.
func ContinueTaskTrace(ctx context.Context, parent string) context.Context {
	if parent == "" {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": parent})
}

func TraceID(ctx context.Context) string {
	span := trace.SpanContextFromContext(ctx)
	if !span.IsValid() {
		return ""
	}
	return span.TraceID().String()
}
