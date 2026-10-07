package worker

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// latencyBuckets for request duration histogram (tuned to flow execution SLO).
var latencyBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}

// WorkerMetrics holds worker-specific Prometheus metrics for observability.
// Each worker binary exposes these metrics at /metrics for scraping.
type WorkerMetrics struct {
	requestsTotal     *prometheus.CounterVec   // worker_requests_total{group,flow_id,status}
	requestDuration   *prometheus.HistogramVec // worker_request_duration_seconds{group,flow_id}
	configVersion     *prometheus.GaugeVec     // worker_config_version{group}
	flowsLoaded       *prometheus.GaugeVec     // worker_flows_loaded{group}
	jdmsLoaded        *prometheus.GaugeVec     // worker_jdms_loaded{group}
	connectionsActive *prometheus.GaugeVec     // worker_connections_active{group,connection}
	reloadTotal       *prometheus.CounterVec   // worker_reload_total{group,status}
}

// NewWorkerMetrics creates and registers worker metrics on the given registerer.
// A nil registerer uses a fresh registry (test-safe). Panics on duplicate
// registration (programmer error).
func NewWorkerMetrics(reg prometheus.Registerer) *WorkerMetrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	m := &WorkerMetrics{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "worker_requests_total",
			Help: "Total flow execution requests by group, flow ID, and status.",
		}, []string{"group", "flow_id", "status"}),

		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "worker_request_duration_seconds",
			Help:    "Flow execution duration in seconds by group and flow ID.",
			Buckets: latencyBuckets,
		}, []string{"group", "flow_id"}),

		configVersion: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "worker_config_version",
			Help: "Currently loaded configuration version by group.",
		}, []string{"group"}),

		flowsLoaded: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "worker_flows_loaded",
			Help: "Number of flows loaded by group.",
		}, []string{"group"}),

		jdmsLoaded: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "worker_jdms_loaded",
			Help: "Number of JDMs loaded by group.",
		}, []string{"group"}),

		connectionsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "worker_connections_active",
			Help: "Active connection count by group and connection key.",
		}, []string{"group", "connection"}),

		reloadTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "worker_reload_total",
			Help: "Total configuration reload attempts by group and status (success/failure).",
		}, []string{"group", "status"}),
	}

	reg.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.configVersion,
		m.flowsLoaded,
		m.jdmsLoaded,
		m.connectionsActive,
		m.reloadTotal,
	)

	return m
}

// ObserveRequest records a flow execution request with timing and status.
// status should be "ok" or "error".
func (m *WorkerMetrics) ObserveRequest(group, flowID, status string, duration time.Duration) {
	m.requestsTotal.WithLabelValues(group, flowID, status).Inc()
	m.requestDuration.WithLabelValues(group, flowID).Observe(duration.Seconds())
}

// SetConfigVersion sets the currently loaded configuration version gauge.
func (m *WorkerMetrics) SetConfigVersion(group string, version int) {
	m.configVersion.WithLabelValues(group).Set(float64(version))
}

// SetFlowsLoaded sets the number of flows loaded gauge.
func (m *WorkerMetrics) SetFlowsLoaded(group string, count int) {
	m.flowsLoaded.WithLabelValues(group).Set(float64(count))
}

// SetJDMsLoaded sets the number of JDMs loaded gauge.
func (m *WorkerMetrics) SetJDMsLoaded(group string, count int) {
	m.jdmsLoaded.WithLabelValues(group).Set(float64(count))
}

// SetConnectionsActive sets the active connection count for a specific connection.
func (m *WorkerMetrics) SetConnectionsActive(group, connection string, active int) {
	m.connectionsActive.WithLabelValues(group, connection).Set(float64(active))
}

// IncReload increments the reload counter with the given status.
// status should be "success" or "failure".
func (m *WorkerMetrics) IncReload(group, status string) {
	m.reloadTotal.WithLabelValues(group, status).Inc()
}
