package gateway

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// latencyBuckets for dispatch duration histogram (tuned to SLO targets).
var latencyBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}

// GatewayMetrics holds gateway-specific Prometheus metrics for KEDA scaling and
// observability. The gateway_dispatch_total counter is the primary metric KEDA
// uses to decide when to scale workers from 0.
type GatewayMetrics struct {
	dispatchTotal    *prometheus.CounterVec   // gateway_dispatch_total{group,status}
	dispatchDuration *prometheus.HistogramVec // gateway_dispatch_duration_seconds{group}
	workerReady      *prometheus.GaugeVec     // gateway_worker_ready{group} 0/1
	workerReplicas   *prometheus.GaugeVec     // gateway_worker_replicas{group}
	scaleOperations  *prometheus.CounterVec   // gateway_scale_operations_total{group,action}
}

// NewGatewayMetrics creates and registers gateway metrics on the given registerer.
// A nil registerer uses a fresh registry (test-safe). Panics on duplicate
// registration (programmer error).
func NewGatewayMetrics(reg prometheus.Registerer) *GatewayMetrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	m := &GatewayMetrics{
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
	}

	reg.MustRegister(
		m.dispatchTotal,
		m.dispatchDuration,
		m.workerReady,
		m.workerReplicas,
		m.scaleOperations,
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
