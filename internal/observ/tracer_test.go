package observ

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newTestTracer wires an OTel tracer over an in-memory synchronous exporter so a
// test can read exported spans right after they end. It returns the Tracer, the
// exporter, and the shared Redactor.
func newTestTracer(t *testing.T) (Tracer, *tracetest.InMemoryExporter, Redactor) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := NewTestTracerProvider(exp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	r := NewRedactor()
	return NewOTelTracerWithRedactor(tp, r), exp, r
}

// TestOneTraceIDSpansWalk proves a root span and every nested node/child span
// share one trace_id, so the whole request is one trace (AC-21). It mirrors the
// span tree http.request -> node:condition -> zen.evaluate / node:action ->
// conn:pg.
func TestOneTraceIDSpansWalk(t *testing.T) {
	tr, exp, _ := newTestTracer(t)

	ctx := WithScope(context.Background(), RequestScope{RequestID: "req-1", Env: "dev"})
	ctx, root := tr.StartSpan(ctx, "http.request", map[string]any{"http.method": "GET"})

	// node:condition with a nested zen.evaluate child.
	cout := TraceNode(ctx, tr, nil, "n-cond", "condition", func(cctx context.Context) NodeOutcome {
		_, zspan := tr.StartSpan(cctx, "zen.evaluate", map[string]any{"jdm_id": "orders"})
		zspan.End(nil)
		return NodeOutcome{Branch: "TRUE"}
	})
	if cout.Branch != "TRUE" {
		t.Fatalf("condition branch = %q; want TRUE", cout.Branch)
	}

	// node:action with a nested conn:pg child.
	TraceNode(ctx, tr, nil, "n-act", "action", func(cctx context.Context) NodeOutcome {
		_, cspan := tr.StartSpan(cctx, "conn:pg", map[string]any{"db.system": "postgresql"})
		cspan.End(nil)
		return NodeOutcome{}
	})

	root.End(nil)

	spans := exp.GetSpans()
	if len(spans) != 5 {
		t.Fatalf("exported %d spans; want 5 (http.request, node:condition, zen.evaluate, node:action, conn:pg)", len(spans))
	}

	// All spans share one trace id.
	traceID := spans[0].SpanContext.TraceID()
	byName := map[string]tracetest.SpanStub{}
	for _, s := range spans {
		if s.SpanContext.TraceID() != traceID {
			t.Fatalf("span %q has trace id %s; want %s (one trace)", s.Name, s.SpanContext.TraceID(), traceID)
		}
		byName[s.Name] = s
	}

	// A node span carries node_id/node_type/duration_ms and branch_taken on the
	// condition (AC-2).
	cond := byName["node:condition"]
	if got := attrString(cond, FldNodeID); got != "n-cond" {
		t.Fatalf("condition node_id = %q; want n-cond", got)
	}
	if got := attrString(cond, FldBranchTaken); got != "TRUE" {
		t.Fatalf("condition branch_taken = %q; want TRUE", got)
	}
	if _, ok := attr(cond, FldDurationMs); !ok {
		t.Fatalf("condition span missing duration_ms")
	}

	// Children nest under the right parents (one trace, correct tree).
	if p := byName["zen.evaluate"].Parent.SpanID(); p != cond.SpanContext.SpanID() {
		t.Fatalf("zen.evaluate parent = %s; want node:condition %s", p, cond.SpanContext.SpanID())
	}
	act := byName["node:action"]
	if p := byName["conn:pg"].Parent.SpanID(); p != act.SpanContext.SpanID() {
		t.Fatalf("conn:pg parent = %s; want node:action %s", p, act.SpanContext.SpanID())
	}
}

// TestSpanRecordsErrorClass proves End(err) sets status=error and the error_class
// read off the error (never by message text).
func TestSpanRecordsErrorClass(t *testing.T) {
	tr, exp, _ := newTestTracer(t)
	_, span := tr.StartSpan(context.Background(), "node:action", nil)
	span.End(Classify(ClassUpstream, "db unreachable", errors.New("dial tcp")))

	s := exp.GetSpans()[0]
	if got := attrString(s, FldSpanStatus); got != "error" {
		t.Fatalf("status = %q; want error", got)
	}
	if got := attrString(s, FldErrorClass); got != string(ClassUpstream) {
		t.Fatalf("error_class = %q; want %s", got, ClassUpstream)
	}
}

// TestSpanNeverLeaksSecret proves a registered secret value set as a span attr is
// masked (AC-20).
func TestSpanNeverLeaksSecret(t *testing.T) {
	tr, exp, r := newTestTracer(t)
	const secret = "top-secret-key"
	RegisterSecret(r, secret)

	_, span := tr.StartSpan(context.Background(), "node:action", map[string]any{"apiKey": secret})
	span.Set("note", secret)
	span.End(nil)

	s := exp.GetSpans()[0]
	for _, kv := range s.Attributes {
		if strings.Contains(kv.Value.AsString(), secret) {
			t.Fatalf("secret leaked on span attr %s = %q", kv.Key, kv.Value.AsString())
		}
	}
}

// TestTraceNodeFeedsCollectorAndLogger proves TraceNode records a dry-run step
// and emits a per-node debug line with the canonical fields.
func TestTraceNodeFeedsCollectorAndLogger(t *testing.T) {
	tr, _, r := newTestTracer(t)

	var buf bytes.Buffer
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelDebug)
	log := NewJSONLoggerWithRedactor(&buf, lv, r)

	coll := NewTraceCollector(r)
	ctx := WithCollector(context.Background(), coll)

	TraceNode(ctx, tr, log, "n1", "set", func(cctx context.Context) NodeOutcome {
		return NodeOutcome{Branch: "B"}
	})

	rec := coll.Result(ctx, true)
	if len(rec.Steps) != 1 {
		t.Fatalf("collector steps = %d; want 1", len(rec.Steps))
	}
	st := rec.Steps[0]
	if st.NodeID != "n1" || st.NodeType != "set" || st.Branch != "B" {
		t.Fatalf("step = %+v; want n1/set/B", st)
	}

	var line map[string]any
	if err := json.Unmarshal(trimLastLine(buf.Bytes()), &line); err != nil {
		t.Fatalf("log line not JSON: %v (%s)", err, buf.String())
	}
	if line[FldNodeID] != "n1" || line[FldNodeType] != "set" || line[FldBranchTaken] != "B" {
		t.Fatalf("log fields = %v; want n1/set/B", line)
	}
}

// --- helpers ---

func attr(s tracetest.SpanStub, key string) (any, bool) {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value.AsInterface(), true
		}
	}
	return nil, false
}

func attrString(s tracetest.SpanStub, key string) string {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}
	return ""
}

// trimLastLine returns the last non-empty line of b (slog writes one JSON object
// per line; a single Emit writes exactly one).
func trimLastLine(b []byte) []byte {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return []byte(lines[len(lines)-1])
}
