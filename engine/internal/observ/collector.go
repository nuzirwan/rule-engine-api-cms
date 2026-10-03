package observ

import (
	"context"
	"sync"
)

// TraceCollector accumulates a node-by-node trace while the normal interpreter
// runs (frozen seam, lld-contracts.md). The dry-run endpoint (Slice D) attaches
// one via WithCollector; the per-node trace helper (TraceNode) calls Record for
// every node so the dry-run trace has the same shape as a live trace (no
// divergent code path). On live traffic no collector is attached and the hot
// path pays only one context lookup.
//
// The Record signature is the frozen contract: (nodeID, nodeType, branch, attrs).
// attrs carries the per-node extras (duration_ms, inputs, decision, wrote, …)
// already redacted by the caller / Record itself.
type TraceCollector interface {
	Record(nodeID, nodeType, branch string, attrs map[string]any)
}

// TraceStep is one recorded node in a dry-run / trace-mode run, in execution
// order. The field tags match the dry-run endpoint's JSON body (Slice D owns the
// HTTP envelope; this slice owns the record contract).
type TraceStep struct {
	NodeID     string         `json:"node_id"`
	NodeType   string         `json:"node_type"`
	Branch     string         `json:"branch_taken,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	Attrs      map[string]any `json:"attrs,omitempty"`
}

// TraceRecord is the full ordered trace returned verbatim as the dry-run
// endpoint's JSON body.
type TraceRecord struct {
	TraceID     string      `json:"trace_id,omitempty"`
	FlowID      string      `json:"flow_id,omitempty"`
	FlowVersion int         `json:"flow_version,omitempty"`
	Env         string      `json:"environment,omitempty"`
	DryRun      bool        `json:"dry_run"`
	Steps       []TraceStep `json:"steps"`
}

// recordingCollector is the concrete TraceCollector. It is safe for concurrent
// Record (a Parallel node records from several goroutines) and redacts every
// attr map so dry-run output cannot leak a secret (AC-20).
type recordingCollector struct {
	redactor Redactor

	mu    sync.Mutex
	steps []TraceStep
}

// NewTraceCollector returns a TraceCollector that redacts recorded attrs with r
// (nil => a no-op redactor). Read the accumulated trace with Record's owner via
// Result.
func NewTraceCollector(r Redactor) *recordingCollector {
	return &recordingCollector{redactor: orNopRedactor(r)}
}

// Record implements TraceCollector. duration_ms, when present in attrs as an
// int/int64, is lifted onto the step; the remaining attrs are kept (redacted).
func (c *recordingCollector) Record(nodeID, nodeType, branch string, attrs map[string]any) {
	step := TraceStep{NodeID: nodeID, NodeType: nodeType, Branch: branch}
	scrubbed := c.redactor.Scrub(attrs)
	if scrubbed != nil {
		if d, ok := scrubbed[FldDurationMs]; ok {
			step.DurationMs = toInt64(d)
			delete(scrubbed, FldDurationMs)
		}
		if len(scrubbed) > 0 {
			step.Attrs = scrubbed
		}
	}
	c.mu.Lock()
	c.steps = append(c.steps, step)
	c.mu.Unlock()
}

// Result returns the accumulated trace. scope stamps the record-level identity
// (trace/flow/env) read from ctx; dryRun marks whether writes were suppressed.
func (c *recordingCollector) Result(ctx context.Context, dryRun bool) TraceRecord {
	c.mu.Lock()
	steps := make([]TraceStep, len(c.steps))
	copy(steps, c.steps)
	c.mu.Unlock()

	rec := TraceRecord{DryRun: dryRun, Steps: steps, TraceID: TraceIDFromContext(ctx)}
	if rs, ok := ScopeFrom(ctx); ok {
		if rec.TraceID == "" {
			rec.TraceID = rs.TraceID
		}
		rec.FlowID = rs.FlowID
		rec.FlowVersion = rs.FlowVersion
		rec.Env = rs.Env
	}
	return rec
}

// collectorKey / dryRunKey are the unexported context keys for the dry-run
// plumbing (collector handle + the write-suppression flag).
type collectorKey struct{}
type dryRunKey struct{}

// WithCollector attaches c to ctx so TraceNode records each node into it. Live
// traffic never calls this, so CollectorFrom returns false and the hot path is
// untouched (seam addition, Slice E — option a, context-carried).
func WithCollector(ctx context.Context, c TraceCollector) context.Context {
	return context.WithValue(ctx, collectorKey{}, c)
}

// CollectorFrom returns the TraceCollector on ctx, if any.
func CollectorFrom(ctx context.Context) (TraceCollector, bool) {
	if ctx == nil {
		return nil, false
	}
	c, ok := ctx.Value(collectorKey{}).(TraceCollector)
	return c, ok
}

// WithDryRun marks ctx as a dry-run so write-side effects are suppressed. Slice
// D's dry-run handler sets this; Slice A's action handler checks IsDryRun and,
// for a write op, skips the I/O and records wrote:"suppressed" (AC-14).
func WithDryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryRunKey{}, true)
}

// IsDryRun reports whether ctx is a dry-run. A false result is the live-traffic
// fast path (one context lookup, no allocation).
func IsDryRun(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(dryRunKey{}).(bool)
	return v
}

// toInt64 coerces an int-like attr to int64 for the step's duration_ms.
func toInt64(v any) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	default:
		return 0
	}
}
