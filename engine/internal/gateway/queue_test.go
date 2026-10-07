package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nzr-rules-engine/internal/worker"
)

func TestNewRequestQueue(t *testing.T) {
	tests := []struct {
		name        string
		maxSize     int
		timeout     time.Duration
		wantMaxSize int
		wantTimeout time.Duration
	}{
		{
			name:        "default values",
			maxSize:     0,
			timeout:     0,
			wantMaxSize: 100,
			wantTimeout: 30 * time.Second,
		},
		{
			name:        "custom values",
			maxSize:     50,
			timeout:     10 * time.Second,
			wantMaxSize: 50,
			wantTimeout: 10 * time.Second,
		},
		{
			name:        "negative maxSize uses default",
			maxSize:     -1,
			timeout:     5 * time.Second,
			wantMaxSize: 100,
			wantTimeout: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := NewRequestQueue(tt.maxSize, tt.timeout)

			if q.maxSize != tt.wantMaxSize {
				t.Errorf("maxSize = %d, want %d", q.maxSize, tt.wantMaxSize)
			}
			if q.timeout != tt.wantTimeout {
				t.Errorf("timeout = %v, want %v", q.timeout, tt.wantTimeout)
			}
		})
	}
}

func TestRequestQueue_EnqueueAndDrain(t *testing.T) {
	q := NewRequestQueue(10, 5*time.Second)

	// Start a goroutine to enqueue a request.
	var response *worker.ExecuteResponse
	var enqueueErr error
	done := make(chan struct{})

	go func() {
		response, enqueueErr = q.Enqueue(context.Background(), "test-group", "flow-1", map[string]any{"key": "value"})
		close(done)
	}()

	// Give time for the request to be enqueued.
	time.Sleep(50 * time.Millisecond)

	// Verify request is queued.
	if q.QueueLength("test-group") != 1 {
		t.Errorf("QueueLength = %d, want 1", q.QueueLength("test-group"))
	}

	if !q.IsQueueing("test-group") {
		t.Error("IsQueueing = false, want true")
	}

	// Drain the queue with a mock dispatch function.
	expectedResp := &worker.ExecuteResponse{Status: 200}
	q.Drain("test-group", func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
		if flowID != "flow-1" {
			t.Errorf("flowID = %s, want flow-1", flowID)
		}
		if input["key"] != "value" {
			t.Errorf("input[key] = %v, want value", input["key"])
		}
		return expectedResp, nil
	})

	// Wait for enqueue to complete.
	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("Enqueue did not return after Drain")
	}

	if enqueueErr != nil {
		t.Errorf("Enqueue error = %v, want nil", enqueueErr)
	}
	if response != expectedResp {
		t.Errorf("response = %v, want %v", response, expectedResp)
	}

	// Queue should be empty after drain.
	if q.QueueLength("test-group") != 0 {
		t.Errorf("QueueLength after drain = %d, want 0", q.QueueLength("test-group"))
	}
}

func TestRequestQueue_EnqueueTimeout(t *testing.T) {
	q := NewRequestQueue(10, 100*time.Millisecond)

	// Enqueue a request that will timeout.
	response, err := q.Enqueue(context.Background(), "test-group", "flow-1", nil)

	if !errors.Is(err, ErrQueueTimeout) {
		t.Errorf("err = %v, want ErrQueueTimeout", err)
	}
	if response != nil {
		t.Errorf("response = %v, want nil", response)
	}
}

func TestRequestQueue_EnqueueContextCancelled(t *testing.T) {
	q := NewRequestQueue(10, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())

	var response *worker.ExecuteResponse
	var enqueueErr error
	done := make(chan struct{})

	go func() {
		response, enqueueErr = q.Enqueue(ctx, "test-group", "flow-1", nil)
		close(done)
	}()

	// Give time for the request to be enqueued.
	time.Sleep(50 * time.Millisecond)

	// Cancel the context.
	cancel()

	// Wait for enqueue to complete.
	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("Enqueue did not return after context cancel")
	}

	if !errors.Is(enqueueErr, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", enqueueErr)
	}
	if response != nil {
		t.Errorf("response = %v, want nil", response)
	}
}

func TestRequestQueue_QueueFull(t *testing.T) {
	q := NewRequestQueue(2, 5*time.Second)

	// Fill the queue.
	go func() { _, _ = q.Enqueue(context.Background(), "test-group", "flow-1", nil) }()
	go func() { _, _ = q.Enqueue(context.Background(), "test-group", "flow-2", nil) }()

	time.Sleep(50 * time.Millisecond)

	// Verify queue is at capacity.
	if q.QueueLength("test-group") != 2 {
		t.Errorf("QueueLength = %d, want 2", q.QueueLength("test-group"))
	}

	// Third request should fail immediately.
	response, err := q.Enqueue(context.Background(), "test-group", "flow-3", nil)

	if !errors.Is(err, ErrQueueFull) {
		t.Errorf("err = %v, want ErrQueueFull", err)
	}
	if response != nil {
		t.Errorf("response = %v, want nil", response)
	}
}

func TestRequestQueue_DrainSkipsCancelledRequests(t *testing.T) {
	q := NewRequestQueue(10, 5*time.Second)

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2 := context.Background()

	// Enqueue two requests, cancel the first one.
	go func() { _, _ = q.Enqueue(ctx1, "test-group", "flow-1", nil) }()
	time.Sleep(20 * time.Millisecond)
	go func() { _, _ = q.Enqueue(ctx2, "test-group", "flow-2", nil) }()
	time.Sleep(20 * time.Millisecond)

	// Cancel the first request's context.
	cancel1()

	// Drain the queue.
	var processedFlows []string
	q.Drain("test-group", func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
		processedFlows = append(processedFlows, flowID)
		return &worker.ExecuteResponse{Status: 200}, nil
	})

	// Wait a bit for drain to complete.
	time.Sleep(50 * time.Millisecond)

	// Only flow-2 should have been processed (flow-1 was cancelled).
	if len(processedFlows) != 1 || processedFlows[0] != "flow-2" {
		t.Errorf("processedFlows = %v, want [flow-2]", processedFlows)
	}
}

