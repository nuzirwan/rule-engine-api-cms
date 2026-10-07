package connect

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockConnector is a test connector that tracks Open calls.
type mockConnector struct {
	typ          string
	openCount    atomic.Int32
	openErr      error
	openDelay    time.Duration
	lifecycle    Lifecycle
	capabilities Capability
}

func (m *mockConnector) Type() string             { return m.typ }
func (m *mockConnector) Lifecycle() Lifecycle     { return m.lifecycle }
func (m *mockConnector) Capabilities() Capability { return m.capabilities }
func (m *mockConnector) Open(ctx context.Context, def ConnectionDef) (Client, error) {
	if m.openDelay > 0 {
		time.Sleep(m.openDelay)
	}
	m.openCount.Add(1)
	if m.openErr != nil {
		return nil, m.openErr
	}
	return &mockPoolClient{key: def.Key}, nil
}

// mockPoolClient is a test client.
type mockPoolClient struct {
	key       string
	closed    bool
	mu        sync.Mutex
	execCount int
}

func (c *mockPoolClient) Execute(ctx context.Context, op Operation) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execCount++
	return map[string]any{"ok": true}, nil
}

func (c *mockPoolClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func TestPool_FirstGetOpensExactlyOnce(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		DefaultPoolConfig(),
		nil,
	)
	pool.Start(context.Background())
	defer pool.Close()

	// First Get should open
	client, err := pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected client, got nil")
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected 1 open, got %d", conn.openCount.Load())
	}

	// Second Get should reuse (no new open)
	client2, err := pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("second Get failed: %v", err)
	}
	if client2 == nil {
		t.Fatal("expected client, got nil")
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected still 1 open after reuse, got %d", conn.openCount.Load())
	}
}

func TestPool_ConcurrentGetsOpenExactlyOnce(t *testing.T) {
	conn := &mockConnector{
		typ:       "test",
		lifecycle: LifecyclePooled,
		openDelay: 50 * time.Millisecond, // Slow open to ensure concurrency
	}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		DefaultPoolConfig(),
		nil,
	)
	pool.Start(context.Background())
	defer pool.Close()

	// Launch many concurrent Gets
	const goroutines = 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	clients := make(chan Client, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := pool.Get(context.Background(), "test-key")
			if err != nil {
				errs <- err
				return
			}
			clients <- c
		}()
	}
	wg.Wait()
	close(errs)
	close(clients)

	// Check for errors
	for err := range errs {
		t.Errorf("concurrent Get failed: %v", err)
	}

	// Should have opened exactly once (singleflight)
	if conn.openCount.Load() != 1 {
		t.Errorf("expected exactly 1 open with singleflight, got %d", conn.openCount.Load())
	}

	// All clients should be the same instance (wrapped)
	var firstClient Client
	for c := range clients {
		if firstClient == nil {
			firstClient = c
		} else if c != firstClient {
			t.Error("expected all Gets to return the same client instance")
		}
	}
}

func TestPool_IdleReaperClosesIdleConnections(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	// Short idle timeout and reap interval for testing
	config := PoolConfig{
		IdleTimeout:  50 * time.Millisecond,
		ReapInterval: 20 * time.Millisecond,
		MaxIdle:      10,
	}

	now := time.Now()
	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		config,
		nil,
	)
	pool.setNowFunc(func() time.Time { return now })
	pool.Start(context.Background())
	defer pool.Close()

	// Open a connection
	_, err := pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Verify it's open
	stats := pool.Stats()
	if stats.OpenConnections != 1 {
		t.Errorf("expected 1 open connection, got %d", stats.OpenConnections)
	}

	// Advance time past idle timeout
	now = now.Add(100 * time.Millisecond)
	pool.setNowFunc(func() time.Time { return now })

	// Wait for reaper to run
	time.Sleep(50 * time.Millisecond)

	// Connection should be reaped
	stats = pool.Stats()
	if stats.OpenConnections != 0 {
		t.Errorf("expected 0 open connections after reap, got %d", stats.OpenConnections)
	}

	// Next Get should reopen
	_, err = pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get after reap failed: %v", err)
	}
	if conn.openCount.Load() != 2 {
		t.Errorf("expected 2 opens (initial + after reap), got %d", conn.openCount.Load())
	}
}

func TestPool_MinWarmPreventedFromReaping(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{
		Key:  "test-key",
		Type: "test",
		Settings: map[string]any{
			"pool": map[string]any{
				"minWarm": 1,
			},
		},
	}}

	config := PoolConfig{
		IdleTimeout:  50 * time.Millisecond,
		ReapInterval: 20 * time.Millisecond,
		MaxIdle:      10,
	}

	now := time.Now()
	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		config,
		nil,
	)
	pool.setNowFunc(func() time.Time { return now })
	pool.Start(context.Background())
	defer pool.Close()

	// minWarm=1 should pre-warm the connection
	time.Sleep(10 * time.Millisecond) // Let Start complete

	stats := pool.Stats()
	if stats.OpenConnections != 1 {
		t.Errorf("expected 1 pre-warmed connection, got %d", stats.OpenConnections)
	}

	// Advance time past idle timeout
	now = now.Add(100 * time.Millisecond)
	pool.setNowFunc(func() time.Time { return now })

	// Wait for reaper to run
	time.Sleep(50 * time.Millisecond)

	// Connection should NOT be reaped because minWarm=1
	stats = pool.Stats()
	if stats.OpenConnections != 1 {
		t.Errorf("expected minWarm connection to be preserved, got %d open", stats.OpenConnections)
	}
}

