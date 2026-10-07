// Package worker implements the isolated worker binary that executes flows for a
// single group. Each worker loads only its assigned group's flows, JDMs, and
// connections, exposing an HTTP API for flow execution and health checks.
package worker

// ExecuteRequest is the wire format for POST /execute. The gateway forwards
// requests to workers using this shape.
type ExecuteRequest struct {
	// FlowID identifies which flow to execute. The worker looks it up in its
	// loaded flow set (only flows assigned to its group).
	FlowID string `json:"flowId"`

	// RequestID is the unique identifier for this request, used for tracing and
	// dedup. If empty, the worker generates one.
	RequestID string `json:"requestId,omitempty"`

	// TraceID is the distributed trace ID for linking spans. If empty, the worker
	// generates one (starting a new trace).
	TraceID string `json:"traceId,omitempty"`

	// Input is the request payload passed to the flow. It becomes Input in the
	// flow.Ctx and is accessible via path expressions in the flow tree.
	Input map[string]any `json:"input,omitempty"`
}

// ExecuteResponse is the wire format for POST /execute responses. It wraps the
// flow output or error detail so the gateway can relay the result to the client.
type ExecuteResponse struct {
	// Status is the HTTP status code the client should receive. 200 for success,
	// 4xx/5xx for errors.
	Status int `json:"status"`

	// Response is the flow output on success. It is the final Response map from
	// the flow.Ctx after the tree walk completes.
	Response map[string]any `json:"response,omitempty"`

	// Error is non-nil when the flow execution failed. It carries structured
	// error details for debugging and client display.
	Error *ErrorDetail `json:"error,omitempty"`
}

// ErrorDetail carries structured error information for failed flow executions.
type ErrorDetail struct {
	// Code is a machine-readable error code (e.g., "FLOW_NOT_FOUND", "TIMEOUT").
	Code string `json:"code"`

	// Message is a human-readable error description.
	Message string `json:"message"`

	// RequestID is echoed back for correlation.
	RequestID string `json:"requestId,omitempty"`
}

// DebugConfig is the response shape for GET /debug/config. It exposes the loaded
// configuration for debugging without sensitive data.
type DebugConfig struct {
	// GroupID is the worker's assigned group.
	GroupID string `json:"groupId"`

	// GroupVersion is the current loaded version (for hot-reload tracking).
	GroupVersion int `json:"groupVersion"`

	// FlowIDs lists the loaded flow identifiers.
	FlowIDs []string `json:"flowIds"`

	// ConnectionKeys lists the loaded connection keys.
	ConnectionKeys []string `json:"connectionKeys"`

	// JDMIDs lists the loaded JDM identifiers.
	JDMIDs []string `json:"jdmIds"`

	// Ready indicates whether the worker is ready to serve requests.
	Ready bool `json:"ready"`
}

// Error codes for ExecuteResponse.Error.Code.
const (
	ErrCodeFlowNotFound   = "FLOW_NOT_FOUND"
	ErrCodeValidation     = "VALIDATION_ERROR"
	ErrCodeTimeout        = "TIMEOUT"
	ErrCodeUpstream       = "UPSTREAM_ERROR"
	ErrCodeInternal       = "INTERNAL_ERROR"
	ErrCodeNotReady       = "NOT_READY"
	ErrCodeInvalidRequest = "INVALID_REQUEST"
)
