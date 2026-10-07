package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// mockLogger is a no-op Logger for tests.
type mockLogger struct{}

func (m *mockLogger) Emit(_ context.Context, _, _ string, _ map[string]any) {}

func TestRegistryGetWorker_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	state, ok := registry.GetWorker("nonexistent")
	if ok {
		t.Errorf("expected GetWorker to return false for unknown group, got true")
	}
	if state != nil {
		t.Errorf("expected nil WorkerState for unknown group, got %+v", state)
	}
}

func TestRegistryGetWorker_Found(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Manually add a worker to the registry
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: "http://worker-default.flow-workers.svc:8080",
		Ready:    true,
		Replicas: 2,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	state, ok := registry.GetWorker("default")
	if !ok {
		t.Fatalf("expected GetWorker to return true for known group, got false")
	}
	if state.Group != "default" {
		t.Errorf("expected Group='default', got %q", state.Group)
	}
	if state.Endpoint != "http://worker-default.flow-workers.svc:8080" {
		t.Errorf("expected Endpoint='http://worker-default.flow-workers.svc:8080', got %q", state.Endpoint)
	}
	if !state.Ready {
		t.Errorf("expected Ready=true, got false")
	}
	if state.Replicas != 2 {
		t.Errorf("expected Replicas=2, got %d", state.Replicas)
	}
	if state.Health != HealthHealthy {
		t.Errorf("expected Health=HealthHealthy, got %v", state.Health)
	}
}

func TestRegistryGetWorker_ReturnsCopy(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: "http://worker-default.flow-workers.svc:8080",
		Ready:    true,
		Replicas: 2,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	// Get a copy
	state, _ := registry.GetWorker("default")
	// Mutate the copy
	state.Ready = false
	state.Health = HealthUnhealthy

	// Verify original is unchanged
	original, _ := registry.GetWorker("default")
	if !original.Ready {
		t.Errorf("mutating returned state affected original: Ready should be true")
	}
	if original.Health != HealthHealthy {
		t.Errorf("mutating returned state affected original: Health should be HealthHealthy")
	}
}

func TestRegistryUpdateHealth(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Add a worker with unknown health
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: "http://worker-default.flow-workers.svc:8080",
		Ready:    true,
		Replicas: 1,
		Health:   HealthUnknown,
	}
	registry.mu.Unlock()

	// Update to healthy
	registry.UpdateHealth("default", HealthHealthy)
	state, _ := registry.GetWorker("default")
	if state.Health != HealthHealthy {
		t.Errorf("expected Health=HealthHealthy after update, got %v", state.Health)
	}

	// Update to unhealthy
	registry.UpdateHealth("default", HealthUnhealthy)
	state, _ = registry.GetWorker("default")
	if state.Health != HealthUnhealthy {
		t.Errorf("expected Health=HealthUnhealthy after update, got %v", state.Health)
	}

	// UpdateHealth for unknown group is a no-op (no panic)
	registry.UpdateHealth("unknown", HealthHealthy) // Should not panic
}

func TestRegistryMarkLastRequest(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:       "default",
		Endpoint:    "http://worker-default.flow-workers.svc:8080",
		Ready:       true,
		Replicas:    1,
		Health:      HealthHealthy,
		LastRequest: time.Time{},
	}
	registry.mu.Unlock()

	before := time.Now()
	registry.MarkLastRequest("default")
	after := time.Now()

	state, _ := registry.GetWorker("default")
	if state.LastRequest.Before(before) || state.LastRequest.After(after) {
		t.Errorf("expected LastRequest to be between %v and %v, got %v", before, after, state.LastRequest)
	}

	// MarkLastRequest for unknown group is a no-op (no panic)
	registry.MarkLastRequest("unknown")
}

func TestRegistryListWorkers(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Empty registry
	groups := registry.listWorkers()
	if len(groups) != 0 {
		t.Errorf("expected empty list, got %v", groups)
	}

	// Add some workers
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{Group: "default"}
	registry.workers["high-priority"] = &WorkerState{Group: "high-priority"}
	registry.mu.Unlock()

	groups = registry.listWorkers()
	if len(groups) != 2 {
		t.Errorf("expected 2 groups, got %d", len(groups))
	}
	// Check that both groups are present (order not guaranteed)
	groupSet := make(map[string]bool)
	for _, g := range groups {
		groupSet[g] = true
	}
	if !groupSet["default"] {
		t.Errorf("expected 'default' in groups list")
	}
	if !groupSet["high-priority"] {
		t.Errorf("expected 'high-priority' in groups list")
	}
}

