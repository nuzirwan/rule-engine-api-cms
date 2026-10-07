package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"

	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"
)

// WorkerClient manages HTTP communication with worker pods. It maintains a pooled
// HTTP client and per-group circuit breakers following the resilience pattern from
// internal/connect/resilience.go.
type WorkerClient struct {
	httpClient *http.Client
	breakers   map[string]*gobreaker.CircuitBreaker[*worker.ExecuteResponse]
	mu         sync.RWMutex
	cfg        BreakerConfig
	log        observ.Logger
}

// NewWorkerClient creates a WorkerClient with connection pooling configured.
// The HTTP transport is tuned for worker communication:
// - MaxIdleConnsPerHost=10: maintain connection pool per worker service
// - IdleConnTimeout=90s: recycle idle connections after 90s
func NewWorkerClient(log observ.Logger) *WorkerClient {
	transport := &http.Transport{
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		// Use reasonable defaults for other settings
		MaxIdleConns:      100,
		ForceAttemptHTTP2: false, // Workers use HTTP/1.1
	}

	return &WorkerClient{
		httpClient: &http.Client{
			Transport: transport,
			// No default timeout; context controls deadline per request
		},
		breakers: make(map[string]*gobreaker.CircuitBreaker[*worker.ExecuteResponse]),
		cfg:      BreakerConfig{}, // Use defaults
		log:      log,
	}
}

// NewWorkerClientWithConfig creates a WorkerClient with custom breaker config.
func NewWorkerClientWithConfig(log observ.Logger, cfg BreakerConfig) *WorkerClient {
	c := NewWorkerClient(log)
	c.cfg = cfg
	return c
}

// Execute sends an ExecuteRequest to the worker endpoint and returns the response.
// It wraps the HTTP call with a circuit breaker for the worker's group.
// Trace headers (X-Request-Id, X-Trace-Id, traceparent) are propagated from context.
// Returns both response and error when the worker returns an error response (allows
// caller to inspect error details from the worker).
func (c *WorkerClient) Execute(ctx context.Context, group, endpoint string, req *worker.ExecuteRequest) (*worker.ExecuteResponse, error) {
	breaker := c.getBreaker(group)

	resp, err := breaker.Execute(func() (*worker.ExecuteResponse, error) {
		return c.doExecute(ctx, endpoint, req)
	})
	if err != nil {
		// Check if breaker is open
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			c.log.Emit(ctx, "warn", "gateway.client.breaker_open", map[string]any{
				"group": group,
			})
			return nil, &DispatchError{
				Code:    ErrCodeBreakerOpen,
				Message: fmt.Sprintf("circuit breaker open for group %s", group),
				Group:   group,
			}
		}
		// Return both response (if any) and error so caller can inspect worker error details
		return resp, err
	}
	return resp, nil
}

// getBreaker returns the circuit breaker for the group, creating one if needed.
func (c *WorkerClient) getBreaker(group string) *gobreaker.CircuitBreaker[*worker.ExecuteResponse] {
	// Fast path: read lock
	c.mu.RLock()
	b, ok := c.breakers[group]
	c.mu.RUnlock()
	if ok {
		return b
	}

	// Slow path: write lock and create
	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check after acquiring write lock
	if b, ok = c.breakers[group]; ok {
		return b
	}
	b = newGroupBreaker(group, c.cfg)
	c.breakers[group] = b
	return b
}

