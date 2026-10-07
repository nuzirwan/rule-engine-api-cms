package worker

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewWorkerMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	if m == nil {
		t.Fatal("NewWorkerMetrics returned nil")
	}

	// Verify all metric fields are initialized.
	if m.requestsTotal == nil {
		t.Error("expected requestsTotal to be initialized")
	}
	if m.requestDuration == nil {
		t.Error("expected requestDuration to be initialized")
	}
	if m.configVersion == nil {
		t.Error("expected configVersion to be initialized")
	}
	if m.flowsLoaded == nil {
		t.Error("expected flowsLoaded to be initialized")
	}
	if m.jdmsLoaded == nil {
		t.Error("expected jdmsLoaded to be initialized")
	}
	if m.connectionsActive == nil {
		t.Error("expected connectionsActive to be initialized")
	}
	if m.reloadTotal == nil {
		t.Error("expected reloadTotal to be initialized")
	}

	// Trigger metrics registration by using them.
	m.ObserveRequest("test", "flow-1", "ok", time.Millisecond)
	m.SetConfigVersion("test", 1)
	m.SetFlowsLoaded("test", 5)
	m.SetJDMsLoaded("test", 3)
	m.SetConnectionsActive("test", "pg", 1)
	m.IncReload("test", "success")

	// Verify metrics are gathered after use.
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	expectedMetrics := map[string]bool{
		"worker_requests_total":           false,
		"worker_request_duration_seconds": false,
		"worker_config_version":           false,
		"worker_flows_loaded":             false,
		"worker_jdms_loaded":              false,
		"worker_connections_active":       false,
		"worker_reload_total":             false,
	}

	for _, mf := range mfs {
		if _, ok := expectedMetrics[mf.GetName()]; ok {
			expectedMetrics[mf.GetName()] = true
		}
	}

	for name, found := range expectedMetrics {
		if !found {
			t.Errorf("expected metric %q not found in registry", name)
		}
	}
}

func TestWorkerMetrics_ObserveRequest(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	// Record a successful request.
	m.ObserveRequest("group-a", "flow-1", "ok", 100*time.Millisecond)

	// Verify counter incremented.
	count := testutil.ToFloat64(m.requestsTotal.WithLabelValues("group-a", "flow-1", "ok"))
	if count != 1 {
		t.Errorf("expected request count 1, got %f", count)
	}

	// Record a failed request.
	m.ObserveRequest("group-a", "flow-1", "error", 200*time.Millisecond)

	errorCount := testutil.ToFloat64(m.requestsTotal.WithLabelValues("group-a", "flow-1", "error"))
	if errorCount != 1 {
		t.Errorf("expected error count 1, got %f", errorCount)
	}

	// Different flow.
	m.ObserveRequest("group-a", "flow-2", "ok", 50*time.Millisecond)
	flow2Count := testutil.ToFloat64(m.requestsTotal.WithLabelValues("group-a", "flow-2", "ok"))
	if flow2Count != 1 {
		t.Errorf("expected flow-2 count 1, got %f", flow2Count)
	}
}

func TestWorkerMetrics_SetConfigVersion(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	m.SetConfigVersion("group-a", 5)
	val := testutil.ToFloat64(m.configVersion.WithLabelValues("group-a"))
	if val != 5 {
		t.Errorf("expected version=5, got %f", val)
	}

	// Update version.
	m.SetConfigVersion("group-a", 10)
	val = testutil.ToFloat64(m.configVersion.WithLabelValues("group-a"))
	if val != 10 {
		t.Errorf("expected version=10, got %f", val)
	}
}

func TestWorkerMetrics_SetFlowsLoaded(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	m.SetFlowsLoaded("group-a", 15)
	val := testutil.ToFloat64(m.flowsLoaded.WithLabelValues("group-a"))
	if val != 15 {
		t.Errorf("expected flows=15, got %f", val)
	}
}

func TestWorkerMetrics_SetJDMsLoaded(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	m.SetJDMsLoaded("group-a", 8)
	val := testutil.ToFloat64(m.jdmsLoaded.WithLabelValues("group-a"))
	if val != 8 {
		t.Errorf("expected jdms=8, got %f", val)
	}
}

