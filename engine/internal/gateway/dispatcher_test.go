package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"

	"k8s.io/client-go/kubernetes/fake"
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
