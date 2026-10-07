package gateway

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// latencyBuckets for dispatch duration histogram (tuned to SLO targets).
var latencyBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}

// coldStartBuckets for cold start latency (longer timescale: container startup + config load).
var coldStartBuckets = []float64{0.5, 1, 2, 5, 10, 15, 20, 30, 45, 60, 90}

// CircuitBreakerState constants for gateway_circuit_breaker_state gauge.
const (
	CircuitStateClosed   = 0 // healthy, requests flowing
	CircuitStateHalfOpen = 1 // testing recovery
	CircuitStateOpen     = 2 // tripped, rejecting requests
)

// GatewayMetrics holds gateway-specific Prometheus metrics for KEDA scaling and
// observability. The gateway_dispatch_total counter is the primary metric KEDA
// uses to decide when to scale workers from 0.
type GatewayMetrics struct {
	// Existing Phase 4 metrics
	dispatchTotal    *prometheus.CounterVec   // gateway_dispatch_total{group,status}
	dispatchDuration *prometheus.HistogramVec // gateway_dispatch_duration_seconds{group}
	workerReady      *prometheus.GaugeVec     // gateway_worker_ready{group} 0/1
	workerReplicas   *prometheus.GaugeVec     // gateway_worker_replicas{group}
	scaleOperations  *prometheus.CounterVec   // gateway_scale_operations_total{group,action}

	// Phase 5 enhanced metrics
	requestsTotal       *prometheus.CounterVec   // gateway_requests_total{group,method,path,status}
	requestDuration     *prometheus.HistogramVec // gateway_request_duration_seconds{group}
	workerHealth        *prometheus.GaugeVec     // gateway_worker_health{group,worker_id}
	circuitBreakerState *prometheus.GaugeVec     // gateway_circuit_breaker_state{group}
	coldStartDuration   *prometheus.HistogramVec // gateway_cold_start_duration_seconds{group}
}

// NewGatewayMetrics creates and registers gateway metrics on the given registerer.
// A nil registerer uses a fresh registry (test-safe). Panics on duplicate
// registration (programmer error).
func NewGatewayMetrics(reg prometheus.Registerer) *GatewayMetrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	m := &GatewayMetrics{
		// Existing Phase 4 metrics
		dispatchTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_dispatch_total",
			Help: "Total gateway dispatch requests by group and status. KEDA queries this metric.",
		}, []string{"group", "status"}),

		dispatchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_dispatch_duration_seconds",
			Help:    "Gateway dispatch duration in seconds by group.",
			Buckets: latencyBuckets,
		}, []string{"group"}),

		workerReady: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_worker_ready",
			Help: "Worker readiness state by group (1=ready, 0=not ready).",
		}, []string{"group"}),

		workerReplicas: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_worker_replicas",
			Help: "Current worker replica count by group.",
		}, []string{"group"}),

		scaleOperations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_scale_operations_total",
			Help: "Total scaling operations by group and action (scale_up, scale_down, ensure_ready).",
		}, []string{"group", "action"}),

		// Phase 5 enhanced metrics
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_requests_total",
			Help: "Total HTTP requests to the gateway by group, method, path, and status code.",
		}, []string{"group", "method", "path", "status"}),

		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_request_duration_seconds",
			Help:    "HTTP request duration in seconds by group.",
			Buckets: latencyBuckets,
		}, []string{"group"}),

		workerHealth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_worker_health",
			Help: "Individual worker pod health status (1=healthy, 0=unhealthy).",
		}, []string{"group", "worker_id"}),

		circuitBreakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_circuit_breaker_state",
			Help: "Circuit breaker state by group (0=closed, 1=half-open, 2=open).",
		}, []string{"group"}),

		coldStartDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_cold_start_duration_seconds",
			Help:    "Cold start duration from scale-from-zero to first ready pod.",
			Buckets: coldStartBuckets,
		}, []string{"group"}),
	}

	reg.MustRegister(
		m.dispatchTotal,
		m.dispatchDuration,
		m.workerReady,
		m.workerReplicas,
		m.scaleOperations,
		m.requestsTotal,
		m.requestDuration,
		m.workerHealth,
		m.circuitBreakerState,
		m.coldStartDuration,
	)

	return m
}

// ObserveDispatch records a completed dispatch request: increments the counter
// and observes duration. status should be "ok" or "error".
func (m *GatewayMetrics) ObserveDispatch(group, status string, duration time.Duration) {
	m.dispatchTotal.WithLabelValues(group, status).Inc()
	m.dispatchDuration.WithLabelValues(group).Observe(duration.Seconds())
}

// SetWorkerReady sets the worker readiness gauge for a group.
func (m *GatewayMetrics) SetWorkerReady(group string, ready bool) {
	val := 0.0
	if ready {
		val = 1.0
	}
	m.workerReady.WithLabelValues(group).Set(val)
}

// SetWorkerReplicas sets the current replica count gauge for a group.
func (m *GatewayMetrics) SetWorkerReplicas(group string, replicas int) {
	m.workerReplicas.WithLabelValues(group).Set(float64(replicas))
}

// IncScaleOperation increments the scale operations counter for a group.
// action should be "scale_up", "scale_down", or "ensure_ready".
func (m *GatewayMetrics) IncScaleOperation(group, action string) {
	m.scaleOperations.WithLabelValues(group, action).Inc()
}

// ObserveRequest records an HTTP request with method, path, and status code.
func (m *GatewayMetrics) ObserveRequest(group, method, path, status string, duration time.Duration) {
	m.requestsTotal.WithLabelValues(group, method, path, status).Inc()
	m.requestDuration.WithLabelValues(group).Observe(duration.Seconds())
}

// SetWorkerHealth sets the health status of an individual worker pod.
func (m *GatewayMetrics) SetWorkerHealth(group, workerID string, healthy bool) {
	val := 0.0
	if healthy {
		val = 1.0
	}
	m.workerHealth.WithLabelValues(group, workerID).Set(val)
}

// SetCircuitBreakerState sets the circuit breaker state gauge for a group.
// state should be CircuitStateClosed (0), CircuitStateHalfOpen (1), or CircuitStateOpen (2).
func (m *GatewayMetrics) SetCircuitBreakerState(group string, state int) {
	m.circuitBreakerState.WithLabelValues(group).Set(float64(state))
}

// ObserveColdStart records the duration of a cold start (scale-from-zero).
func (m *GatewayMetrics) ObserveColdStart(group string, duration time.Duration) {
	m.coldStartDuration.WithLabelValues(group).Observe(duration.Seconds())
}