func TestWorkerMetrics_SetConnectionsActive(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	// Set active connections.
	m.SetConnectionsActive("group-a", "postgres", 5)
	m.SetConnectionsActive("group-a", "redis", 3)

	pgVal := testutil.ToFloat64(m.connectionsActive.WithLabelValues("group-a", "postgres"))
	if pgVal != 5 {
		t.Errorf("expected postgres=5, got %f", pgVal)
	}

	redisVal := testutil.ToFloat64(m.connectionsActive.WithLabelValues("group-a", "redis"))
	if redisVal != 3 {
		t.Errorf("expected redis=3, got %f", redisVal)
	}

	// Update value.
	m.SetConnectionsActive("group-a", "postgres", 10)
	pgVal = testutil.ToFloat64(m.connectionsActive.WithLabelValues("group-a", "postgres"))
	if pgVal != 10 {
		t.Errorf("expected postgres=10, got %f", pgVal)
	}
}

func TestWorkerMetrics_IncReload(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	// Record successful reloads.
	m.IncReload("group-a", "success")
	m.IncReload("group-a", "success")
	m.IncReload("group-a", "failure")

	successCount := testutil.ToFloat64(m.reloadTotal.WithLabelValues("group-a", "success"))
	if successCount != 2 {
		t.Errorf("expected success=2, got %f", successCount)
	}

	failureCount := testutil.ToFloat64(m.reloadTotal.WithLabelValues("group-a", "failure"))
	if failureCount != 1 {
		t.Errorf("expected failure=1, got %f", failureCount)
	}
}

func TestWorkerMetrics_MultipleGroups(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	// Set metrics for multiple groups.
	m.SetConfigVersion("group-a", 1)
	m.SetConfigVersion("group-b", 2)
	m.SetFlowsLoaded("group-a", 5)
	m.SetFlowsLoaded("group-b", 10)

	// Verify isolation.
	if v := testutil.ToFloat64(m.configVersion.WithLabelValues("group-a")); v != 1 {
		t.Errorf("expected group-a version=1, got %f", v)
	}
	if v := testutil.ToFloat64(m.configVersion.WithLabelValues("group-b")); v != 2 {
		t.Errorf("expected group-b version=2, got %f", v)
	}
	if v := testutil.ToFloat64(m.flowsLoaded.WithLabelValues("group-a")); v != 5 {
		t.Errorf("expected group-a flows=5, got %f", v)
	}
	if v := testutil.ToFloat64(m.flowsLoaded.WithLabelValues("group-b")); v != 10 {
		t.Errorf("expected group-b flows=10, got %f", v)
	}
}

func TestWorkerMetrics_NilRegisterer(t *testing.T) {
	// Should not panic with nil registerer.
	m := NewWorkerMetrics(nil)
	if m == nil {
		t.Fatal("NewWorkerMetrics returned nil")
	}

	// Verify metrics work.
	m.ObserveRequest("test", "flow-1", "ok", 10*time.Millisecond)
	m.SetConfigVersion("test", 1)
	m.SetFlowsLoaded("test", 5)
	m.SetJDMsLoaded("test", 3)
	m.SetConnectionsActive("test", "pg", 2)
	m.IncReload("test", "success")
}

func TestWorkerMetrics_DurationHistogram(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewWorkerMetrics(reg)

	// Record requests with different durations.
	m.ObserveRequest("group-a", "flow-1", "ok", 5*time.Millisecond)
	m.ObserveRequest("group-a", "flow-1", "ok", 50*time.Millisecond)
	m.ObserveRequest("group-a", "flow-1", "ok", 500*time.Millisecond)

	// Verify histogram has 3 observations.
	mfs, _ := reg.Gather()
	for _, mf := range mfs {
		if mf.GetName() == "worker_request_duration_seconds" {
			for _, metric := range mf.GetMetric() {
				hist := metric.GetHistogram()
				if hist.GetSampleCount() != 3 {
					t.Errorf("expected 3 samples, got %d", hist.GetSampleCount())
				}
			}
		}
	}
}
