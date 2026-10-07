package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"
)

func TestDispatcher_WorkerNotFound(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	dispatcher := NewDispatcher(registry, client, DispatchConfig{}, &mockLogger{})

	_, err := dispatcher.Dispatch(context.Background(), "flow-1", "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent worker")
	}

	dispErr, ok := err.(*DispatchError)
	if !ok {
		t.Fatalf("expected *DispatchError, got %T", err)
	}
	if dispErr.Code != ErrCodeWorkerNotFound {
		t.Errorf("expected Code=%s, got %s", ErrCodeWorkerNotFound, dispErr.Code)
	}
	if dispErr.Group != "nonexistent" {
		t.Errorf("expected Group=nonexistent, got %s", dispErr.Group)
	}
}

func TestDispatcher_WorkerNotReady(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	dispatcher := NewDispatcher(registry, client, DispatchConfig{}, &mockLogger{})

	// Add worker that is not ready
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: "http://worker-default.flow-workers.svc:8080",
		Ready:    false, // Not ready
		Replicas: 0,
		Health:   HealthUnhealthy,
	}
	registry.mu.Unlock()

	_, err := dispatcher.Dispatch(context.Background(), "flow-1", "default", nil)
	if err == nil {
		t.Fatal("expected error for not-ready worker")
	}

	dispErr, ok := err.(*DispatchError)
	if !ok {
		t.Fatalf("expected *DispatchError, got %T", err)
	}
	if dispErr.Code != ErrCodeWorkerNotReady {
		t.Errorf("expected Code=%s, got %s", ErrCodeWorkerNotReady, dispErr.Code)
	}
	if dispErr.Group != "default" {
		t.Errorf("expected Group=default, got %s", dispErr.Group)
	}
}

func TestDispatcher_Success(t *testing.T) {
	// Set up mock worker server
	expectedResp := &worker.ExecuteResponse{
		Status: 200,
		Response: map[string]any{
			"output": "processed",
		},
	}
	var receivedReq worker.ExecuteRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewDecoder(r.Body).Decode(&receivedReq)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expectedResp)
	}))
	defer server.Close()

	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	dispatcher := NewDispatcher(registry, client, DispatchConfig{
		RequestTimeout: 5 * time.Second,
	}, &mockLogger{})

	// Add ready worker pointing to test server
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	// Create context with scope
	ctx := observ.WithScope(context.Background(), observ.RequestScope{
		RequestID: "req-abc",
		TraceID:   "trace-xyz",
	})

	input := map[string]any{"key": "value"}
	resp, err := dispatcher.Dispatch(ctx, "flow-1", "default", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify response
	if resp.Status != 200 {
		t.Errorf("expected status 200, got %d", resp.Status)
	}
	if resp.Response["output"] != "processed" {
		t.Errorf("unexpected response: %+v", resp.Response)
	}

	// Verify request was built correctly
	if receivedReq.FlowID != "flow-1" {
		t.Errorf("expected FlowID=flow-1, got %s", receivedReq.FlowID)
	}
	if receivedReq.RequestID != "req-abc" {
		t.Errorf("expected RequestID=req-abc, got %s", receivedReq.RequestID)
	}
	if receivedReq.TraceID != "trace-xyz" {
		t.Errorf("expected TraceID=trace-xyz, got %s", receivedReq.TraceID)
	}
	if receivedReq.Input["key"] != "value" {
		t.Errorf("expected Input.key=value, got %v", receivedReq.Input["key"])
	}
}

func TestDispatcher_Timeout(t *testing.T) {
	// Server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		resp := &worker.ExecuteResponse{Status: 200}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	dispatcher := NewDispatcher(registry, client, DispatchConfig{
		RequestTimeout: 50 * time.Millisecond, // Very short timeout
	}, &mockLogger{})

	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	_, err := dispatcher.Dispatch(context.Background(), "flow-1", "default", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	dispErr, ok := err.(*DispatchError)
	if !ok {
		t.Fatalf("expected *DispatchError, got %T", err)
	}
	if dispErr.Code != ErrCodeTimeout {
		t.Errorf("expected Code=%s, got %s", ErrCodeTimeout, dispErr.Code)
	}
}