func TestExtractGroupFromServiceName(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"worker-default", "default"},
		{"worker-high-priority", "high-priority"},
		{"worker-", ""},          // Edge case: empty group
		{"not-a-worker", ""},     // Does not match prefix
		{"", ""},                 // Empty string
		{"workerdefault", ""},    // Missing hyphen
		{"WORKER-default", ""},   // Wrong case
		{"worker-my-group", "my-group"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractGroupFromServiceName(tt.name)
			if got != tt.expected {
				t.Errorf("extractGroupFromServiceName(%q) = %q, want %q", tt.name, got, tt.expected)
			}
		})
	}
}

func TestHealthStatusString(t *testing.T) {
	tests := []struct {
		status   HealthStatus
		expected string
	}{
		{HealthUnknown, "unknown"},
		{HealthHealthy, "healthy"},
		{HealthUnhealthy, "unhealthy"},
		{HealthStatus(99), "unknown"}, // Invalid value defaults to unknown
	}

	for _, tt := range tests {
		got := tt.status.String()
		if got != tt.expected {
			t.Errorf("HealthStatus(%d).String() = %q, want %q", tt.status, got, tt.expected)
		}
	}
}

func TestRegistryHandleEndpointsUpdate(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Create an Endpoints resource for worker-default
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-default",
			Namespace: "flow-workers",
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{
					{IP: "10.0.0.1"},
					{IP: "10.0.0.2"},
				},
			},
		},
	}

	registry.handleEndpointsUpdate(ep)

	state, ok := registry.GetWorker("default")
	if !ok {
		t.Fatalf("expected worker 'default' to be registered")
	}
	if state.Endpoint != "http://worker-default.flow-workers.svc:8080" {
		t.Errorf("unexpected endpoint: %s", state.Endpoint)
	}
	if !state.Ready {
		t.Errorf("expected Ready=true with addresses present")
	}
	if state.Replicas != 2 {
		t.Errorf("expected Replicas=2, got %d", state.Replicas)
	}
	if state.Health != HealthUnknown {
		t.Errorf("expected Health=HealthUnknown for new worker, got %v", state.Health)
	}
}

func TestRegistryHandleEndpointsUpdate_NoAddresses(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Endpoints with no ready addresses
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-default",
			Namespace: "flow-workers",
		},
		Subsets: []corev1.EndpointSubset{},
	}

	registry.handleEndpointsUpdate(ep)

	state, ok := registry.GetWorker("default")
	if !ok {
		t.Fatalf("expected worker 'default' to be registered")
	}
	if state.Ready {
		t.Errorf("expected Ready=false with no addresses")
	}
	if state.Replicas != 0 {
		t.Errorf("expected Replicas=0, got %d", state.Replicas)
	}
}

func TestRegistryHandleEndpointsUpdate_PreservesExistingState(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Pre-populate with existing state
	lastReq := time.Now().Add(-time.Hour)
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:       "default",
		Endpoint:    "http://old-endpoint:8080",
		Ready:       false,
		Replicas:    0,
		Health:      HealthHealthy,
		LastRequest: lastReq,
	}
	registry.mu.Unlock()

	// Update with new endpoints
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-default",
			Namespace: "flow-workers",
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}},
			},
		},
	}

	registry.handleEndpointsUpdate(ep)

	state, _ := registry.GetWorker("default")
	// Endpoint, Ready, Replicas should be updated
	if state.Endpoint != "http://worker-default.flow-workers.svc:8080" {
		t.Errorf("expected endpoint to be updated")
	}
	if !state.Ready {
		t.Errorf("expected Ready to be updated to true")
	}
	if state.Replicas != 1 {
		t.Errorf("expected Replicas to be updated to 1")
	}
	// Health and LastRequest should be preserved
	if state.Health != HealthHealthy {
		t.Errorf("expected Health to be preserved as HealthHealthy")
	}
	if !state.LastRequest.Equal(lastReq) {
		t.Errorf("expected LastRequest to be preserved")
	}
}

