package gateway

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestNewGatewayMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)
	if m == nil {
		t.Fatal("expected non-nil GatewayMetrics")
	}
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
}

func TestNewGatewayMetrics_NilRegistry(t *testing.T) {
	// Should not panic with nil registry.
	m := NewGatewayMetrics(nil)
	if m == nil {
		t.Fatal("expected non-nil GatewayMetrics even with nil registry")
	}
}

func TestGatewayMetrics_ObserveDispatch(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	m.ObserveDispatch("orders", "ok", 100*time.Millisecond)
	m.ObserveDispatch("orders", "ok", 200*time.Millisecond)
	m.ObserveDispatch("orders", "error", 50*time.Millisecond)
	m.ObserveDispatch("payments", "ok", 150*time.Millisecond)

	// Verify counter values.
	ordersOk := getCounterValue(t, m.dispatchTotal, "orders", "ok")
	if ordersOk != 2 {
		t.Errorf("expected orders/ok counter = 2, got %v", ordersOk)
	}

	ordersError := getCounterValue(t, m.dispatchTotal, "orders", "error")
	if ordersError != 1 {
		t.Errorf("expected orders/error counter = 1, got %v", ordersError)
	}

	paymentsOk := getCounterValue(t, m.dispatchTotal, "payments", "ok")
	if paymentsOk != 1 {
		t.Errorf("expected payments/ok counter = 1, got %v", paymentsOk)
	}
}

func TestGatewayMetrics_SetWorkerReady(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	m.SetWorkerReady("orders", true)
	m.SetWorkerReady("payments", false)

	ordersReady := getGaugeValue(t, m.workerReady, "orders")
	if ordersReady != 1 {
		t.Errorf("expected orders ready gauge = 1, got %v", ordersReady)
	}

	paymentsReady := getGaugeValue(t, m.workerReady, "payments")
	if paymentsReady != 0 {
		t.Errorf("expected payments ready gauge = 0, got %v", paymentsReady)
	}
}

func TestGatewayMetrics_SetWorkerReplicas(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	m.SetWorkerReplicas("orders", 3)
	m.SetWorkerReplicas("payments", 0)

	ordersReplicas := getGaugeValue(t, m.workerReplicas, "orders")
	if ordersReplicas != 3 {
		t.Errorf("expected orders replicas gauge = 3, got %v", ordersReplicas)
	}

	paymentsReplicas := getGaugeValue(t, m.workerReplicas, "payments")
	if paymentsReplicas != 0 {
		t.Errorf("expected payments replicas gauge = 0, got %v", paymentsReplicas)
	}
}

func TestGatewayMetrics_IncScaleOperation(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewGatewayMetrics(reg)

	m.IncScaleOperation("orders", "scale_up")
	m.IncScaleOperation("orders", "scale_up")
	m.IncScaleOperation("orders", "scale_down")
	m.IncScaleOperation("payments", "ensure_ready")

	ordersScaleUp := getCounterValue(t, m.scaleOperations, "orders", "scale_up")
	if ordersScaleUp != 2 {
		t.Errorf("expected orders/scale_up counter = 2, got %v", ordersScaleUp)
	}

	ordersScaleDown := getCounterValue(t, m.scaleOperations, "orders", "scale_down")
	if ordersScaleDown != 1 {
		t.Errorf("expected orders/scale_down counter = 1, got %v", ordersScaleDown)
	}

	paymentsEnsure := getCounterValue(t, m.scaleOperations, "payments", "ensure_ready")
	if paymentsEnsure != 1 {
		t.Errorf("expected payments/ensure_ready counter = 1, got %v", paymentsEnsure)
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