// doExecute performs the actual HTTP request to the worker.
func (c *WorkerClient) doExecute(ctx context.Context, endpoint string, req *worker.ExecuteRequest) (*worker.ExecuteResponse, error) {
	// Marshal request body
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInternal,
			Message: "failed to marshal request",
			cause:   err,
		}
	}

	// Build HTTP request
	url := endpoint + "/execute"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeInternal,
			Message: "failed to create request",
			cause:   err,
		}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Propagate trace headers from context
	c.propagateTraceHeaders(ctx, httpReq)

	// Execute request
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// Classify the error
		if ctx.Err() != nil {
			return nil, &DispatchError{
				Code:    ErrCodeTimeout,
				Message: "request timeout",
				cause:   ctx.Err(),
			}
		}
		return nil, &DispatchError{
			Code:    ErrCodeUpstream,
			Message: "worker request failed",
			cause:   err,
		}
	}
	defer httpResp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, &DispatchError{
			Code:    ErrCodeUpstream,
			Message: "failed to read response body",
			cause:   err,
		}
	}

	// Parse response
	var execResp worker.ExecuteResponse
	if err := json.Unmarshal(respBody, &execResp); err != nil {
		// If we can't parse JSON, but got a non-2xx status, report that
		if httpResp.StatusCode >= 400 {
			return nil, &DispatchError{
				Code:    classifyHTTPStatus(httpResp.StatusCode),
				Message: fmt.Sprintf("worker returned status %d", httpResp.StatusCode),
			}
		}
		return nil, &DispatchError{
			Code:    ErrCodeInternal,
			Message: "failed to parse response",
			cause:   err,
		}
	}

	// Check HTTP status first (before checking response error)
	if httpResp.StatusCode >= 400 {
		// If response has error detail, use that
		if execResp.Error != nil {
			return &execResp, &DispatchError{
				Code:    execResp.Error.Code,
				Message: execResp.Error.Message,
			}
		}
		return &execResp, &DispatchError{
			Code:    classifyHTTPStatus(httpResp.StatusCode),
			Message: fmt.Sprintf("worker returned status %d", httpResp.StatusCode),
		}
	}

	// Check for error in response (even with 2xx status, response might have error)
	if execResp.Error != nil {
		return &execResp, &DispatchError{
			Code:    execResp.Error.Code,
			Message: execResp.Error.Message,
		}
	}

	return &execResp, nil
}

// propagateTraceHeaders copies trace headers from context to the outgoing request.
// It uses the observ package's scope and trace utilities.
func (c *WorkerClient) propagateTraceHeaders(ctx context.Context, req *http.Request) {
	// Get request scope from context
	if scope, ok := observ.ScopeFrom(ctx); ok {
		if scope.RequestID != "" {
			req.Header.Set("X-Request-Id", scope.RequestID)
		}
		if scope.TraceID != "" {
			req.Header.Set("X-Trace-Id", scope.TraceID)
		}
	}

	// Get OTel trace ID if available
	if traceID := observ.TraceIDFromContext(ctx); traceID != "" {
		req.Header.Set("X-Trace-Id", traceID)
		// Also set W3C traceparent for OTel propagation
		// Format: 00-{trace_id}-{span_id}-{flags}
		// We set a minimal version without span_id since we don't have it here
		req.Header.Set("traceparent", fmt.Sprintf("00-%s-0000000000000000-01", traceID))
	}
}

// classifyHTTPStatus maps HTTP status codes to error codes.
func classifyHTTPStatus(status int) string {
	switch {
	case status == 404:
		return ErrCodeNotFound
	case status == 408 || status == 504:
		return ErrCodeTimeout
	case status >= 400 && status < 500:
		return ErrCodeValidation
	case status >= 500:
		return ErrCodeUpstream
	default:
		return ErrCodeInternal
	}
}

// Error codes for DispatchError.
const (
	ErrCodeWorkerNotFound = "WORKER_NOT_FOUND"
	ErrCodeWorkerNotReady = "WORKER_NOT_READY"
	ErrCodeBreakerOpen    = "BREAKER_OPEN"
	ErrCodeTimeout        = "TIMEOUT"
	ErrCodeUpstream       = "UPSTREAM_ERROR"
	ErrCodeInternal       = "INTERNAL_ERROR"
	ErrCodeValidation     = "VALIDATION_ERROR"
	ErrCodeNotFound       = "NOT_FOUND"
)

// DispatchError is a classified error from the gateway dispatcher or client.
type DispatchError struct {
	Code    string
	Message string
	Group   string
	cause   error
}

func (e *DispatchError) Error() string {
	if e.Group != "" {
		return fmt.Sprintf("%s: %s [group=%s]", e.Code, e.Message, e.Group)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *DispatchError) Unwrap() error {
	return e.cause
}
