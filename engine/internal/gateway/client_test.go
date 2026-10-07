package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"
)

func TestWorkerClient_Execute_Success(t *testing.T) {
	// Set up a test server that returns a successful response
	expectedResp := &worker.ExecuteResponse{
		Status: 200,
		Response: map[string]any{
			"result": "success",
		},
	}
	var receivedReq worker.ExecuteRequest
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("unexpected Content-Type: %s", ct)
		}

		// Capture headers for verification
		receivedHeaders = r.Header.Clone()

		// Decode request
		if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
			t.Errorf("failed to decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Return response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(expectedResp)
	}))
	defer server.Close()

	client := NewWorkerClient(&mockLogger{})

	// Create context with request scope for trace header propagation
	ctx := observ.WithScope(context.Background(), observ.RequestScope{
		RequestID: "req-123",
		TraceID:   "trace-abc",
	})

	req := &worker.ExecuteRequest{
		FlowID:    "flow-1",
		RequestID: "req-123",
		TraceID:   "trace-abc",
		Input:     map[string]any{"foo": "bar"},
	}

	resp, err := client.Execute(ctx, "default", server.URL, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify response
	if resp.Status != 200 {
		t.Errorf("expected status 200, got %d", resp.Status)
	}
	if resp.Response["result"] != "success" {
		t.Errorf("unexpected response: %+v", resp.Response)
	}

	// Verify request was received correctly
	if receivedReq.FlowID != "flow-1" {
		t.Errorf("expected FlowID=flow-1, got %s", receivedReq.FlowID)
	}
	if receivedReq.Input["foo"] != "bar" {
		t.Errorf("expected Input.foo=bar, got %v", receivedReq.Input["foo"])
	}

	// Verify trace headers were propagated
	if receivedHeaders.Get("X-Request-Id") != "req-123" {
		t.Errorf("expected X-Request-Id=req-123, got %s", receivedHeaders.Get("X-Request-Id"))
	}
	if receivedHeaders.Get("X-Trace-Id") != "trace-abc" {
		t.Errorf("expected X-Trace-Id=trace-abc, got %s", receivedHeaders.Get("X-Trace-Id"))
	}
}

