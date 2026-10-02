package connect

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeConnector builds fakeClients and counts how many times Open is called per
// key so a test can prove one pool is built per connection key (AC-5).
type fakeConnector struct {
	typ   string
	opens map[string]*int64 // key -> open count
}

func newFakeConnector(typ string) *fakeConnector {
	return &fakeConnector{typ: typ, opens: map[string]*int64{}}
}

func (f *fakeConnector) Type() string { return f.typ }

func (f *fakeConnector) Open(ctx context.Context, def ConnectionDef) (Client, error) {
	c, ok := f.opens[def.Key]
	if !ok {
		var n int64
		c = &n
		f.opens[def.Key] = c
	}
	atomic.AddInt64(c, 1)
	return &fakeClient{key: def.Key}, nil
}

func (f *fakeConnector) openCount(key string) int64 {
	if c, ok := f.opens[key]; ok {
		return atomic.LoadInt64(c)
	}
	return 0
}

// fakeClient records executions and can simulate a slow op so a timeout override
// is observable by elapsed time.
type fakeClient struct {
	key      string
	execs    int64
	sleep    time.Duration // when >0, Execute blocks until ctx expires or sleep elapses
	closed   bool
	execKind string
}

func (c *fakeClient) Execute(ctx context.Context, op Operation) (any, error) {
	atomic.AddInt64(&c.execs, 1)
	c.execKind = op.Kind
	if op.Kind == "nope" {
		return nil, NewConnError(Validation, c.key, op.Kind, "unsupported kind", nil)
	}
	if c.sleep > 0 {
		select {
		case <-time.After(c.sleep):
			return map[string]any{"ok": true}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return map[string]any{"ok": true}, nil
}

func (c *fakeClient) Close() error {
	c.closed = true
	return nil
}

func newTestRegistry(t *testing.T, conn Connector, defs ...ConnectionDef) *registry {
	t.Helper()
	r, err := New([]Connector{conn}, defs, NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("New registry: %v", err)
	}
	return r
}

// TestClientPointerIdentity proves one pooled client per key (AC-5): N Client
// calls return the SAME instance and Open was called exactly once for the key.
func TestClientPointerIdentity(t *testing.T) {
	fc := newFakeConnector("fake")
	r := newTestRegistry(t, fc, ConnectionDef{Key: "k1", Type: "fake"})

	ctx := context.Background()
	first, err := r.Client(ctx, "k1")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	for i := 0; i < 5; i++ {
		got, err := r.Client(ctx, "k1")
		if err != nil {
			t.Fatalf("Client #%d: %v", i, err)
		}
		if got != first {
			t.Fatalf("Client #%d returned a different instance; want pointer identity", i)
		}
	}
	if n := fc.openCount("k1"); n != 1 {
		t.Fatalf("Open called %d times for k1; want exactly 1 (one pool per key)", n)
	}
}

// TestUnknownKeyIsValidation proves an unknown key is a Validation error.
func TestUnknownKeyIsValidation(t *testing.T) {
	fc := newFakeConnector("fake")
	r := newTestRegistry(t, fc, ConnectionDef{Key: "k1", Type: "fake"})

	_, err := r.Client(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown key error class = %v; want Validation", err)
	}
}

// TestUnsupportedKindIsValidation proves an unsupported op kind surfaces as a
// Validation error through the resilient client.
func TestUnsupportedKindIsValidation(t *testing.T) {
	fc := newFakeConnector("fake")
	r := newTestRegistry(t, fc, ConnectionDef{Key: "k1", Type: "fake"})

	c, _ := r.Client(context.Background(), "k1")
	_, err := c.Execute(context.Background(), Operation{Kind: "nope"})
	if err == nil {
		t.Fatal("expected an error for an unsupported kind")
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("unsupported kind error class = %v; want Validation", err)
	}
}

// TestTimeoutOverrideWins proves a node-level timeout override beats the
// connection default (AC-7): against a slow fake, a 50ms override fails fast
// while a no-override op inherits the generous connection default and succeeds.
func TestTimeoutOverrideWins(t *testing.T) {
	fc := &fakeConnector{typ: "slow", opens: map[string]*int64{}}
	// Wrap Open to return a slow client.
	r, err := New([]Connector{slowConnector{}}, []ConnectionDef{{
		Key:        "slow",
		Type:       "slow",
		Resilience: ResiliencePolicy{Timeout: 2 * time.Second},
	}}, NewEnvSecretProvider(), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = fc

	c, _ := r.Client(context.Background(), "slow")

	// With a 50ms override against a 500ms inner op, Execute must fail ~50ms.
	start := time.Now()
	_, err = c.Execute(context.Background(), Operation{
		Kind:     "query",
		Override: &ResiliencePolicy{Timeout: 50 * time.Millisecond},
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error with the 50ms override")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("override error class = %v; want Timeout", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("override op took %v; expected ~50ms (override should win)", elapsed)
	}

	// With NO override, the 2s connection default holds and the 500ms op succeeds.
	start = time.Now()
	_, err = c.Execute(context.Background(), Operation{Kind: "query"})
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("no-override op failed: %v (default 2s should allow a 500ms op)", err)
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("no-override op took %v; expected ~500ms (default held)", elapsed)
	}
}

// slowConnector builds a client whose Execute blocks ~500ms unless ctx expires.
type slowConnector struct{}

func (slowConnector) Type() string { return "slow" }
func (slowConnector) Open(ctx context.Context, def ConnectionDef) (Client, error) {
	return &fakeClient{key: def.Key, sleep: 500 * time.Millisecond}, nil
}

// TestReloadReconciles proves Reload adds, keeps unchanged (no re-open), and
// removes keys, closing the removed client.
func TestReloadReconciles(t *testing.T) {
	fc := newFakeConnector("fake")
	r := newTestRegistry(t, fc,
		ConnectionDef{Key: "keep", Type: "fake"},
		ConnectionDef{Key: "drop", Type: "fake"},
	)
	ctx := context.Background()

	keepBefore, _ := r.Client(ctx, "keep")
	dropClient, _ := r.Client(ctx, "drop")

	err := r.Reload(ctx, []ConnectionDef{
		{Key: "keep", Type: "fake"},
		{Key: "add", Type: "fake"},
	})
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// unchanged key keeps the same warm instance (no re-open).
	keepAfter, _ := r.Client(ctx, "keep")
	if keepAfter != keepBefore {
		t.Fatal("unchanged key was re-opened; want the warm pool kept")
	}
	if n := fc.openCount("keep"); n != 1 {
		t.Fatalf("keep opened %d times; want 1 (unchanged key not re-opened)", n)
	}

	// added key is now resolvable.
	if _, err := r.Client(ctx, "add"); err != nil {
		t.Fatalf("added key not resolvable: %v", err)
	}

	// removed key is gone and its client was closed.
	if _, err := r.Client(ctx, "drop"); !errors.Is(err, ErrValidation) {
		t.Fatalf("removed key still resolvable (err=%v)", err)
	}
	if dc, ok := dropClient.(*resilientClient); ok {
		if fcInner, ok := dc.inner.(*fakeClient); ok && !fcInner.closed {
			t.Fatal("removed client was not closed")
		}
	}
}

// TestMergePolicyFieldLevel proves field-level merge: a node override of only
// Timeout keeps the base retry/breaker; a nil override returns the base verbatim.
func TestMergePolicyFieldLevel(t *testing.T) {
	base := ResiliencePolicy{Timeout: 5 * time.Second}
	base.Retry.MaxAttempts = 3
	base.Breaker.FailureThreshold = 7

	got := mergePolicy(base, &ResiliencePolicy{Timeout: 50 * time.Millisecond})
	if got.Timeout != 50*time.Millisecond {
		t.Fatalf("merged timeout = %v; want 50ms (override wins)", got.Timeout)
	}
	if got.Retry.MaxAttempts != 3 {
		t.Fatalf("merged retry = %d; want 3 (inherited)", got.Retry.MaxAttempts)
	}
	if got.Breaker.FailureThreshold != 7 {
		t.Fatalf("merged breaker threshold = %d; want 7 (inherited)", got.Breaker.FailureThreshold)
	}

	same := mergePolicy(base, nil)
	if same != base {
		t.Fatalf("nil override changed the base policy: %+v", same)
	}
}
