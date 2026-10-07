package gateway

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"nzr-rules-engine/internal/observ"
)

// TracingConfig holds configuration for distributed tracing.
type TracingConfig struct {
	// ServiceName is used as the instrumentation scope name.
	ServiceName string
}

// DefaultTracingConfig returns the default tracing configuration.
func DefaultTracingConfig() TracingConfig {
	return TracingConfig{
		ServiceName: "nzr-rules-gateway",
	}
}

// InjectTraceContext injects the trace context from ctx into the outgoing HTTP
// request using W3C trace context propagation (traceparent header). It also sets
// custom X-Request-Id and X-Trace-Id headers for legacy compatibility.
func InjectTraceContext(ctx context.Context, req *http.Request) {
	// Use OTEL global propagator for W3C trace context injection.
	// This sets traceparent and tracestate headers automatically.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	// Also set custom headers for legacy compatibility and explicit ID passing.
	if scope, ok := observ.ScopeFrom(ctx); ok {
		if scope.RequestID != "" {
			req.Header.Set("X-Request-Id", scope.RequestID)
		}
		if scope.TraceID != "" {
			req.Header.Set("X-Trace-Id", scope.TraceID)
		}
	}

	// Get OTel trace ID if available and set as custom header.
	if traceID := observ.TraceIDFromContext(ctx); traceID != "" {
		req.Header.Set("X-Trace-Id", traceID)
	}
}

// ExtractTraceContext extracts the trace context from incoming HTTP request
// headers using W3C trace context propagation. It returns a new context with
// the extracted trace span context, which allows child spans to be properly
// parented.
func ExtractTraceContext(ctx context.Context, r *http.Request) context.Context {
	// Use OTEL global propagator for W3C trace context extraction.
	// This reads traceparent and tracestate headers and returns a context
	// with the remote span context.
	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
}

// StartDispatchSpan starts a span for the gateway dispatch operation.
// The span is named "gateway.dispatch" and includes group and flow_id attributes.
func StartDispatchSpan(ctx context.Context, tracer observ.Tracer, group, flowID string) (context.Context, observ.Span) {
	if tracer == nil {
		return ctx, nopSpan{}
	}
	return tracer.StartSpan(ctx, "gateway.dispatch", map[string]any{
		"group":   group,
		"flow_id": flowID,
	})
}

// StartScaleSpan starts a span for a scaling operation.
// The span is named with the action (e.g., "gateway.scale.ensure_ready").
func StartScaleSpan(ctx context.Context, tracer observ.Tracer, action, group string) (context.Context, observ.Span) {
	if tracer == nil {
		return ctx, nopSpan{}
	}
	return tracer.StartSpan(ctx, "gateway.scale."+action, map[string]any{
		"group": group,
	})
}

// StartClientSpan starts a span for an outgoing worker client request.
func StartClientSpan(ctx context.Context, tracer observ.Tracer, group, endpoint string) (context.Context, observ.Span) {
	if tracer == nil {
		return ctx, nopSpan{}
	}
	return tracer.StartSpan(ctx, "gateway.client.execute", map[string]any{
		"group":    group,
		"endpoint": endpoint,
	})
}

// StartWorkerExecuteSpan starts a span for worker flow execution.
// The span is named "worker.execute" and includes flow_id attribute.
func StartWorkerExecuteSpan(ctx context.Context, tracer observ.Tracer, flowID, group string) (context.Context, observ.Span) {
	if tracer == nil {
		return ctx, nopSpan{}
	}
	return tracer.StartSpan(ctx, "worker.execute", map[string]any{
		"flow_id": flowID,
		"group":   group,
	})
}

// nopSpan is a no-op Span implementation used when no tracer is available.
type nopSpan struct{}

func (nopSpan) End(err error)          {}
func (nopSpan) Set(attr string, v any) {}

// TracingFields returns common tracing fields to include in log lines.
// It extracts request_id, trace_id, and group from context and adds the
// provided group if not already in scope.
func TracingFields(ctx context.Context, group string) map[string]any {
	fields := make(map[string]any)

	// Get request scope fields.
	if scope, ok := observ.ScopeFrom(ctx); ok {
		if scope.RequestID != "" {
			fields[observ.FldRequestID] = scope.RequestID
		}
		if scope.TraceID != "" {
			fields[observ.FldTraceID] = scope.TraceID
		}
	}

	// Get OTel trace ID if available (prefer over scope).
	if traceID := observ.TraceIDFromContext(ctx); traceID != "" {
		fields[observ.FldTraceID] = traceID
	}

	// Always include group.
	if group != "" {
		fields["group"] = group
	}

	return fields
}

// MergeFields merges additional fields into a base fields map.
// It returns a new map with all fields combined.
func MergeFields(base, extra map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}
