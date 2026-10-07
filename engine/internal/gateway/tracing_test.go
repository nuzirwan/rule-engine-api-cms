package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"nzr-rules-engine/internal/observ"
)

func TestInjectTraceContext_WithOTelContext(t *testing.T) {
	// Set up in-memory span recorder.
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Set up the global propagator for W3C trace context.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Start a span so we have an active trace context.
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	// Create an HTTP request.
	req := httptest.NewRequest(http.MethodPost, "/execute", nil)

	// Inject trace context.
	InjectTraceContext(ctx, req)

	// Verify traceparent header is set.
	traceparent := req.Header.Get("traceparent")
	if traceparent == "" {
		t.Error("expected traceparent header to be set")
	}

	// Verify X-Trace-Id header is set.
	xTraceID := req.Header.Get("X-Trace-Id")
	if xTraceID == "" {
		t.Error("expected X-Trace-Id header to be set")
	}

	// Verify the trace ID matches what's in the OTel context.
	expectedTraceID := span.SpanContext().TraceID().String()
	if xTraceID != expectedTraceID {
		t.Errorf("X-Trace-Id mismatch: got %s, want %s", xTraceID, expectedTraceID)
	}
}

func TestInjectTraceContext_WithRequestScope(t *testing.T) {
	// Create context with request scope but no OTel span.
	ctx := observ.WithScope(context.Background(), observ.RequestScope{
		RequestID: "req-123",
		TraceID:   "trace-456",
	})

	// Create an HTTP request.
	req := httptest.NewRequest(http.MethodPost, "/execute", nil)

	// Inject trace context.
	InjectTraceContext(ctx, req)

	// Verify custom headers are set.
	if got := req.Header.Get("X-Request-Id"); got != "req-123" {
		t.Errorf("X-Request-Id: got %q, want %q", got, "req-123")
	}
	if got := req.Header.Get("X-Trace-Id"); got != "trace-456" {
		t.Errorf("X-Trace-Id: got %q, want %q", got, "trace-456")
	}
}

func TestExtractTraceContext_WithTraceparent(t *testing.T) {
	// Set up the global propagator for W3C trace context.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Create a request with traceparent header.
	// Format: version-trace_id-span_id-flags
	traceID := "0af7651916cd43dd8448eb211c80319c"
	spanID := "b7ad6b7169203331"
	traceparent := "00-" + traceID + "-" + spanID + "-01"

	req := httptest.NewRequest(http.MethodPost, "/execute", nil)
	req.Header.Set("traceparent", traceparent)

	// Extract trace context.
	ctx := ExtractTraceContext(context.Background(), req)

	// Verify the extracted trace ID.
	extractedTraceID := observ.TraceIDFromContext(ctx)
	if extractedTraceID != traceID {
		t.Errorf("extracted trace ID: got %q, want %q", extractedTraceID, traceID)
	}
}

func TestExtractTraceContext_NoTraceparent(t *testing.T) {
	// Set up the global propagator.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Create a request without traceparent header.
	req := httptest.NewRequest(http.MethodPost, "/execute", nil)

	// Extract trace context.
	ctx := ExtractTraceContext(context.Background(), req)

	// Should return empty trace ID.
	extractedTraceID := observ.TraceIDFromContext(ctx)
	if extractedTraceID != "" {
		t.Errorf("expected empty trace ID, got %q", extractedTraceID)
	}
}

func TestStartDispatchSpan(t *testing.T) {
	// Create a mock tracer.
	tracer := &mockTracer{}

	ctx, span := StartDispatchSpan(context.Background(), tracer, "test-group", "flow-123")

	if ctx == nil {
		t.Error("expected non-nil context")
	}
	if span == nil {
		t.Error("expected non-nil span")
	}

	// Verify the tracer was called with correct parameters.
	if tracer.lastSpanName != "gateway.dispatch" {
		t.Errorf("span name: got %q, want %q", tracer.lastSpanName, "gateway.dispatch")
	}
	if tracer.lastAttrs["group"] != "test-group" {
		t.Errorf("group attr: got %v, want %v", tracer.lastAttrs["group"], "test-group")
	}
	if tracer.lastAttrs["flow_id"] != "flow-123" {
		t.Errorf("flow_id attr: got %v, want %v", tracer.lastAttrs["flow_id"], "flow-123")
	}
}

func TestStartDispatchSpan_NilTracer(t *testing.T) {
	ctx, span := StartDispatchSpan(context.Background(), nil, "test-group", "flow-123")

	if ctx == nil {
		t.Error("expected non-nil context")
	}
	if span == nil {
		t.Error("expected non-nil span (nop span)")
	}

	// Should not panic when calling End on nop span.
	span.End(nil)
	span.Set("key", "value")
}

func TestStartScaleSpan(t *testing.T) {
	tracer := &mockTracer{}

	_, span := StartScaleSpan(context.Background(), tracer, "ensure_ready", "test-group")

	if tracer.lastSpanName != "gateway.scale.ensure_ready" {
		t.Errorf("span name: got %q, want %q", tracer.lastSpanName, "gateway.scale.ensure_ready")
	}
	if tracer.lastAttrs["group"] != "test-group" {
		t.Errorf("group attr: got %v, want %v", tracer.lastAttrs["group"], "test-group")
	}

	span.End(nil)
}

func TestTracingFields(t *testing.T) {
	// With request scope.
	ctx := observ.WithScope(context.Background(), observ.RequestScope{
		RequestID: "req-123",
		TraceID:   "trace-456",
	})

	fields := TracingFields(ctx, "test-group")

	if fields[observ.FldRequestID] != "req-123" {
		t.Errorf("request_id: got %v, want %v", fields[observ.FldRequestID], "req-123")
	}
	if fields[observ.FldTraceID] != "trace-456" {
		t.Errorf("trace_id: got %v, want %v", fields[observ.FldTraceID], "trace-456")
	}
	if fields["group"] != "test-group" {
		t.Errorf("group: got %v, want %v", fields["group"], "test-group")
	}
}

func TestTracingFields_EmptyContext(t *testing.T) {
	fields := TracingFields(context.Background(), "test-group")

	// Should only have group field.
	if fields["group"] != "test-group" {
		t.Errorf("group: got %v, want %v", fields["group"], "test-group")
	}
	if _, ok := fields[observ.FldRequestID]; ok {
		t.Error("expected no request_id field")
	}
}

func TestMergeFields(t *testing.T) {
	base := map[string]any{"a": 1, "b": 2}
	extra := map[string]any{"b": 3, "c": 4}

	result := MergeFields(base, extra)

	if result["a"] != 1 {
		t.Errorf("a: got %v, want %v", result["a"], 1)
	}
	if result["b"] != 3 {
		t.Errorf("b: got %v, want %v (extra should override)", result["b"], 3)
	}
	if result["c"] != 4 {
		t.Errorf("c: got %v, want %v", result["c"], 4)
	}

	// Verify original maps are unchanged.
	if base["b"] != 2 {
		t.Error("base map was modified")
	}
}

// mockTracer is a simple mock implementation of observ.Tracer for testing.
type mockTracer struct {
	lastSpanName string
	lastAttrs    map[string]any
}

func (t *mockTracer) StartSpan(ctx context.Context, name string, attrs map[string]any) (context.Context, observ.Span) {
	t.lastSpanName = name
	t.lastAttrs = attrs
	return ctx, &mockSpan{}
}

type mockSpan struct{}

func (s *mockSpan) End(err error)          {}
func (s *mockSpan) Set(attr string, v any) {}
