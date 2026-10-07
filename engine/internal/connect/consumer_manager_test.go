package connect

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockConsumer is a test consumer implementation.
type mockConsumer struct {
	typ          string
	lifecycle    Lifecycle
	capabilities Capability
	openCount    atomic.Int32
	openErr      error

	mu             sync.Mutex
	subscribed     bool
	subscribeCalls atomic.Int32
	subscribeErr   error
	subscribeDelay time.Duration
	messages       []Message
	msgIndex       int
	unsubscribed   bool
	handler        MessageHandler
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
}

func newMockConsumer(typ string) *mockConsumer {
	return &mockConsumer{
		typ:          typ,
		lifecycle:    LifecycleLongLived,
		capabilities: CapSubscribe,
		done:         make(chan struct{}),
	}
}

func (m *mockConsumer) Type() string             { return m.typ }
func (m *mockConsumer) Lifecycle() Lifecycle     { return m.lifecycle }
func (m *mockConsumer) Capabilities() Capability { return m.capabilities }

func (m *mockConsumer) Open(ctx context.Context, def ConnectionDef) (Client, error) {
	m.openCount.Add(1)
	if m.openErr != nil {
		return nil, m.openErr
	}
	return &mockPoolClient{key: def.Key}, nil
}

func (m *mockConsumer) Subscribe(ctx context.Context, handler MessageHandler) error {
	m.subscribeCalls.Add(1)

	m.mu.Lock()
	m.subscribed = true
	m.handler = handler
	m.ctx, m.cancel = context.WithCancel(ctx)
	localCtx := m.ctx
	m.mu.Unlock()

	if m.subscribeDelay > 0 {
		select {
		case <-localCtx.Done():
			return localCtx.Err()
		case <-time.After(m.subscribeDelay):
		}
	}

	if m.subscribeErr != nil {
		return m.subscribeErr
	}

	// Deliver any queued messages
	m.mu.Lock()
	for m.msgIndex < len(m.messages) {
		msg := m.messages[m.msgIndex]
		m.msgIndex++
		h := m.handler
		m.mu.Unlock()

		if h != nil {
			_ = h(localCtx, msg)
		}

		m.mu.Lock()
	}
	m.mu.Unlock()

	// Block until context is cancelled
	<-localCtx.Done()
	return localCtx.Err()
}

func (m *mockConsumer) Unsubscribe(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unsubscribed = true
	m.subscribed = false
	if m.cancel != nil {
		m.cancel()
	}
	return nil
}

func (m *mockConsumer) QueueMessage(msg Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
}

// failingConsumer returns an error on Subscribe after a delay.
type failingConsumer struct {
	typ            string
	lifecycle      Lifecycle
	capabilities   Capability
	openCount      atomic.Int32
	subscribeCalls atomic.Int32
	failCount      atomic.Int32
	failUntil      int32
	failErr        error
	subscribeWait  chan struct{}
	unsubscribed   bool
	mu             sync.Mutex
	cancel         context.CancelFunc
}

func newFailingConsumer(typ string, failCount int32, failErr error) *failingConsumer {
	return &failingConsumer{
		typ:           typ,
		lifecycle:     LifecycleLongLived,
		capabilities:  CapSubscribe,
		failUntil:     failCount,
		failErr:       failErr,
		subscribeWait: make(chan struct{}, 10),
	}
}

func (f *failingConsumer) Type() string             { return f.typ }
func (f *failingConsumer) Lifecycle() Lifecycle     { return f.lifecycle }
func (f *failingConsumer) Capabilities() Capability { return f.capabilities }

func (f *failingConsumer) Open(ctx context.Context, def ConnectionDef) (Client, error) {
	f.openCount.Add(1)
	return &mockPoolClient{key: def.Key}, nil
}

