package observ

import (
	"context"
	"time"
)

// NodeOutcome is what a traced node execution reports back to TraceNode: the
// branch it selected (empty for a linear node) and the error (nil on success).
// It is a small value type so observ does not need to import flow (which would
// invert the flow -> observ dependency and create an import cycle). Slice A
// adapts its flow.Directive to this shape at the call site.
type NodeOutcome struct {
	Branch string
	Err    error
}

// TraceNode wraps a single node execution in a child span and emits the per-node
// debug trace line, so the span-per-node field set and conventions are defined
// once (AC-21, AC-2). The returned ctx inside exec carries the child span, so any
// connector/ZEN call made inside exec nests under this node's span and the whole
// request stays one trace.
//
// exec receives the child-span context and returns the node outcome. TraceNode
// stamps node_id/node_type, duration_ms and branch_taken on the span, ends it
// with the outcome error (which sets status + error_class), emits a debug trace
// line with the same fields, and feeds the dry-run collector when one is attached
// (so dry-run trace == live trace shape).
func TraceNode(ctx context.Context, tr Tracer, log Logger, nodeID, nodeType string,
	exec func(ctx context.Context) NodeOutcome) NodeOutcome {

	cctx, span := tr.StartSpan(ctx, "node:"+nodeType, map[string]any{
		FldNodeID:   nodeID,
		FldNodeType: nodeType,
	})
	start := time.Now()
	out := exec(cctx)
	durMs := time.Since(start).Milliseconds()

	span.Set(FldDurationMs, durMs)
	if out.Branch != "" {
		span.Set(FldBranchTaken, out.Branch)
	}
	span.End(out.Err)

	fields := map[string]any{
		FldNodeID:     nodeID,
		FldNodeType:   nodeType,
		FldDurationMs: durMs,
	}
	if out.Branch != "" {
		fields[FldBranchTaken] = out.Branch
	}
	level := "debug"
	if out.Err != nil {
		level = "error"
		fields[FldErrorClass] = string(ClassOf(out.Err))
	}
	if log != nil {
		log.Emit(cctx, level, "node", fields)
	}

	if c, ok := CollectorFrom(cctx); ok {
		attrs := map[string]any{FldDurationMs: durMs}
		if out.Err != nil {
			attrs[FldErrorClass] = string(ClassOf(out.Err))
		}
		c.Record(nodeID, nodeType, out.Branch, attrs)
	}
	return out
}
