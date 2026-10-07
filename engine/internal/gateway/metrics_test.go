package gateway

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestNewGatewayMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	if m == nil {
		t.Fatal("NewGatewayMetrics returned nil")
	}

	// Verify all metric fields are initialized.
	if m.dispatchTotal == nil {
		t.Error("expected dispatchTotal to be initialized")
	}
	if m.dispatchDuration == nil {
		t.Error("expected dispatchDuration to be initialized")
	}
	if m.workerReady == nil {
		t.Error("expected workerReady to be initialized")
	}
	if m.workerReplicas == nil {
		t.Error("expected workerReplicas to be initialized")
	}
	if m.scaleOperations == nil {
		t.Error("expected scaleOperations to be initialized")
	}
	if m.requestsTotal == nil {
		t.Error("expected requestsTotal to be initialized")
	}
	if m.requestDuration == nil {
		t.Error("expected requestDuration to be initialized")
	}
	if m.workerHealth == nil {
		t.Error("expected workerHealth to be initialized")
	}
	if m.circuitBreakerState == nil {
		t.Error("expected circuitBreakerState to be initialized")
	}
	if m.coldStartDuration == nil {
		t.Error("expected coldStartDuration to be initialized")
	}
	if m.queueLength == nil {
		t.Error("expected queueLength to be initialized")
	}
	if m.queueTimeoutTotal == nil {
		t.Error("expected queueTimeoutTotal to be initialized")
	}

	// Verify metrics can be used (triggers registration).
	m.ObserveDispatch("test", "ok", time.Millisecond)
	m.ObserveRequest("test", "POST", "/execute", "200", time.Millisecond)
	m.SetWorkerHealth("test", "pod-1", true)
	m.SetCircuitBreakerState("test", CircuitStateClosed)
	m.ObserveColdStart("test", time.Second)
	m.SetWorkerReady("test", true)
	m.SetWorkerReplicas("test", 1)
	m.IncScaleOperation("test", "scale_up")
	m.SetQueueLength("test", 5)
	m.IncQueueTimeout("test")

	// Verify metrics are gathered after use.
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	expectedMetrics := map[string]bool{
		"gateway_dispatch_total":              false,
		"gateway_dispatch_duration_seconds":   false,
		"gateway_worker_ready":                false,
		"gateway_worker_replicas":             false,
		"gateway_scale_operations_total":      false,
		"gateway_requests_total":              false,
		"gateway_request_duration_seconds":    false,
		"gateway_worker_health":               false,
		"gateway_circuit_breaker_state":       false,
		"gateway_cold_start_duration_seconds": false,
		"gateway_queue_length":                false,
		"gateway_queue_timeout_total":         false,
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

func TestGatewayMetrics_ObserveDispatch(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	// Record a dispatch.
	m.ObserveDispatch("test-group", "ok", 100*time.Millisecond)

	// Verify counter incremented.
	count := testutil.ToFloat64(m.dispatchTotal.WithLabelValues("test-group", "ok"))
	if count != 1 {
		t.Errorf("expected dispatch count 1, got %f", count)
	}

	// Record another dispatch with error status.
	m.ObserveDispatch("test-group", "error", 200*time.Millisecond)

	errorCount := testutil.ToFloat64(m.dispatchTotal.WithLabelValues("test-group", "error"))
	if errorCount != 1 {
		t.Errorf("expected error count 1, got %f", errorCount)
	}
}

func TestGatewayMetrics_SetWorkerReady(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	// Set worker ready.
	m.SetWorkerReady("test-group", true)
	val := testutil.ToFloat64(m.workerReady.WithLabelValues("test-group"))
	if val != 1 {
		t.Errorf("expected ready=1, got %f", val)
	}

	// Set worker not ready.
	m.SetWorkerReady("test-group", false)
	val = testutil.ToFloat64(m.workerReady.WithLabelValues("test-group"))
	if val != 0 {
		t.Errorf("expected ready=0, got %f", val)
	}
}

func TestGatewayMetrics_SetWorkerReplicas(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	m.SetWorkerReplicas("test-group", 3)
	val := testutil.ToFloat64(m.workerReplicas.WithLabelValues("test-group"))
	if val != 3 {
		t.Errorf("expected replicas=3, got %f", val)
	}
}

func TestGatewayMetrics_IncScaleOperation(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	m.IncScaleOperation("test-group", "scale_up")
	m.IncScaleOperation("test-group", "scale_up")
	m.IncScaleOperation("test-group", "scale_down")

	upCount := testutil.ToFloat64(m.scaleOperations.WithLabelValues("test-group", "scale_up"))
	if upCount != 2 {
		t.Errorf("expected scale_up=2, got %f", upCount)
	}

	downCount := testutil.ToFloat64(m.scaleOperations.WithLabelValues("test-group", "scale_down"))
	if downCount != 1 {
		t.Errorf("expected scale_down=1, got %f", downCount)
	}
}

func TestGatewayMetrics_ObserveRequest(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	m.ObserveRequest("test-group", "POST", "/execute", "200", 50*time.Millisecond)
	m.ObserveRequest("test-group", "POST", "/execute", "500", 100*time.Millisecond)
	m.ObserveRequest("test-group", "GET", "/healthz", "200", 5*time.Millisecond)

	// Verify counters.
	okCount := testutil.ToFloat64(m.requestsTotal.WithLabelValues("test-group", "POST", "/execute", "200"))
	if okCount != 1 {
		t.Errorf("expected POST /execute 200 count=1, got %f", okCount)
	}

	errCount := testutil.ToFloat64(m.requestsTotal.WithLabelValues("test-group", "POST", "/execute", "500"))
	if errCount != 1 {
		t.Errorf("expected POST /execute 500 count=1, got %f", errCount)
	}

	healthCount := testutil.ToFloat64(m.requestsTotal.WithLabelValues("test-group", "GET", "/healthz", "200"))
	if healthCount != 1 {
		t.Errorf("expected GET /healthz 200 count=1, got %f", healthCount)
	}
}

func TestGatewayMetrics_SetWorkerHealth(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	// Set healthy.
	m.SetWorkerHealth("test-group", "pod-1", true)
	val := testutil.ToFloat64(m.workerHealth.WithLabelValues("test-group", "pod-1"))
	if val != 1 {
		t.Errorf("expected health=1, got %f", val)
	}

	// Set unhealthy.
	m.SetWorkerHealth("test-group", "pod-1", false)
	val = testutil.ToFloat64(m.workerHealth.WithLabelValues("test-group", "pod-1"))
	if val != 0 {
		t.Errorf("expected health=0, got %f", val)
	}

	// Multiple pods.
	m.SetWorkerHealth("test-group", "pod-2", true)
	val1 := testutil.ToFloat64(m.workerHealth.WithLabelValues("test-group", "pod-1"))
	val2 := testutil.ToFloat64(m.workerHealth.WithLabelValues("test-group", "pod-2"))
	if val1 != 0 || val2 != 1 {
		t.Errorf("expected pod-1=0 pod-2=1, got %f %f", val1, val2)
	}
}

func TestGatewayMetrics_SetCircuitBreakerState(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	tests := []struct {
		name  string
		state int
		want  float64
	}{
		{"closed", CircuitStateClosed, 0},
		{"half-open", CircuitStateHalfOpen, 1},
		{"open", CircuitStateOpen, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetCircuitBreakerState("test-group", tt.state)
			val := testutil.ToFloat64(m.circuitBreakerState.WithLabelValues("test-group"))
			if val != tt.want {
				t.Errorf("expected state=%f, got %f", tt.want, val)
			}
		})
	}
}

