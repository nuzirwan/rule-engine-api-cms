package gateway

import (
	"context"
	"fmt"
	"time"

	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"
)

// DispatchConfig holds configuration for the Dispatcher.
type DispatchConfig struct {
	// RequestTimeout is the timeout applied to each dispatch request.
	// Defaults to 30s if zero.
	RequestTimeout time.Duration
}

// Default dispatch configuration values.
const (
	defaultRequestTimeout = 30 * time.Second
)

// Dispatcher routes execute requests to worker pods based on group assignment.
// It looks up workers via the registry, checks readiness, and forwards requests
// through the WorkerClient with circuit breaker protection.
type Dispatcher struct {
	registry *WorkerRegistry
	client   *WorkerClient
	config   DispatchConfig
	log      observ.Logger
}

// NewDispatcher creates a Dispatcher with the given dependencies.
func NewDispatcher(registry *WorkerRegistry, client *WorkerClient, config DispatchConfig, log observ.Logger) *Dispatcher {
	return &Dispatcher{
		registry: registry,
		client:   client,
		config:   config,
		log:      log,
	}
}

// Dispatch routes a flow execution request to the appropriate worker for the group.
// It returns an error if the worker is not found or not ready (NO fallback per design).
// The request is forwarded to the worker's /execute endpoint with trace headers propagated.
func (d *Dispatcher) Dispatch(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
	// Lookup worker via registry
	workerState, ok := d.registry.GetWorker(group)
	if !ok {
		d.log.Emit(ctx, "warn", "gateway.dispatch.worker_not_found", map[string]any{
			"group":  group,
			"flowId": flowID,
		})
		return nil, &DispatchError{
			Code:    ErrCodeWorkerNotFound,
			Message: fmt.Sprintf("no worker found for group %s", group),
			Group:   group,
		}
	}

	// Check worker readiness (per design: NO fallback if not ready)
	if !workerState.Ready {
		d.log.Emit(ctx, "warn", "gateway.dispatch.worker_not_ready", map[string]any{
			"group":    group,
			"flowId":   flowID,
			"replicas": workerState.Replicas,
			"health":   workerState.Health.String(),
		})
		return nil, &DispatchError{
			Code:    ErrCodeWorkerNotReady,
			Message: fmt.Sprintf("worker for group %s is not ready", group),
			Group:   group,
		}
	}

	// Extract request context values
	requestID := ""
	traceID := ""
	if scope, ok := observ.ScopeFrom(ctx); ok {
		requestID = scope.RequestID
		traceID = scope.TraceID
	}
	// Prefer OTel trace ID if available
	if otelTraceID := observ.TraceIDFromContext(ctx); otelTraceID != "" {
		traceID = otelTraceID
	}

	// Build ExecuteRequest
	req := &worker.ExecuteRequest{
		FlowID:    flowID,
		RequestID: requestID,
		TraceID:   traceID,
		Input:     input,
	}

	// Apply request timeout
	timeout := d.config.RequestTimeout
	if timeout == 0 {
		timeout = defaultRequestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Mark last request time for the worker
	d.registry.MarkLastRequest(group)

	// Execute via client (with circuit breaker)
	d.log.Emit(ctx, "debug", "gateway.dispatch.sending", map[string]any{
		"group":     group,
		"flowId":    flowID,
		"requestId": requestID,
		"endpoint":  workerState.Endpoint,
	})

	resp, err := d.client.Execute(ctx, group, workerState.Endpoint, req)
	if err != nil {
		d.log.Emit(ctx, "error", "gateway.dispatch.failed", map[string]any{
			"group":     group,
			"flowId":    flowID,
			"requestId": requestID,
			"error":     err.Error(),
		})
		// Return both response (if any) and error so caller can inspect worker error details
		return resp, err
	}

	d.log.Emit(ctx, "debug", "gateway.dispatch.success", map[string]any{
		"group":     group,
		"flowId":    flowID,
		"requestId": requestID,
		"status":    resp.Status,
	})

	return resp, nil
}
