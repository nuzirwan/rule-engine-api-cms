package gateway

import (
	"context"
	"errors"
	"sync"
	"time"

	"nzr-rules-engine/internal/worker"
)

// ErrQueueFull is returned when the request queue for a group is at capacity.
var ErrQueueFull = errors.New("request queue is full")

// ErrQueueTimeout is returned when a queued request times out waiting for a worker.
var ErrQueueTimeout = errors.New("request queue timeout waiting for worker")

// queuedRequest represents a request waiting in the queue during cold start.
type queuedRequest struct {
	ctx        context.Context
	flowID     string
	input      map[string]any
	responseCh chan *queueResponse
}

// queueResponse carries the result of a dispatched queued request.
type queueResponse struct {
	response *worker.ExecuteResponse
	err      error
}

// RequestQueue holds requests during cold start scaling operations. When a worker
// group scales from 0, incoming requests are queued rather than rejected or blocked
// synchronously. Once the worker becomes ready, the queue is drained.
type RequestQueue struct {
	queues   map[string]chan *queuedRequest
	maxSize  int
	timeout  time.Duration
	mu       sync.RWMutex
	metrics  *GatewayMetrics
}

// NewRequestQueue creates a new RequestQueue with the given configuration.
// maxSize limits the number of queued requests per group.
// timeout is how long a request will wait in the queue before timing out.
func NewRequestQueue(maxSize int, timeout time.Duration) *RequestQueue {
	if maxSize <= 0 {
		maxSize = 100 // Default to 100 requests per group
	}
	if timeout <= 0 {
		timeout = 30 * time.Second // Default to 30 seconds
	}
	return &RequestQueue{
		queues:  make(map[string]chan *queuedRequest),
		maxSize: maxSize,
		timeout: timeout,
	}
}

// SetMetrics sets the metrics collector for queue metrics.
func (q *RequestQueue) SetMetrics(m *GatewayMetrics) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.metrics = m
}

// getOrCreateQueue returns the queue for the given group, creating it if needed.
func (q *RequestQueue) getOrCreateQueue(group string) chan *queuedRequest {
	q.mu.Lock()
	defer q.mu.Unlock()

	ch, ok := q.queues[group]
	if !ok {
		ch = make(chan *queuedRequest, q.maxSize)
		q.queues[group] = ch
	}
	return ch
}

// getQueue returns the queue for the group without creating, or nil if not exists.
func (q *RequestQueue) getQueue(group string) chan *queuedRequest {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.queues[group]
}

// Enqueue adds a request to the queue for the given group and blocks until the
// request is processed or times out. Returns the execution response or an error.
func (q *RequestQueue) Enqueue(ctx context.Context, group string, flowID string, input map[string]any) (*worker.ExecuteResponse, error) {
	ch := q.getOrCreateQueue(group)

	// Create the queued request with a response channel.
	responseCh := make(chan *queueResponse, 1)
	req := &queuedRequest{
		ctx:        ctx,
		flowID:     flowID,
		input:      input,
		responseCh: responseCh,
	}

	// Try to enqueue, fail fast if queue is full.
	select {
	case ch <- req:
		// Successfully enqueued, update metrics.
		q.updateQueueLengthMetric(group)
	default:
		// Queue is full.
		return nil, ErrQueueFull
	}

	// Wait for response, context cancellation, or timeout.
	timeoutCtx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()

	select {
	case result := <-responseCh:
		return result.response, result.err
	case <-timeoutCtx.Done():
		// Timed out waiting for worker.
		q.incTimeoutMetric(group)
		// Request is still in the queue but we're abandoning it.
		// The Drain() will skip it when it sees the closed context.
		return nil, ErrQueueTimeout
	case <-ctx.Done():
		// Caller cancelled.
		return nil, ctx.Err()
	}
}

// Drain processes all queued requests for the group using the provided dispatch
// function. This should be called when the worker becomes ready. The dispatch
// function is typically Dispatcher.forward() or similar.
func (q *RequestQueue) Drain(group string, dispatch func(ctx context.Context, flowID string, input map[string]any) (*worker.ExecuteResponse, error)) {
	ch := q.getQueue(group)
	if ch == nil {
		return
	}

	// Process all queued requests without blocking.
	for {
		select {
		case req := <-ch:
			// Skip requests whose context has been cancelled (timeout/cancelled).
			if req.ctx.Err() != nil {
				// Send error to the response channel in case someone is still waiting.
				select {
				case req.responseCh <- &queueResponse{err: req.ctx.Err()}:
				default:
				}
				continue
			}

			// Dispatch the request.
			resp, err := dispatch(req.ctx, req.flowID, req.input)

			// Send response back to the waiting goroutine.
			select {
			case req.responseCh <- &queueResponse{response: resp, err: err}:
			default:
				// Response channel was closed or nobody waiting (timed out).
			}
		default:
			// No more requests in queue.
			q.updateQueueLengthMetric(group)
			return
		}
	}
}

// IsQueueing returns true if the group has requests waiting in the queue.
func (q *RequestQueue) IsQueueing(group string) bool {
	ch := q.getQueue(group)
	if ch == nil {
		return false
	}
	return len(ch) > 0
}

// QueueLength returns the number of requests queued for the given group.
func (q *RequestQueue) QueueLength(group string) int {
	ch := q.getQueue(group)
	if ch == nil {
		return 0
	}
	return len(ch)
}

// updateQueueLengthMetric updates the queue length gauge for the group.
func (q *RequestQueue) updateQueueLengthMetric(group string) {
	q.mu.RLock()
	m := q.metrics
	q.mu.RUnlock()

	if m != nil {
		m.SetQueueLength(group, q.QueueLength(group))
	}
}

// incTimeoutMetric increments the timeout counter for the group.
func (q *RequestQueue) incTimeoutMetric(group string) {
	q.mu.RLock()
	m := q.metrics
	q.mu.RUnlock()

	if m != nil {
		m.IncQueueTimeout(group)
	}
}