func TestRequestQueue_DrainWithError(t *testing.T) {
	q := NewRequestQueue(10, 5*time.Second)

	var response *worker.ExecuteResponse
	var enqueueErr error
	done := make(chan struct{})

	go func() {
		response, enqueueErr = q.Enqueue(context.Background(), "test-group", "flow-1", nil)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	// Drain with an error.
	expectedErr := errors.New("dispatch failed")
	q.Drain("test-group", func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
		return nil, expectedErr
	})

	select {
	case <-done:
		// OK
	case <-time.After(time.Second):
		t.Fatal("Enqueue did not return after Drain")
	}

	if enqueueErr != expectedErr {
		t.Errorf("err = %v, want %v", enqueueErr, expectedErr)
	}
	if response != nil {
		t.Errorf("response = %v, want nil", response)
	}
}

func TestRequestQueue_DrainEmptyQueue(t *testing.T) {
	q := NewRequestQueue(10, 5*time.Second)

	// Drain an empty queue should not panic.
	q.Drain("test-group", func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
		t.Error("dispatch should not be called for empty queue")
		return nil, nil
	})

	// Drain non-existent group should not panic.
	q.Drain("nonexistent", func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
		t.Error("dispatch should not be called for nonexistent group")
		return nil, nil
	})
}

func TestRequestQueue_ConcurrentAccess(t *testing.T) {
	q := NewRequestQueue(100, 5*time.Second)

	const numGoroutines = 20
	var wg sync.WaitGroup
	var successCount atomic.Int32

	// Start multiple enqueuers.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, err := q.Enqueue(context.Background(), "test-group", "flow", map[string]any{"id": id})
			if err == nil {
				successCount.Add(1)
			}
		}(i)
	}

	// Give time for requests to be enqueued.
	time.Sleep(100 * time.Millisecond)

	// Drain concurrently.
	drainDone := make(chan struct{})
	go func() {
		q.Drain("test-group", func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
			return &worker.ExecuteResponse{Status: 200}, nil
		})
		close(drainDone)
	}()

	// Wait for all enqueuers.
	wg.Wait()

	select {
	case <-drainDone:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not complete")
	}

	if successCount.Load() != numGoroutines {
		t.Errorf("successCount = %d, want %d", successCount.Load(), numGoroutines)
	}
}

func TestRequestQueue_QueueLengthAndIsQueueing(t *testing.T) {
	q := NewRequestQueue(10, 5*time.Second)

	// Non-existent group.
	if q.QueueLength("nonexistent") != 0 {
		t.Errorf("QueueLength(nonexistent) = %d, want 0", q.QueueLength("nonexistent"))
	}
	if q.IsQueueing("nonexistent") {
		t.Error("IsQueueing(nonexistent) = true, want false")
	}

	// Start enqueuing.
	go func() { _, _ = q.Enqueue(context.Background(), "group-a", "flow-1", nil) }()
	go func() { _, _ = q.Enqueue(context.Background(), "group-b", "flow-2", nil) }()

	time.Sleep(50 * time.Millisecond)

	if q.QueueLength("group-a") != 1 {
		t.Errorf("QueueLength(group-a) = %d, want 1", q.QueueLength("group-a"))
	}
	if q.QueueLength("group-b") != 1 {
		t.Errorf("QueueLength(group-b) = %d, want 1", q.QueueLength("group-b"))
	}
	if !q.IsQueueing("group-a") {
		t.Error("IsQueueing(group-a) = false, want true")
	}
}

func TestRequestQueue_MultipleGroups(t *testing.T) {
	q := NewRequestQueue(10, 5*time.Second)

	done1 := make(chan struct{})
	done2 := make(chan struct{})

	var resp1, resp2 *worker.ExecuteResponse

	go func() {
		resp1, _ = q.Enqueue(context.Background(), "group-a", "flow-a", nil)
		close(done1)
	}()
	go func() {
		resp2, _ = q.Enqueue(context.Background(), "group-b", "flow-b", nil)
		close(done2)
	}()

	time.Sleep(50 * time.Millisecond)

	// Drain only group-a.
	q.Drain("group-a", func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
		return &worker.ExecuteResponse{Status: 200, Response: map[string]any{"group": "a"}}, nil
	})

	select {
	case <-done1:
		// OK
	case <-time.After(time.Second):
		t.Fatal("group-a enqueue did not complete")
	}

	// group-b should still be queued.
	if q.QueueLength("group-b") != 1 {
		t.Errorf("QueueLength(group-b) = %d, want 1", q.QueueLength("group-b"))
	}

	// Drain group-b.
	q.Drain("group-b", func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
		return &worker.ExecuteResponse{Status: 200, Response: map[string]any{"group": "b"}}, nil
	})

	select {
	case <-done2:
		// OK
	case <-time.After(time.Second):
		t.Fatal("group-b enqueue did not complete")
	}

	if resp1.Response["group"] != "a" {
		t.Errorf("resp1.Response[group] = %v, want a", resp1.Response["group"])
	}
	if resp2.Response["group"] != "b" {
		t.Errorf("resp2.Response[group] = %v, want b", resp2.Response["group"])
	}
}