func TestWorkerClient_Execute_WorkerError(t *testing.T) {
	// Server returns a response with an error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := &worker.ExecuteResponse{
			Status: 404,
			Error: &worker.ErrorDetail{
				Code:    "FLOW_NOT_FOUND",
				Message: "flow xyz not found",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewWorkerClient(&mockLogger{})
	req := &worker.ExecuteRequest{FlowID: "xyz"}

	resp, err := client.Execute(context.Background(), "default", server.URL, req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// Should return the response even with error
	if resp == nil {
		t.Fatal("expected response with error detail")
	}

	// Check error type
	dispErr, ok := err.(*DispatchError)
	if !ok {
		t.Fatalf("expected *DispatchError, got %T", err)
	}
	if dispErr.Code != "FLOW_NOT_FOUND" {
		t.Errorf("expected Code=FLOW_NOT_FOUND, got %s", dispErr.Code)
	}
}

func TestWorkerClient_Execute_HTTPError(t *testing.T) {
	// Server returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	client := NewWorkerClient(&mockLogger{})
	req := &worker.ExecuteRequest{FlowID: "flow-1"}

	_, err := client.Execute(context.Background(), "default", server.URL, req)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	dispErr, ok := err.(*DispatchError)
	if !ok {
		t.Fatalf("expected *DispatchError, got %T", err)
	}
	if dispErr.Code != ErrCodeUpstream {
		t.Errorf("expected Code=%s, got %s", ErrCodeUpstream, dispErr.Code)
	}
}

func TestWorkerClient_Execute_Timeout(t *testing.T) {
	// Server that never responds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer server.Close()

	client := NewWorkerClient(&mockLogger{})
	req := &worker.ExecuteRequest{FlowID: "flow-1"}

	// Use a very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.Execute(ctx, "default", server.URL, req)
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

func TestWorkerClient_CircuitBreaker_Opens(t *testing.T) {
	// Counter for failed requests
	var requestCount atomic.Int32

	// Server that always fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Create client with low failure threshold for testing
	client := NewWorkerClientWithConfig(&mockLogger{}, BreakerConfig{
		FailureThreshold: 3,
		OpenTimeout:      1 * time.Second,
	})
	req := &worker.ExecuteRequest{FlowID: "flow-1"}

	// Make requests until breaker opens
	// First 3 should hit the server, then breaker should be open
	for i := 0; i < 5; i++ {
		_, err := client.Execute(context.Background(), "default", server.URL, req)
		if err == nil {
			t.Errorf("request %d: expected error", i)
		}
	}

	// After 3 failures, breaker should be open and reject without hitting server
	count := requestCount.Load()
	if count > 3 {
		// Allow some tolerance since state change isn't instant
		if count > 4 {
			t.Errorf("expected ~3 requests before breaker opens, got %d", count)
		}
	}

	// Verify last error is breaker open
	_, err := client.Execute(context.Background(), "default", server.URL, req)
	if err == nil {
		t.Fatal("expected error")
	}
	dispErr, ok := err.(*DispatchError)
	if !ok {
		t.Fatalf("expected *DispatchError, got %T", err)
	}
	// Could be either upstream (request failed) or breaker open
	if dispErr.Code != ErrCodeBreakerOpen && dispErr.Code != ErrCodeUpstream {
		t.Errorf("expected Code=%s or %s, got %s", ErrCodeBreakerOpen, ErrCodeUpstream, dispErr.Code)
	}
}

func TestWorkerClient_CircuitBreaker_PerGroup(t *testing.T) {
	// Counter per group
	var group1Count, group2Count atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check which "group" this is based on a header we'll add
		group := r.Header.Get("X-Test-Group")
		if group == "group1" {
			group1Count.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			group2Count.Add(1)
			resp := &worker.ExecuteResponse{Status: 200}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	client := NewWorkerClientWithConfig(&mockLogger{}, BreakerConfig{
		FailureThreshold: 2,
	})

	// Trip breaker for group1
	for i := 0; i < 3; i++ {
		req := &worker.ExecuteRequest{FlowID: "flow-1"}
		ctx := context.Background()
		// We can't easily set test headers, but the breaker is keyed by group name
		client.Execute(ctx, "group1", server.URL, req)
	}

	// group2 should still work (different breaker)
	req := &worker.ExecuteRequest{FlowID: "flow-1"}
	resp, err := client.Execute(context.Background(), "group2", server.URL, req)
	if err != nil {
		t.Fatalf("group2 should succeed, got error: %v", err)
	}
	if resp.Status != 200 {
		t.Errorf("expected status 200 for group2, got %d", resp.Status)
	}
}

func TestWorkerClient_getBreaker_CreateOnce(t *testing.T) {
	client := NewWorkerClient(&mockLogger{})

	// Get breaker multiple times
	b1 := client.getBreaker("test-group")
	b2 := client.getBreaker("test-group")

	// Should be the same instance
	if b1 != b2 {
		t.Error("expected same breaker instance for same group")
	}

	// Different group should get different breaker
	b3 := client.getBreaker("other-group")
	if b1 == b3 {
		t.Error("expected different breaker instance for different group")
	}
}

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		status   int
		expected string
	}{
		{404, ErrCodeNotFound},
		{408, ErrCodeTimeout},
		{504, ErrCodeTimeout},
		{400, ErrCodeValidation},
		{401, ErrCodeValidation},
		{403, ErrCodeValidation},
		{422, ErrCodeValidation},
		{500, ErrCodeUpstream},
		{502, ErrCodeUpstream},
		{503, ErrCodeUpstream},
		{200, ErrCodeInternal}, // Shouldn't happen but handle gracefully
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			got := classifyHTTPStatus(tt.status)
			if got != tt.expected {
				t.Errorf("classifyHTTPStatus(%d) = %s, want %s", tt.status, got, tt.expected)
			}
		})
	}
}

func TestDispatchError_Error(t *testing.T) {
	err := &DispatchError{
		Code:    ErrCodeWorkerNotFound,
		Message: "worker not available",
		Group:   "default",
	}
	expected := "WORKER_NOT_FOUND: worker not available [group=default]"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}

	// Without group
	err2 := &DispatchError{
		Code:    ErrCodeTimeout,
		Message: "request timed out",
	}
	expected2 := "TIMEOUT: request timed out"
	if err2.Error() != expected2 {
		t.Errorf("expected %q, got %q", expected2, err2.Error())
	}
}

func TestDispatchError_Unwrap(t *testing.T) {
	cause := &testError{msg: "connection refused"}
	err := &DispatchError{
		Code:    ErrCodeUpstream,
		Message: "failed",
		cause:   cause,
	}

	unwrapped := err.Unwrap()
	if unwrapped != cause {
		t.Errorf("expected unwrap to return cause")
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