func TestPool_UnknownKeyReturnsError(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		DefaultPoolConfig(),
		nil,
	)
	pool.Start(context.Background())
	defer pool.Close()

	_, err := pool.Get(context.Background(), "unknown-key")
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	var connErr *ConnError
	if !errors.As(err, &connErr) {
		t.Fatalf("expected ConnError, got %T", err)
	}
	if connErr.Class != Validation {
		t.Errorf("expected Validation class, got %v", connErr.Class)
	}
}

func TestPool_UpdateDefsEvictsChangedKeys(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test", Settings: map[string]any{"host": "old"}}}

	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		DefaultPoolConfig(),
		nil,
	)
	pool.Start(context.Background())
	defer pool.Close()

	// Open the connection
	_, err := pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected 1 open, got %d", conn.openCount.Load())
	}

	// Update with changed settings
	newDefs := []ConnectionDef{{Key: "test-key", Type: "test", Settings: map[string]any{"host": "new"}}}
	pool.UpdateDefs(newDefs)

	// Give time for async close
	time.Sleep(10 * time.Millisecond)

	// Next Get should reopen
	_, err = pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get after update failed: %v", err)
	}
	if conn.openCount.Load() != 2 {
		t.Errorf("expected 2 opens after def change, got %d", conn.openCount.Load())
	}
}

func TestPool_UpdateDefsKeepsUnchangedKeys(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test", Settings: map[string]any{"host": "same"}}}

	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		DefaultPoolConfig(),
		nil,
	)
	pool.Start(context.Background())
	defer pool.Close()

	// Open the connection
	_, err := pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected 1 open, got %d", conn.openCount.Load())
	}

	// Update with same settings (unchanged)
	newDefs := []ConnectionDef{{Key: "test-key", Type: "test", Settings: map[string]any{"host": "same"}}}
	pool.UpdateDefs(newDefs)

	// Next Get should NOT reopen
	_, err = pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get after update failed: %v", err)
	}
	if conn.openCount.Load() != 1 {
		t.Errorf("expected still 1 open (unchanged def), got %d", conn.openCount.Load())
	}
}

func TestPool_OpenErrorDoesNotBlockOtherKeys(t *testing.T) {
	goodConn := &mockConnector{typ: "good", lifecycle: LifecyclePooled}
	badConn := &mockConnector{typ: "bad", lifecycle: LifecyclePooled, openErr: errors.New("connection failed")}
	defs := []ConnectionDef{
		{Key: "good-key", Type: "good"},
		{Key: "bad-key", Type: "bad"},
	}

	pool := NewConnectionPool(
		[]Connector{goodConn, badConn},
		defs,
		nil,
		DefaultPoolConfig(),
		nil,
	)
	pool.Start(context.Background())
	defer pool.Close()

	// Good key should work
	_, err := pool.Get(context.Background(), "good-key")
	if err != nil {
		t.Fatalf("Get good-key failed: %v", err)
	}

	// Bad key should fail
	_, err = pool.Get(context.Background(), "bad-key")
	if err == nil {
		t.Fatal("expected error for bad-key")
	}

	// Good key should still work
	_, err = pool.Get(context.Background(), "good-key")
	if err != nil {
		t.Fatalf("Get good-key failed after bad-key error: %v", err)
	}
}

func TestPool_CloseStopsReaper(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{{Key: "test-key", Type: "test"}}

	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		DefaultPoolConfig(),
		nil,
	)
	pool.Start(context.Background())

	// Open a connection
	_, err := pool.Get(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	// Close should complete without hanging
	done := make(chan struct{})
	go func() {
		pool.Close()
		close(done)
	}()

	select {
	case <-done:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("Close timed out")
	}
}

func TestPool_NewOpensZeroConnections(t *testing.T) {
	conn := &mockConnector{typ: "test", lifecycle: LifecyclePooled}
	defs := []ConnectionDef{
		{Key: "key1", Type: "test"},
		{Key: "key2", Type: "test"},
		{Key: "key3", Type: "test"},
	}

	pool := NewConnectionPool(
		[]Connector{conn},
		defs,
		nil,
		DefaultPoolConfig(),
		nil,
	)
	// Don't call Start() - just test that New opens nothing

	// No connections should be opened
	if conn.openCount.Load() != 0 {
		t.Errorf("NewConnectionPool should open 0 connections, got %d", conn.openCount.Load())
	}

	stats := pool.Stats()
	if stats.OpenConnections != 0 {
		t.Errorf("expected 0 open connections, got %d", stats.OpenConnections)
	}
	if stats.TotalDefs != 3 {
		t.Errorf("expected 3 total defs, got %d", stats.TotalDefs)
	}
}