func TestGatewayMetrics_ObserveColdStart(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	// Record cold start durations.
	m.ObserveColdStart("test-group", 5*time.Second)
	m.ObserveColdStart("test-group", 10*time.Second)

	// Verify histogram has observations (checking sum is easiest).
	// Count should be 2.
	mfs, _ := reg.Gather()
	var found bool
	for _, mf := range mfs {
		if mf.GetName() == "gateway_cold_start_duration_seconds" {
			found = true
			for _, metric := range mf.GetMetric() {
				hist := metric.GetHistogram()
				if hist.GetSampleCount() != 2 {
					t.Errorf("expected 2 samples, got %d", hist.GetSampleCount())
				}
				// Sum should be 15 (5+10).
				if hist.GetSampleSum() != 15 {
					t.Errorf("expected sum=15, got %f", hist.GetSampleSum())
				}
			}
		}
	}
	if !found {
		t.Error("cold_start_duration_seconds metric not found")
	}
}

func TestGatewayMetrics_NilRegisterer(t *testing.T) {
	// Should not panic with nil registerer.
	m := NewGatewayMetrics(nil)
	if m == nil {
		t.Fatal("NewGatewayMetrics returned nil")
	}

	// Verify metrics work.
	m.ObserveDispatch("test", "ok", 10*time.Millisecond)
	m.SetWorkerReady("test", true)
	m.SetWorkerReplicas("test", 2)
	m.IncScaleOperation("test", "scale_up")
	m.ObserveRequest("test", "POST", "/execute", "200", 10*time.Millisecond)
	m.SetWorkerHealth("test", "pod-1", true)
	m.SetCircuitBreakerState("test", CircuitStateClosed)
	m.ObserveColdStart("test", time.Second)
	m.SetQueueLength("test", 5)
	m.IncQueueTimeout("test")
}