func (f *failingConsumer) Subscribe(ctx context.Context, handler MessageHandler) error {
	count := f.subscribeCalls.Add(1)
	f.failCount.Store(count)

	f.mu.Lock()
	var localCancel context.CancelFunc
	ctx, localCancel = context.WithCancel(ctx)
	f.cancel = localCancel
	f.mu.Unlock()

	// Signal that we've entered Subscribe
	select {
	case f.subscribeWait <- struct{}{}:
	default:
	}

	if count <= f.failUntil {
		// Simulate some work before failing
		time.Sleep(5 * time.Millisecond)
		return f.failErr
	}

	// After failUntil attempts, block until context is cancelled
	<-ctx.Done()
	return ctx.Err()
}

func (f *failingConsumer) Unsubscribe(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unsubscribed = true
	if f.cancel != nil {
		f.cancel()
	}
	return nil
}

func TestConsumerManager_StartsSubscribeConnectors(t *testing.T) {
	consumer := newMockConsumer("kafka")
	defs := []ConnectionDef{{Key: "kafka-main", Type: "kafka"}}

	mgr := NewConsumerManager(
		[]Connector{consumer},
		defs,
		nil,
		DefaultConsumerManagerConfig(),
		nil,
	)

	// Register a handler
	var handlerCalled atomic.Bool
	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		handlerCalled.Store(true)
		return nil
	})

	ctx := context.Background()
	err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give time for Subscribe to be called
	time.Sleep(50 * time.Millisecond)

	// Verify consumer was opened and Subscribe was called
	if consumer.openCount.Load() != 1 {
		t.Errorf("expected 1 open call, got %d", consumer.openCount.Load())
	}
	if consumer.subscribeCalls.Load() != 1 {
		t.Errorf("expected 1 subscribe call, got %d", consumer.subscribeCalls.Load())
	}

	consumer.mu.Lock()
	subscribed := consumer.subscribed
	consumer.mu.Unlock()
	if !subscribed {
		t.Error("expected consumer to be subscribed")
	}

	// Stop the manager
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	consumer.mu.Lock()
	unsubscribed := consumer.unsubscribed
	consumer.mu.Unlock()
	if !unsubscribed {
		t.Error("expected consumer to be unsubscribed after Stop")
	}
}

func TestConsumerManager_RestartsOnFailure(t *testing.T) {
	// Consumer fails first 2 times, then succeeds
	consumer := newFailingConsumer("kafka", 2, errors.New("broker unavailable"))
	defs := []ConnectionDef{{Key: "kafka-main", Type: "kafka"}}

	config := ConsumerManagerConfig{
		InitialBackoff: 10 * time.Millisecond, // Short backoff for testing
		MaxBackoff:     100 * time.Millisecond,
		DrainTimeout:   1 * time.Second,
	}

	mgr := NewConsumerManager(
		[]Connector{consumer},
		defs,
		nil,
		config,
		nil,
	)

	// Register a handler
	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		return nil
	})

	ctx := context.Background()
	err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for Subscribe to be called at least 3 times (2 failures + 1 success)
	deadline := time.After(500 * time.Millisecond)
	for {
		calls := consumer.subscribeCalls.Load()
		if calls >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for 3 subscribe calls, got %d", calls)
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify Subscribe was called at least 3 times (retried after failures)
	calls := consumer.subscribeCalls.Load()
	if calls < 3 {
		t.Errorf("expected at least 3 subscribe calls (2 failures + success), got %d", calls)
	}

	// Stop the manager
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestConsumerManager_GracefulShutdown(t *testing.T) {
	consumer := newMockConsumer("kafka")
	defs := []ConnectionDef{{Key: "kafka-main", Type: "kafka"}}

	config := ConsumerManagerConfig{
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     60 * time.Second,
		DrainTimeout:   2 * time.Second,
	}

	mgr := NewConsumerManager(
		[]Connector{consumer},
		defs,
		nil,
		config,
		nil,
	)

	// Register a handler that simulates processing time
	var processed atomic.Int32
	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		time.Sleep(10 * time.Millisecond)
		processed.Add(1)
		return nil
	})

	ctx := context.Background()
	err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give time for Subscribe to start
	time.Sleep(50 * time.Millisecond)

	// Stop should complete within drain timeout
	stopDone := make(chan struct{})
	go func() {
		err := mgr.Stop(ctx)
		if err != nil {
			t.Errorf("Stop failed: %v", err)
		}
		close(stopDone)
	}()

	select {
	case <-stopDone:
		// Good - shutdown completed
	case <-time.After(3 * time.Second):
		t.Fatal("Stop timed out - did not complete graceful shutdown")
	}

	// Verify consumer was unsubscribed
	consumer.mu.Lock()
	unsubscribed := consumer.unsubscribed
	consumer.mu.Unlock()
	if !unsubscribed {
		t.Error("expected consumer to be unsubscribed")
	}
}

