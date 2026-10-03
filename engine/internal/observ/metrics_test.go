package observ

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestFlowMetrics proves the RED flow metrics observe and the error counter
// breaks out by class.
func TestFlowMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.ObserveFlow("orders", "dev", "GET", "ok", 12*time.Millisecond)
	m.ObserveFlow("orders", "dev", "GET", "error", 5*time.Millisecond)
	m.ObserveFlowError("orders", "dev", ClassTimeout)

	if got := testutil.ToFloat64(m.flowRequests.WithLabelValues("orders", "dev", "GET", "ok")); got != 1 {
		t.Fatalf("flow requests ok = %v; want 1", got)
	}
	if got := testutil.ToFloat64(m.flowErrors.WithLabelValues("orders", "dev", string(ClassTimeout))); got != 1 {
		t.Fatalf("flow errors Timeout = %v; want 1", got)
	}
	if n := testutil.CollectAndCount(m.flowDuration); n == 0 {
		t.Fatalf("flow duration histogram not registered/observed")
	}
}

// TestBreakerGauge proves the breaker-state gauge reflects an injected
// transition (closed -> open).
func TestBreakerGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.SetBreakerState("pg", BreakerClosed)
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("pg")); got != 0 {
		t.Fatalf("breaker state = %v; want 0 (closed)", got)
	}
	m.SetBreakerState("pg", BreakerOpen)
	if got := testutil.ToFloat64(m.breakerState.WithLabelValues("pg")); got != 2 {
		t.Fatalf("breaker state = %v; want 2 (open)", got)
	}
}

// TestConnMetricsAndInflight proves the connection RED metrics and the inflight
// saturation gauge move.
func TestConnMetricsAndInflight(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)

	m.ObserveConn("pg", "postgres", "ok", 3*time.Millisecond)
	if got := testutil.ToFloat64(m.connCalls.WithLabelValues("pg", "postgres", "ok")); got != 1 {
		t.Fatalf("conn calls = %v; want 1", got)
	}
	m.IncInflight()
	m.IncInflight()
	m.DecInflight()
	if got := testutil.ToFloat64(m.inflight); got != 1 {
		t.Fatalf("inflight = %v; want 1", got)
	}
}
