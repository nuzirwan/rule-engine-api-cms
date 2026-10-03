package observ

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Breaker-state gauge values (nzr_breaker_state).
const (
	BreakerClosed   = 0
	BreakerHalfOpen = 1
	BreakerOpen     = 2
)

// Metrics is the RED (Rate, Errors, Duration) metric set plus the breaker-state
// gauge, registered on an injected prometheus.Registerer (DIP — tests use an
// in-memory registry). Metric names/labels are defined once here; the call sites
// live in the httpapi RED middleware (flow metrics) and in connect (connection
// metrics + breaker gauge). Label cardinality is bounded by configured flows and
// connections, not by request path params (per distributed-tracing / cost-awareness).
type Metrics struct {
	flowRequests  *prometheus.CounterVec
	flowDuration  *prometheus.HistogramVec
	flowErrors    *prometheus.CounterVec
	connCalls     *prometheus.CounterVec
	connDuration  *prometheus.HistogramVec
	breakerState  *prometheus.GaugeVec
	inflight      prometheus.Gauge
	configReloads *prometheus.CounterVec
}

// latencyBuckets are tuned to the SLO targets (1ms…2s) so p95/p99 are
// meaningful rather than averaged away (per sli-slo-sla).
var latencyBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2}

// NewMetrics builds and registers the metric set on reg. A nil reg falls back to
// a fresh registry so the returned *Metrics is always usable (e.g. in a test
// that does not scrape). It panics only on a duplicate registration, which is a
// programmer error (double-wiring), surfaced early.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	factory := prometheus.WrapRegistererWith(nil, reg)
	m := &Metrics{
		flowRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nzr_flow_requests_total",
			Help: "Total flow requests (rate + errors) by flow, env, method and status.",
		}, []string{"flow_id", "env", "method", "status"}),
		flowDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "nzr_flow_request_duration_seconds",
			Help:    "Flow request duration in seconds by flow, env and method.",
			Buckets: latencyBuckets,
		}, []string{"flow_id", "env", "method"}),
		flowErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nzr_flow_errors_total",
			Help: "Flow errors broken out by error class.",
		}, []string{"flow_id", "env", "error_class"}),
		connCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nzr_connection_calls_total",
			Help: "Total connection calls (rate + errors) by connection key, type and status.",
		}, []string{"conn_key", "type", "status"}),
		connDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "nzr_connection_call_duration_seconds",
			Help:    "Connection call duration in seconds by connection key and type.",
			Buckets: latencyBuckets,
		}, []string{"conn_key", "type"}),
		breakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "nzr_breaker_state",
			Help: "Circuit breaker state per connection: 0=closed, 1=half-open, 2=open.",
		}, []string{"conn_key"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nzr_inflight_requests",
			Help: "In-flight flow requests (saturation signal; read by graceful drain).",
		}),
		configReloads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nzr_config_reload_total",
			Help: "Config hot-reload attempts by env and result.",
		}, []string{"env", "result"}),
	}
	factory.MustRegister(
		m.flowRequests, m.flowDuration, m.flowErrors,
		m.connCalls, m.connDuration, m.breakerState,
		m.inflight, m.configReloads,
	)
	return m
}

// ObserveFlow records one completed flow request: the duration histogram plus a
// rate/error counter labeled with the outcome status (ok|error).
func (m *Metrics) ObserveFlow(flowID, env, method, status string, d time.Duration) {
	m.flowRequests.WithLabelValues(flowID, env, method, status).Inc()
	m.flowDuration.WithLabelValues(flowID, env, method).Observe(d.Seconds())
}

// ObserveFlowError increments the per-class flow error counter so dashboards
// distinguish a Timeout storm from a Validation spike.
func (m *Metrics) ObserveFlowError(flowID, env string, class ErrorClass) {
	m.flowErrors.WithLabelValues(flowID, env, string(class)).Inc()
}

// ObserveConn records one connection call: duration plus a rate/error counter.
func (m *Metrics) ObserveConn(key, typ, status string, d time.Duration) {
	m.connCalls.WithLabelValues(key, typ, status).Inc()
	m.connDuration.WithLabelValues(key, typ).Observe(d.Seconds())
}

// SetBreakerState sets the breaker-state gauge for a connection (0/1/2). The
// Registry calls this on every breaker transition so the gauge reflects reality
// even with no traffic.
func (m *Metrics) SetBreakerState(key string, state int) {
	m.breakerState.WithLabelValues(key).Set(float64(state))
}

// IncInflight / DecInflight move the saturation gauge; the drain watches it to
// zero during graceful shutdown.
func (m *Metrics) IncInflight() { m.inflight.Inc() }
func (m *Metrics) DecInflight() { m.inflight.Dec() }

// ObserveConfigReload records a config hot-reload outcome ("ok"|"error") per env.
func (m *Metrics) ObserveConfigReload(env, result string) {
	m.configReloads.WithLabelValues(env, result).Inc()
}