func TestGatewayMetrics_SetQueueLength(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	// Set queue length.
	m.SetQueueLength("test-group", 5)
	val := testutil.ToFloat64(m.queueLength.WithLabelValues("test-group"))
	if val != 5 {
		t.Errorf("expected queueLength=5, got %f", val)
	}

	// Update queue length.
	m.SetQueueLength("test-group", 10)
	val = testutil.ToFloat64(m.queueLength.WithLabelValues("test-group"))
	if val != 10 {
		t.Errorf("expected queueLength=10, got %f", val)
	}

	// Queue drains to zero.
	m.SetQueueLength("test-group", 0)
	val = testutil.ToFloat64(m.queueLength.WithLabelValues("test-group"))
	if val != 0 {
		t.Errorf("expected queueLength=0, got %f", val)
	}

	// Multiple groups.
	m.SetQueueLength("group-a", 3)
	m.SetQueueLength("group-b", 7)
	valA := testutil.ToFloat64(m.queueLength.WithLabelValues("group-a"))
	valB := testutil.ToFloat64(m.queueLength.WithLabelValues("group-b"))
	if valA != 3 || valB != 7 {
		t.Errorf("expected group-a=3 group-b=7, got %f %f", valA, valB)
	}
}

func TestGatewayMetrics_IncQueueTimeout(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	// Increment timeout counter.
	m.IncQueueTimeout("test-group")
	m.IncQueueTimeout("test-group")
	m.IncQueueTimeout("test-group")

	val := testutil.ToFloat64(m.queueTimeoutTotal.WithLabelValues("test-group"))
	if val != 3 {
		t.Errorf("expected queueTimeoutTotal=3, got %f", val)
	}

	// Multiple groups.
	m.IncQueueTimeout("group-a")
	m.IncQueueTimeout("group-b")
	m.IncQueueTimeout("group-b")

	valA := testutil.ToFloat64(m.queueTimeoutTotal.WithLabelValues("group-a"))
	valB := testutil.ToFloat64(m.queueTimeoutTotal.WithLabelValues("group-b"))
	if valA != 1 || valB != 2 {
		t.Errorf("expected group-a=1 group-b=2, got %f %f", valA, valB)
	}
}

// getCounterValue extracts the counter value for the given labels.
func getCounterValue(t *testing.T, counter *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := counter.WithLabelValues(labels...).Write(m); err != nil {
		t.Fatalf("failed to read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

// getGaugeValue extracts the gauge value for the given labels.
func getGaugeValue(t *testing.T, gauge *prometheus.GaugeVec, labels ...string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := gauge.WithLabelValues(labels...).Write(m); err != nil {
		t.Fatalf("failed to read gauge: %v", err)
	}
	return m.GetGauge().GetValue()
}
