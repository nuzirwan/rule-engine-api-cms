package observ

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// tracerName is the instrumentation scope name OTel records on every span this
// package starts.
const tracerName = "nzr-rules-engine"

// otelTracer is a Tracer backed by OpenTelemetry (the frozen Tracer seam). It
// derives every span from the span already on ctx, so the root span created at
// the edge (httpapi middleware) is the ancestor of every node and connector span
// and the whole request is one trace with one trace_id (AC-21). The backend
// (OTLP/Jaeger/Tempo, or an in-memory exporter in tests) is swappable because the
// TracerProvider is injected (DIP, per low-level-design).
type otelTracer struct {
	tr       trace.Tracer
	redactor Redactor
}

// NewOTelTracer returns a Tracer that records spans through tp. A nil tp falls
// back to the global provider so the returned Tracer is always safe to use.
// Attribute values are redacted before they reach a span so no secret value is
// ever recorded (AC-20).
func NewOTelTracer(tp trace.TracerProvider) Tracer {
	return NewOTelTracerWithRedactor(tp, NewRedactor())
}

// NewOTelTracerWithRedactor is NewOTelTracer with an explicit Redactor so the
// composition root can share one Redactor (and its registered secret values)
// across the Tracer, Logger and dry-run collector.
func NewOTelTracerWithRedactor(tp trace.TracerProvider, r Redactor) Tracer {
	if tp == nil {
		// A nil provider yields a no-op tracer so the returned Tracer is always
		// safe to use (e.g. before the exporter is wired).
		return &otelTracer{tr: noop.NewTracerProvider().Tracer(tracerName), redactor: orNopRedactor(r)}
	}
	return &otelTracer{tr: tp.Tracer(tracerName), redactor: orNopRedactor(r)}
}

// StartSpan implements Tracer. It starts a child span of whatever span is on ctx
// (so the trace stays whole), stamps the request scope and the caller attrs, and
// returns the child context carrying the new span.
func (t *otelTracer) StartSpan(ctx context.Context, name string, attrs map[string]any) (context.Context, Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, s := t.tr.Start(ctx, name)
	sp := &otelSpan{s: s, redactor: t.redactor}
	if rs, ok := ScopeFrom(ctx); ok {
		sp.setScope(rs)
	}
	sp.setMap(attrs)
	return ctx, sp
}

// otelSpan is one open OTel span. It is not safe for concurrent Set.
type otelSpan struct {
	s        trace.Span
	redactor Redactor
	ended    bool
}

// Set implements Span. The value is redacted before it is recorded so a secret
// never reaches the span (AC-20); nil values are dropped to keep cardinality
// sane (per distributed-tracing).
func (sp *otelSpan) Set(attr string, v any) {
	if v == nil {
		return
	}
	scrubbed := sp.redactor.Scrub(map[string]any{attr: v})
	sp.s.SetAttributes(toKV(attr, scrubbed[attr]))
}

// End implements Span. It records the span status and, on error, the error_class
// read off the error (never by string match). Calling End more than once is a
// no-op after the first. Duration is computed by OTel from start to End.
func (sp *otelSpan) End(err error) {
	if sp.ended {
		return
	}
	sp.ended = true
	if err != nil {
		// The rendered error string is scrubbed before it reaches the span.
		sp.s.SetStatus(codes.Error, sp.redactor.ScrubString(err.Error()))
		sp.s.SetAttributes(
			attribute.String(FldSpanStatus, "error"),
			attribute.String(FldErrorClass, string(ClassOf(err))),
		)
	} else {
		sp.s.SetAttributes(attribute.String(FldSpanStatus, "ok"))
	}
	sp.s.End()
}

// setScope stamps the request-stable identity onto the span.
func (sp *otelSpan) setScope(rs RequestScope) {
	if rs.RequestID != "" {
		sp.s.SetAttributes(attribute.String(FldRequestID, rs.RequestID))
	}
	if rs.Env != "" {
		sp.s.SetAttributes(attribute.String(FldEnvironment, rs.Env))
	}
	if rs.FlowID != "" {
		sp.s.SetAttributes(attribute.String(FldFlowID, rs.FlowID))
	}
	if rs.FlowVersion != 0 {
		sp.s.SetAttributes(attribute.Int(FldFlowVersion, rs.FlowVersion))
	}
}

// setMap stamps the caller attrs after redaction.
func (sp *otelSpan) setMap(attrs map[string]any) {
	for k, v := range sp.redactor.Scrub(attrs) {
		if v == nil {
			continue
		}
		sp.s.SetAttributes(toKV(k, v))
	}
}

// TraceIDFromContext returns the hex trace id of the span on ctx, or "" if there
// is no recording span. The httpapi middleware uses it to seed RequestScope so
// the correlation id and the trace id are the same value (per distributed-tracing).
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

// toKV converts a single attribute to an OTel key/value, keeping types the
// backend understands and falling back to a string rendering for anything else
// (so cardinality is bounded by the caller, not by exotic types).
func toKV(key string, v any) attribute.KeyValue {
	switch t := v.(type) {
	case string:
		return attribute.String(key, t)
	case bool:
		return attribute.Bool(key, t)
	case int:
		return attribute.Int(key, t)
	case int32:
		return attribute.Int64(key, int64(t))
	case int64:
		return attribute.Int64(key, t)
	case float32:
		return attribute.Float64(key, float64(t))
	case float64:
		return attribute.Float64(key, t)
	default:
		return attribute.String(key, fmt.Sprintf("%v", t))
	}
}

// orNopRedactor returns r, or a nop Redactor when r is nil, so a Tracer built
// without one never panics.
func orNopRedactor(r Redactor) Redactor {
	if r == nil {
		return nopRedactor{}
	}
	return r
}