func TestDispatcher_MarksLastRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := &worker.ExecuteResponse{Status: 200}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	dispatcher := NewDispatcher(registry, client, DispatchConfig{}, &mockLogger{})

	// Add worker with old LastRequest
	oldTime := time.Now().Add(-time.Hour)
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:       "default",
		Endpoint:    server.URL,
		Ready:       true,
		Replicas:    1,
		Health:      HealthHealthy,
		LastRequest: oldTime,
	}
	registry.mu.Unlock()

	before := time.Now()
	_, err := dispatcher.Dispatch(context.Background(), "flow-1", "default", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := time.Now()

	// Check LastRequest was updated
	state, _ := registry.GetWorker("default")
	if state.LastRequest.Before(before) || state.LastRequest.After(after) {
		t.Errorf("expected LastRequest to be updated to ~now, got %v", state.LastRequest)
	}
}

func TestDispatcher_PropagatesWorkerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := &worker.ExecuteResponse{
			Status: 404,
			Error: &worker.ErrorDetail{
				Code:    "FLOW_NOT_FOUND",
				Message: "flow xyz does not exist",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	dispatcher := NewDispatcher(registry, client, DispatchConfig{}, &mockLogger{})

	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	resp, err := dispatcher.Dispatch(context.Background(), "xyz", "default", nil)
	if err == nil {
		t.Fatal("expected error for flow not found")
	}

	// Response should still be returned
	if resp == nil {
		t.Fatal("expected response even with error")
	}

	dispErr, ok := err.(*DispatchError)
	if !ok {
		t.Fatalf("expected *DispatchError, got %T", err)
	}
	if dispErr.Code != "FLOW_NOT_FOUND" {
		t.Errorf("expected Code=FLOW_NOT_FOUND, got %s", dispErr.Code)
	}
}

func TestDispatcher_DefaultTimeout(t *testing.T) {
	// Test that default timeout is applied when not configured
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := &worker.ExecuteResponse{Status: 200}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	dispatcher := NewDispatcher(registry, client, DispatchConfig{}, &mockLogger{}) // No timeout config

	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	// This should work with default timeout
	resp, err := dispatcher.Dispatch(context.Background(), "flow-1", "default", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("expected status 200, got %d", resp.Status)
	}
}

func TestDispatcher_ContextWithRequestScope(t *testing.T) {
	var receivedReq worker.ExecuteRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedReq)
		resp := &worker.ExecuteResponse{Status: 200}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	dispatcher := NewDispatcher(registry, client, DispatchConfig{}, &mockLogger{})

	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	// Test without scope
	_, err := dispatcher.Dispatch(context.Background(), "flow-1", "default", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// RequestID and TraceID should be empty when no scope
	if receivedReq.RequestID != "" {
		t.Errorf("expected empty RequestID without scope, got %s", receivedReq.RequestID)
	}
	if receivedReq.TraceID != "" {
		t.Errorf("expected empty TraceID without scope, got %s", receivedReq.TraceID)
	}
}

func TestDispatcher_WithScaler_ScalesUp(t *testing.T) {
	// Set up mock worker server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := &worker.ExecuteResponse{Status: 200}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create deployment with 0 replicas (scaled to 0).
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-orders",
			Namespace: "flow-workers",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(0),
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      0,
			ReadyReplicas: 0,
		},
	}
	k8sClient := fake.NewSimpleClientset(deployment)

	// Make GET return ready after first call (simulates scaling).
	callCount := 0
	k8sClient.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		callCount++
		if callCount > 1 {
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-orders",
					Namespace: "flow-workers",
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(1),
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 1,
				},
			}
			return true, dep, nil
		}
		return false, nil, nil
	})

	store := &mockGroupReader{
		groups: map[string]config.Group{
			"orders": {
				ID:   "orders",
				Name: "Orders",
				Scaling: config.ScalingConfig{
					Mode: config.ScalingModeDynamic,
				},
			},
		},
	}

	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	scaler := NewScaler(ScalerConfig{
		K8sClient:      k8sClient,
		Namespace:      "flow-workers",
		Store:          store,
		Log:            &mockLogger{},
		Metrics:        metrics,
		StartupTimeout: 2 * time.Second,
	})

	dispatcher := NewDispatcherWithScaler(registry, client, DispatchConfig{}, &mockLogger{}, scaler, metrics)

	// Worker starts not in registry (not yet discovered).
	// After scaler.EnsureReady, we need to add it to registry (simulating discovery).
	// For this test, we pre-add the worker as ready after scaling would complete.
	registry.mu.Lock()
	registry.workers["orders"] = &WorkerState{
		Group:    "orders",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	ctx := context.Background()
	resp, err := dispatcher.Dispatch(ctx, "flow-1", "orders", nil)
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("expected status 200, got %d", resp.Status)
	}
}

