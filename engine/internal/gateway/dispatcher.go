package gateway

import (
	"context"
	"fmt"
	"time"

	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"
)

// tracerKey is a context key for passing the tracer to the dispatcher.
type tracerKey struct{}

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
// through the WorkerClient with circuit breaker protection. When a Scaler is
// configured, it can dynamically scale workers from 0 before dispatch.
type Dispatcher struct {
	registry *WorkerRegistry
	client   *WorkerClient
	config   DispatchConfig
	log      observ.Logger
	tracer   observ.Tracer
	scaler   *Scaler
	metrics  *GatewayMetrics
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

// NewDispatcherWithScaler creates a Dispatcher with scaler and metrics support
// for dynamic scaling with KEDA.
func NewDispatcherWithScaler(registry *WorkerRegistry, client *WorkerClient, config DispatchConfig, log observ.Logger, scaler *Scaler, metrics *GatewayMetrics) *Dispatcher {
	return &Dispatcher{
		registry: registry,
		client:   client,
		config:   config,
		log:      log,
		scaler:   scaler,
		metrics:  metrics,
	}
}

// NewDispatcherWithTracing creates a Dispatcher with full observability support
// including tracing, metrics, and scaler integration.
func NewDispatcherWithTracing(registry *WorkerRegistry, client *WorkerClient, config DispatchConfig, log observ.Logger, tracer observ.Tracer, scaler *Scaler, metrics *GatewayMetrics) *Dispatcher {
	return &Dispatcher{
		registry: registry,
		client:   client,
		config:   config,
		log:      log,
		tracer:   tracer,
		scaler:   scaler,
		metrics:  metrics,
	}
}

// SetTracer sets the tracer for distributed tracing support.
func (d *Dispatcher) SetTracer(t observ.Tracer) {
	d.tracer = t
}

// Dispatch routes a flow execution request to the appropriate worker for the group.
// It returns an error if the worker is not found or not ready (NO fallback per design).
// The request is forwarded to the worker's /execute endpoint with trace headers propagated.
// When a Scaler is configured and the worker is not ready, it attempts to scale up first.
func (d *Dispatcher) Dispatch(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error) {
	start := time.Now()

	// Start a dispatch span for tracing.
	ctx, span := StartDispatchSpan(ctx, d.tracer, group, flowID)
	defer func() {
		span.End(nil)
	}()

	// Build tracing fields for log lines.
	traceFields := TracingFields(ctx, group)

	// Lookup worker via registry
	workerState, ok := d.registry.GetWorker(group)

	// If not found or not ready, try to ensure via scaler (dynamic mode)
	if (!ok || !workerState.Ready) && d.scaler != nil {
		d.log.Emit(ctx, "debug", "gateway.dispatch.scaling", MergeFields(traceFields, map[string]any{
			"flowId": flowID,
			"exists": ok,
			"ready":  ok && workerState.Ready,
		}))

		if err := d.scaler.EnsureReady(ctx, group); err != nil {
			span.Set("error", err.Error())
			d.recordMetrics(group, "error", time.Since(start))
			return nil, &DispatchError{
				Code:    ErrCodeWorkerNotReady,
				Message: fmt.Sprintf("failed to ensure worker ready for group %s: %v", group, err),
				Group:   group,
			}
		}
		// Re-fetch worker state after scaling.
		workerState, ok = d.registry.GetWorker(group)
	}

	if !ok {
		d.log.Emit(ctx, "warn", "gateway.dispatch.worker_not_found", MergeFields(traceFields, map[string]any{
			"flowId": flowID,
		}))
		span.Set("error", "worker_not_found")
		d.recordMetrics(group, "error", time.Since(start))
		return nil, &DispatchError{
			Code:    ErrCodeWorkerNotFound,
			Message: fmt.Sprintf("no worker found for group %s", group),
			Group:   group,
		}
	}

	// Check worker readiness (per design: NO fallback if not ready)
	if !workerState.Ready {
		d.log.Emit(ctx, "warn", "gateway.dispatch.worker_not_ready", MergeFields(traceFields, map[string]any{
			"flowId":   flowID,
			"replicas": workerState.Replicas,
			"health":   workerState.Health.String(),
		}))
		span.Set("error", "worker_not_ready")
		d.recordMetrics(group, "error", time.Since(start))
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
	d.log.Emit(ctx, "debug", "gateway.dispatch.sending", MergeFields(traceFields, map[string]any{
		"flowId":    flowID,
		"requestId": requestID,
		"endpoint":  workerState.Endpoint,
	}))

	span.Set("endpoint", workerState.Endpoint)
	span.Set("request_id", requestID)

	resp, err := d.client.Execute(ctx, group, workerState.Endpoint, req)
	if err != nil {
		d.log.Emit(ctx, "error", "gateway.dispatch.failed", MergeFields(traceFields, map[string]any{
			"flowId":    flowID,
			"requestId": requestID,
			"error":     err.Error(),
		}))
		span.Set("error", err.Error())
		d.recordMetrics(group, "error", time.Since(start))
		// Return both response (if any) and error so caller can inspect worker error details
		return resp, err
	}

	d.log.Emit(ctx, "debug", "gateway.dispatch.success", MergeFields(traceFields, map[string]any{
		"flowId":    flowID,
		"requestId": requestID,
		"status":    resp.Status,
	}))

	span.Set("status", resp.Status)
	d.recordMetrics(group, "ok", time.Since(start))
	return resp, nil
}

// recordMetrics records dispatch metrics if metrics are configured.
func (d *Dispatcher) recordMetrics(group, status string, duration time.Duration) {
	if d.metrics != nil {
		d.metrics.ObserveDispatch(group, status, duration)
	}
}