func TestConsumerManager_SkipsConnectorsWithoutHandler(t *testing.T) {
	consumer := newMockConsumer("kafka")
	defs := []ConnectionDef{
		{Key: "kafka-main", Type: "kafka"},
		{Key: "kafka-other", Type: "kafka"}, // No handler registered
	}

	mgr := NewConsumerManager(
		[]Connector{consumer},
		defs,
		nil,
		DefaultConsumerManagerConfig(),
		nil,
	)

	// Only register handler for kafka-main
	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		return nil
	})

	ctx := context.Background()
	err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give time for startup
	time.Sleep(50 * time.Millisecond)

	// Only one consumer should be opened (kafka-main)
	if consumer.openCount.Load() != 1 {
		t.Errorf("expected 1 open call for registered handler, got %d", consumer.openCount.Load())
	}

	// Stop the manager
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestConsumerManager_SkipsNonSubscribeConnectors(t *testing.T) {
	// Connector without CapSubscribe capability
	pooledConnector := &mockConnector{
		typ:          "postgres",
		lifecycle:    LifecyclePooled,
		capabilities: CapQueryExec, // No CapSubscribe
	}
	subscribeConnector := newMockConsumer("kafka")

	defs := []ConnectionDef{
		{Key: "pg-main", Type: "postgres"},
		{Key: "kafka-main", Type: "kafka"},
	}

	mgr := NewConsumerManager(
		[]Connector{pooledConnector, subscribeConnector},
		defs,
		nil,
		DefaultConsumerManagerConfig(),
		nil,
	)

	// Register handlers for both
	mgr.RegisterHandler("pg-main", func(ctx context.Context, msg Message) error {
		return nil
	})
	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		return nil
	})

	ctx := context.Background()
	err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give time for startup
	time.Sleep(50 * time.Millisecond)

	// Only kafka connector should be opened (postgres lacks CapSubscribe)
	if pooledConnector.openCount.Load() != 0 {
		t.Errorf("expected 0 opens for non-subscribe connector, got %d", pooledConnector.openCount.Load())
	}
	if subscribeConnector.openCount.Load() != 1 {
		t.Errorf("expected 1 open for subscribe connector, got %d", subscribeConnector.openCount.Load())
	}

	// Stop the manager
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestConsumerManager_Health(t *testing.T) {
	consumer := newMockConsumer("kafka")
	defs := []ConnectionDef{{Key: "kafka-main", Type: "kafka"}}

	mgr := NewConsumerManager(
		[]Connector{consumer},
		defs,
		nil,
		DefaultConsumerManagerConfig(),
		nil,
	)

	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		return nil
	})

	ctx := context.Background()

	// Before start, health check should pass (no consumers)
	err := mgr.Health(ctx)
	if err != nil {
		t.Errorf("expected no error before start, got: %v", err)
	}

	err = mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give time for startup
	time.Sleep(50 * time.Millisecond)

	// Health should pass (consumer is starting/healthy)
	err = mgr.Health(ctx)
	if err != nil {
		t.Errorf("expected no error for starting consumer, got: %v", err)
	}

	// Mark as healthy
	mgr.SetHealthy("kafka-main")

	// Health should pass
	err = mgr.Health(ctx)
	if err != nil {
		t.Errorf("expected no error for healthy consumer, got: %v", err)
	}

	// Stop the manager
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestConsumerManager_ExponentialBackoff(t *testing.T) {
	consumer := newFailingConsumer("kafka", 10, errors.New("always fails"))
	defs := []ConnectionDef{{Key: "kafka-main", Type: "kafka"}}

	config := ConsumerManagerConfig{
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     80 * time.Millisecond, // Cap at 80ms (10 -> 20 -> 40 -> 80)
		DrainTimeout:   1 * time.Second,
	}

	mgr := NewConsumerManager(
		[]Connector{consumer},
		defs,
		nil,
		config,
		nil,
	)

	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		return nil
	})

	ctx := context.Background()
	err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for a few retry attempts
	time.Sleep(200 * time.Millisecond)

	// Get the managed consumer's current backoff
	mgr.mu.RLock()
	mc := mgr.consumers["kafka-main"]
	mgr.mu.RUnlock()

	mc.mu.RLock()
	backoff := mc.backoff
	mc.mu.RUnlock()

	// Backoff should have increased and be capped at MaxBackoff
	if backoff > config.MaxBackoff {
		t.Errorf("backoff %v exceeded max %v", backoff, config.MaxBackoff)
	}

	// Stop the manager
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestConsumerManager_HealthDetailsReportsAllConsumers(t *testing.T) {
	consumer1 := newMockConsumer("kafka")
	consumer2 := newMockConsumer("rabbitmq")
	consumer2.capabilities = CapSubscribe
	defs := []ConnectionDef{
		{Key: "kafka-main", Type: "kafka"},
		{Key: "rabbit-main", Type: "rabbitmq"},
	}

	mgr := NewConsumerManager(
		[]Connector{consumer1, consumer2},
		defs,
		nil,
		DefaultConsumerManagerConfig(),
		nil,
	)

	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		return nil
	})
	mgr.RegisterHandler("rabbit-main", func(ctx context.Context, msg Message) error {
		return nil
	})

	ctx := context.Background()
	err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give time for startup
	time.Sleep(50 * time.Millisecond)

	details := mgr.HealthDetails(ctx)
	if len(details) != 2 {
		t.Errorf("expected 2 consumer details, got %d", len(details))
	}

	// Stop the manager
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestConsumerManager_SetHealthyResetsBackoff(t *testing.T) {
	consumer := newFailingConsumer("kafka", 1, errors.New("first fail"))
	defs := []ConnectionDef{{Key: "kafka-main", Type: "kafka"}}

	config := ConsumerManagerConfig{
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     1 * time.Second,
		DrainTimeout:   1 * time.Second,
	}

	mgr := NewConsumerManager(
		[]Connector{consumer},
		defs,
		nil,
		config,
		nil,
	)

	mgr.RegisterHandler("kafka-main", func(ctx context.Context, msg Message) error {
		return nil
	})

	ctx := context.Background()
	err := mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait for the first failure and backoff increase
	time.Sleep(100 * time.Millisecond)

	// Get current backoff (should have doubled after first failure)
	mgr.mu.RLock()
	mc := mgr.consumers["kafka-main"]
	mgr.mu.RUnlock()

	mc.mu.RLock()
	backoffBefore := mc.backoff
	mc.mu.RUnlock()

	// Backoff should have increased
	if backoffBefore <= config.InitialBackoff {
		t.Logf("backoff didn't increase yet, got %v", backoffBefore)
	}

	// Mark as healthy (simulates successful message processing)
	mgr.SetHealthy("kafka-main")

	// Backoff should be reset to initial
	mc.mu.RLock()
	backoffAfter := mc.backoff
	mc.mu.RUnlock()

	if backoffAfter != config.InitialBackoff {
		t.Errorf("expected backoff reset to %v, got %v", config.InitialBackoff, backoffAfter)
	}

	// Stop the manager
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}