func TestDispatcher_WithScaler_AlreadyReady(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := &worker.ExecuteResponse{Status: 200}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	k8sClient := fake.NewSimpleClientset()
	store := &mockGroupReader{
		groups: map[string]config.Group{
			"orders": {
				ID:   "orders",
				Name: "Orders",
				Scaling: config.ScalingConfig{
					Mode: config.ScalingModeDynamic,
				},
			},
		},
	}

	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})
	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	scaler := NewScaler(ScalerConfig{
		K8sClient:      k8sClient,
		Namespace:      "flow-workers",
		Store:          store,
		Log:            &mockLogger{},
		Metrics:        metrics,
		StartupTimeout: 1 * time.Second,
	})

	dispatcher := NewDispatcherWithScaler(registry, client, DispatchConfig{}, &mockLogger{}, scaler, metrics)

	// Worker is already ready in registry.
	registry.mu.Lock()
	registry.workers["orders"] = &WorkerState{
		Group:    "orders",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 3,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	ctx := context.Background()
	resp, err := dispatcher.Dispatch(ctx, "flow-1", "orders", nil)
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("expected status 200, got %d", resp.Status)
	}
}

func TestDispatcher_RecordsMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := &worker.ExecuteResponse{Status: 200}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})

	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	dispatcher := NewDispatcherWithScaler(registry, client, DispatchConfig{}, &mockLogger{}, nil, metrics)

	registry.mu.Lock()
	registry.workers["orders"] = &WorkerState{
		Group:    "orders",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	ctx := context.Background()
	_, err := dispatcher.Dispatch(ctx, "flow-1", "orders", nil)
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	// Verify dispatch metrics were recorded.
	okCounter := getCounterValue(t, metrics.dispatchTotal, "orders", "ok")
	if okCounter != 1 {
		t.Errorf("expected dispatchTotal{orders,ok}=1, got %v", okCounter)
	}
}

func TestDispatcher_RecordsErrorMetrics(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	registry := NewRegistry(k8sClient, "flow-workers", &mockLogger{})
	client := NewWorkerClient(&mockLogger{})

	reg := prometheus.NewRegistry()
	metrics := NewGatewayMetrics(reg)

	dispatcher := NewDispatcherWithScaler(registry, client, DispatchConfig{}, &mockLogger{}, nil, metrics)

	// No worker registered — should error.
	ctx := context.Background()
	_, err := dispatcher.Dispatch(ctx, "flow-1", "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error")
	}

	// Verify error metrics were recorded.
	errorCounter := getCounterValue(t, metrics.dispatchTotal, "nonexistent", "error")
	if errorCounter != 1 {
		t.Errorf("expected dispatchTotal{nonexistent,error}=1, got %v", errorCounter)
	}
}

// int32Ptr returns a pointer to an int32 value.
func int32Ptr(i int32) *int32 {
	return &i
}
