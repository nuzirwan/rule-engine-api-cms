package worker

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Handler provides HTTP endpoints for the worker binary.
type Handler struct {
	worker *Worker
}

// NewHandler creates an HTTP handler for the given worker.
func NewHandler(w *Worker) *Handler {
	return &Handler{worker: w}
}

// Mount registers all worker endpoints on the given mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /execute", h.handleExecute)
	mux.HandleFunc("GET /healthz", h.handleHealthz)
	mux.HandleFunc("GET /readyz", h.handleReadyz)
	mux.HandleFunc("GET /debug/config", h.handleDebugConfig)
}

// handleExecute processes POST /execute requests.
func (h *Handler) handleExecute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()

	// Decode the request first.
	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ExecuteResponse{
			Status: http.StatusBadRequest,
			Error: &ErrorDetail{
				Code:    ErrCodeInvalidRequest,
				Message: "invalid request body: " + err.Error(),
			},
		})
		return
	}

	// Validate required fields.
	if req.FlowID == "" {
		writeJSON(w, http.StatusBadRequest, ExecuteResponse{
			Status: http.StatusBadRequest,
			Error: &ErrorDetail{
				Code:    ErrCodeInvalidRequest,
				Message: "flowId is required",
			},
		})
		return
	}

	// Check readiness — reject if not ready.
	if !h.worker.Ready() {
		writeJSON(w, http.StatusServiceUnavailable, ExecuteResponse{
			Status: http.StatusServiceUnavailable,
			Error: &ErrorDetail{
				Code:    ErrCodeNotReady,
				Message: "worker not ready",
			},
		})
		return
	}

	// Generate IDs if not provided.
	reqID := req.RequestID
	if reqID == "" {
		reqID = uuid.New().String()
	}
	traceID := req.TraceID
	if traceID == "" {
		traceID = uuid.New().String()
	}

	// Extract trace context from headers if present (propagation).
	if headerReqID := r.Header.Get("X-Request-Id"); headerReqID != "" && req.RequestID == "" {
		reqID = headerReqID
	}
	if headerTraceID := r.Header.Get("X-Trace-Id"); headerTraceID != "" && req.TraceID == "" {
		traceID = headerTraceID
	}

	// Execute the flow.
	response, err := h.worker.Execute(ctx, req.FlowID, reqID, traceID, req.Input)
	duration := time.Since(start)

	if err != nil {
		// Record metrics for failed request.
		if m := h.worker.Metrics(); m != nil {
			m.ObserveRequest(h.worker.GroupID(), req.FlowID, "error", duration)
		}

		// Classify the error for the response.
		code, status := classifyError(err)
		writeJSON(w, status, ExecuteResponse{
			Status: status,
			Error: &ErrorDetail{
				Code:      code,
				Message:   err.Error(),
				RequestID: reqID,
			},
		})
		return
	}

	// Record metrics for successful request.
	if m := h.worker.Metrics(); m != nil {
		m.ObserveRequest(h.worker.GroupID(), req.FlowID, "ok", duration)
	}

	// Success response.
	writeJSON(w, http.StatusOK, ExecuteResponse{
		Status:   http.StatusOK,
		Response: response,
	})
}

// handleHealthz handles GET /healthz (liveness probe).
// Always returns 200 OK if the process is running.
func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz handles GET /readyz (readiness probe).
// Returns 200 only when the worker is ready to serve requests.
func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if h.worker.Ready() {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("not ready"))
}

// handleDebugConfig handles GET /debug/config (protected debug endpoint).
// Returns the loaded configuration for debugging.
func (h *Handler) handleDebugConfig(w http.ResponseWriter, r *http.Request) {
	// TODO: Add authentication check here for production.
	// For now, this endpoint is unprotected (internal use only).

	cfg := h.worker.DebugConfig()
	writeJSON(w, http.StatusOK, cfg)
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// classifyError maps an execution error to an error code and HTTP status.
func classifyError(err error) (code string, status int) {
	if err == nil {
		return "", http.StatusOK
	}

	msg := strings.ToLower(err.Error())

	// Check for specific error patterns.
	switch {
	case strings.Contains(msg, "flow not found"):
		return ErrCodeFlowNotFound, http.StatusNotFound
	case strings.Contains(msg, "worker not loaded"):
		return ErrCodeNotReady, http.StatusServiceUnavailable
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return ErrCodeTimeout, http.StatusGatewayTimeout
	case strings.Contains(msg, "validation"):
		return ErrCodeValidation, http.StatusBadRequest
	case strings.Contains(msg, "upstream"), strings.Contains(msg, "connection"):
		return ErrCodeUpstream, http.StatusBadGateway
	default:
		return ErrCodeInternal, http.StatusInternalServerError
	}
}
