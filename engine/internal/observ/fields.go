package observ

import "context"

// Canonical attribute keys (HLD §3). Used verbatim as both slog field names and
// OTel span attribute keys so a log line and its span share the vocabulary and
// join on identical keys (AC-21). This is the single source of truth for the
// field set — the automatic per-node trace and the Logger node both emit these.
const (
	FldTraceID     = "trace_id"
	FldRequestID   = "request_id"
	FldFlowID      = "flow_id"
	FldFlowVersion = "flow_version"
	FldNodeID      = "node_id"
	FldNodeType    = "node_type"
	FldEnvironment = "environment"
	FldLabel       = "label"
	FldDurationMs  = "duration_ms"
	FldBranchTaken = "branch_taken"
	// Outcome fields (not in the §3 list but required by error-classification
	// across the seam).
	FldErrorClass = "error_class" // Timeout|NotFound|Validation|Upstream|Internal
	FldSpanStatus = "status"      // ok|error
)

// RequestScope carries the request-stable identity extracted once at the edge
// (httpapi middleware) and threaded via context into every node/connector call.
// The Tracer and Logger read it so every emission is auto-stamped — a node
// author never passes these by hand.
type RequestScope struct {
	TraceID     string
	RequestID   string
	Env         string
	FlowID      string
	FlowVersion int
}

// scopeKey is the unexported context key for RequestScope (avoids collisions).
type scopeKey struct{}

// WithScope returns a child context carrying rs. The httpapi middleware stamps
// the scope at the edge; downstream reads it via ScopeFrom.
func WithScope(ctx context.Context, rs RequestScope) context.Context {
	return context.WithValue(ctx, scopeKey{}, rs)
}

// ScopeFrom returns the RequestScope on ctx, if any.
func ScopeFrom(ctx context.Context) (RequestScope, bool) {
	if ctx == nil {
		return RequestScope{}, false
	}
	rs, ok := ctx.Value(scopeKey{}).(RequestScope)
	return rs, ok
}

// EnrichScope returns a child context whose RequestScope has flowID, version and
// env stamped onto it, preserving the trace/request ids already present. The
// resolver (Slice D) calls this the moment the active flow version is pinned, so
// from that point every span and log line carries flow_id/flow_version/
// environment (seam addition, Slice E — additive helper, no signature change).
func EnrichScope(ctx context.Context, flowID string, version int, env string) context.Context {
	rs, _ := ScopeFrom(ctx)
	rs.FlowID = flowID
	rs.FlowVersion = version
	rs.Env = env
	return WithScope(ctx, rs)
}
