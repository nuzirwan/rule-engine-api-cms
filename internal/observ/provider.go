package observ

import (
	"context"

	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// NewTracerProvider builds an OTel SDK TracerProvider around exp (a span
// exporter — an OTLP batch exporter in production, an in-memory exporter in
// tests). The service name is stamped as a resource attribute so spans are
// attributable to this engine. The caller owns Shutdown so traces are flushed on
// graceful shutdown (AC-24, wired by cmd/engine).
//
// Passing a batch processor-backed exporter keeps the hot path cheap; tests pass
// a SimpleSpanProcessor (via NewTestTracerProvider) for synchronous assertions.
func NewTracerProvider(exp sdktrace.SpanExporter) *sdktrace.TracerProvider {
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(tracerName),
	)
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
}

// NewTestTracerProvider builds a TracerProvider with a synchronous
// SimpleSpanProcessor so a test can assert on exported spans immediately after a
// span ends (no batching delay). It is exported for other slices' trace
// assertions (AC-21); production uses NewTracerProvider with a batch exporter.
func NewTestTracerProvider(exp sdktrace.SpanExporter) *sdktrace.TracerProvider {
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(tracerName),
	)
	return sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithResource(res),
	)
}

// ShutdownProvider flushes and shuts down tp, ignoring the error when ctx is
// already done (best-effort flush on shutdown).
func ShutdownProvider(ctx context.Context, tp *sdktrace.TracerProvider) error {
	if tp == nil {
		return nil
	}
	return tp.Shutdown(ctx)
}