func TestRegistryHandleEndpointsUpdate_IgnoresNonWorkerServices(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Endpoints for a non-worker service
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-api-server",
			Namespace: "flow-workers",
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{{IP: "10.0.0.1"}},
			},
		},
	}

	registry.handleEndpointsUpdate(ep)

	if len(registry.listWorkers()) != 0 {
		t.Errorf("expected no workers to be registered for non-worker service")
	}
}

func TestRegistryHandleEndpointsDelete(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Add a worker
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{Group: "default"}
	registry.mu.Unlock()

	// Delete it
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-default",
			Namespace: "flow-workers",
		},
	}
	registry.handleEndpointsDelete(ep)

	_, ok := registry.GetWorker("default")
	if ok {
		t.Errorf("expected worker 'default' to be deleted")
	}
}

func TestRegistryWatch_DiscoveryFromEndpoints(t *testing.T) {
	// Create fake client with pre-existing endpoints
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "worker-default",
			Namespace: "flow-workers",
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{
					{IP: "10.0.0.1"},
					{IP: "10.0.0.2"},
				},
			},
		},
	}
	client := fake.NewSimpleClientset(ep)
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start watching
	registry.Watch(ctx)

	// Give the informer time to sync and process
	time.Sleep(200 * time.Millisecond)

	// Verify the endpoint was discovered
	state, ok := registry.GetWorker("default")
	if !ok {
		t.Fatalf("expected worker 'default' to be discovered from endpoints")
	}
	if state.Endpoint != "http://worker-default.flow-workers.svc:8080" {
		t.Errorf("unexpected endpoint: %s", state.Endpoint)
	}
	if state.Replicas != 2 {
		t.Errorf("expected Replicas=2, got %d", state.Replicas)
	}

	// Cleanup
	registry.Close()
}

func TestRegistryProbeWorker_Healthy(t *testing.T) {
	// Start a test HTTP server that returns 200 on /healthz
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Add a worker pointing to our test server
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthUnknown,
	}
	registry.mu.Unlock()

	// Probe the worker
	registry.probeWorker(context.Background(), "default")

	state, _ := registry.GetWorker("default")
	if state.Health != HealthHealthy {
		t.Errorf("expected Health=HealthHealthy after successful probe, got %v", state.Health)
	}
}

func TestRegistryProbeWorker_Unhealthy(t *testing.T) {
	// Start a test HTTP server that returns 503 on /healthz
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Add a worker pointing to our test server
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: server.URL,
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	// Probe the worker
	registry.probeWorker(context.Background(), "default")

	state, _ := registry.GetWorker("default")
	if state.Health != HealthUnhealthy {
		t.Errorf("expected Health=HealthUnhealthy after 503 response, got %v", state.Health)
	}
}

func TestRegistryProbeWorker_ConnectionFailure(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	// Add a worker pointing to an unreachable endpoint
	registry.mu.Lock()
	registry.workers["default"] = &WorkerState{
		Group:    "default",
		Endpoint: "http://127.0.0.1:1", // Port 1 is typically unreachable
		Ready:    true,
		Replicas: 1,
		Health:   HealthHealthy,
	}
	registry.mu.Unlock()

	// Use a very short timeout for the test
	registry.httpClient.Timeout = 100 * time.Millisecond

	// Probe the worker
	registry.probeWorker(context.Background(), "default")

	state, _ := registry.GetWorker("default")
	if state.Health != HealthUnhealthy {
		t.Errorf("expected Health=HealthUnhealthy after connection failure, got %v", state.Health)
	}
}

func TestRegistryClose(t *testing.T) {
	client := fake.NewSimpleClientset()
	registry := NewRegistry(client, "flow-workers", &mockLogger{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start watching
	registry.Watch(ctx)

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Close should not hang and should complete
	done := make(chan struct{})
	go func() {
		registry.Close()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Errorf("Close() did not complete within timeout")
	}
}
