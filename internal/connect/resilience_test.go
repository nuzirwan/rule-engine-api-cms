package connect

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedClient is a fake inner Client whose Execute returns errors from a
// script (one per call) and counts invocations, so a test can assert exactly how
// many inner attempts a resilience envelope made.
type scriptedClient struct {
	key   string
	calls int64
	errs  []error // errs[i] returned on call i; past the end returns nil (success)
}

func (c *scriptedClient) Execute(ctx context.Context, op Operation) (any, error) {
	n := atomic.AddInt64(&c.calls, 1)
	i := int(n - 1)
	if i < len(c.errs) {
		if c.errs[i] != nil {
			return nil, c.errs[i]
		}
	}
	return map[string]any{"ok": true}, nil
}

func (c *scriptedClient) Close() error { return nil }

// noSleep is a sleeper that advances no real time, so retry tests never block.
func noSleep(ctx context.Context, d time.Duration) error { return ctx.Err() }

// newTestClient builds a resilientClient over a scripted inner client with an
// explicit policy and the no-sleep backoff clock.
func newTestClient(key string, policy ResiliencePolicy, inner Client) *resilientClient {
	c := newResilientClient(key, inner, policy)
	c.sleep = noSleep
	return c
}

// upstream builds a retryable Upstream ConnError.
func upstream(key string) error { return newErr(Upstream, key, "", "simulated upstream failure") }

// TestRetryOnlyIdempotent proves retry replays a transient failure ONLY for an
// idempotent op: a read (query) is retried up to the attempt budget, while a
// non-idempotent write (exec) without an idempotency key is tried at most once.
func TestRetryOnlyIdempotent(t *testing.T) {
	policy := ResiliencePolicy{Timeout: time.Second}
	policy.Retry.MaxAttempts = 3

	t.Run("idempotent read retries up to budget", func(t *testing.T) {
		inner := &scriptedClient{key: "k", errs: []error{upstream("k"), upstream("k"), upstream("k")}}
		c := newTestClient("k", policy, inner)
		_, err := c.Execute(context.Background(), Operation{Kind: "query"})
		if err == nil {
			t.Fatal("expected the retried op to still fail after exhausting attempts")
		}
		if !errors.Is(err, ErrUpstream) {
			t.Fatalf("err class = %v; want Upstream", err)
		}
		if got := atomic.LoadInt64(&inner.calls); got != 3 {
			t.Fatalf("inner called %d times; want 3 (first + 2 retries)", got)
		}
	})

	t.Run("non-idempotent write is at-most-once", func(t *testing.T) {
		inner := &scriptedClient{key: "k", errs: []error{upstream("k")}}
		c := newTestClient("k", policy, inner)
		_, err := c.Execute(context.Background(), Operation{Kind: "exec"})
		if err == nil {
			t.Fatal("expected the write to fail")
		}
		if got := atomic.LoadInt64(&inner.calls); got != 1 {
			t.Fatalf("inner called %d times; want 1 (no retry for a non-idempotent write)", got)
		}
	})

	t.Run("validation error is never retried", func(t *testing.T) {
		inner := &scriptedClient{key: "k", errs: []error{newErr(Validation, "k", "query", "bad shape")}}
		c := newTestClient("k", policy, inner)
		_, err := c.Execute(context.Background(), Operation{Kind: "query"})
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("err class = %v; want Validation", err)
		}
		if got := atomic.LoadInt64(&inner.calls); got != 1 {
			t.Fatalf("inner called %d times; want 1 (validation is poison, never retried)", got)
		}
	})

	t.Run("idempotency key makes a write retryable", func(t *testing.T) {
		inner := &scriptedClient{key: "k", errs: []error{upstream("k"), upstream("k"), nil}}
		c := newTestClient("k", policy, inner)
		res, err := c.Execute(context.Background(), Operation{Kind: "exec", IdempotencyKey: "01J-ulid"})
		if err != nil {
			t.Fatalf("keyed write should have succeeded on the 3rd attempt: %v", err)
		}
		if res == nil {
			t.Fatal("expected a success result")
		}
		if got := atomic.LoadInt64(&inner.calls); got != 3 {
			t.Fatalf("inner called %d times; want 3 (keyed write retried)", got)
		}
	})
}

// TestBreakerOpensAndIsolates proves AC-6: after enough consecutive transient
// failures the per-key breaker trips; subsequent calls return ErrBreakerOpen
// WITHOUT invoking the inner client (fail fast). A SECOND connection's breaker
// stays closed and serving — one failing source is isolated from the others.
func TestBreakerOpensAndIsolates(t *testing.T) {
	// A low threshold and a single-attempt retry budget so each Execute is one
	// inner failure, deterministically tripping after `threshold` calls.
	policy := ResiliencePolicy{Timeout: time.Second}
	policy.Retry.MaxAttempts = 1 // no in-call retry; each Execute = one failure
	policy.Breaker.FailureThreshold = 3
	policy.Breaker.OpenTimeout = 50 * time.Millisecond

	// A perpetually-failing upstream for the "bad" connection.
	bad := &scriptedClient{key: "bad", errs: []error{
		upstream("bad"), upstream("bad"), upstream("bad"),
		upstream("bad"), upstream("bad"), upstream("bad"),
	}}
	badClient := newTestClient("bad", policy, bad)

	// A healthy second connection with its OWN breaker.
	good := &scriptedClient{key: "good"}
	goodClient := newTestClient("good", policy, good)

	ctx := context.Background()

	// Drive the bad connection to its failure threshold (reads so the breaker
	// counts them, but with MaxAttempts=1 each Execute is a single inner call).
	for i := 0; i < 3; i++ {
		if _, err := badClient.Execute(ctx, Operation{Kind: "query"}); err == nil {
			t.Fatalf("call %d: expected a failure", i)
		}
	}
	callsAtTrip := atomic.LoadInt64(&bad.calls)

	// The breaker is now open: the next call must fail fast as ErrBreakerOpen
	// WITHOUT touching the inner client.
	_, err := badClient.Execute(ctx, Operation{Kind: "query"})
	if err == nil {
		t.Fatal("expected ErrBreakerOpen after the breaker tripped")
	}
	if !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("err = %v; want ErrBreakerOpen", err)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("ErrBreakerOpen should classify as Upstream; got %v", err)
	}
	if got := atomic.LoadInt64(&bad.calls); got != callsAtTrip {
		t.Fatalf("inner was called while the breaker was open (%d -> %d); want no inner call", callsAtTrip, got)
	}

	// ISOLATION: the healthy connection's breaker is unaffected and still serves.
	if _, err := goodClient.Execute(ctx, Operation{Kind: "query"}); err != nil {
		t.Fatalf("healthy connection failed while a different connection's breaker was open: %v", err)
	}
	if got := atomic.LoadInt64(&good.calls); got != 1 {
		t.Fatalf("healthy inner called %d times; want 1 (its breaker is closed and serving)", got)
	}

	// RECOVERY: after the open timeout a half-open probe is admitted. Point the
	// bad inner at success now and wait out the cooldown; the probe closes it.
	bad.errs = nil
	time.Sleep(60 * time.Millisecond)
	if _, err := badClient.Execute(ctx, Operation{Kind: "query"}); err != nil {
		t.Fatalf("half-open probe should have been admitted and succeeded: %v", err)
	}
}

// fakeDedupStore is an in-memory DedupStore for unit tests: Acquire is a map
// SET-NX, Release is a delete. A failmode makes Acquire error so the pessimistic
// posture can be asserted.
type fakeDedupStore struct {
	held     map[string]bool
	acquireN int64
	releaseN int64
	failAcq  bool
}

func newFakeDedupStore() *fakeDedupStore { return &fakeDedupStore{held: map[string]bool{}} }

func (s *fakeDedupStore) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	atomic.AddInt64(&s.acquireN, 1)
	if s.failAcq {
		return false, errors.New("dedup store down")
	}
	if s.held[key] {
		return false, nil
	}
	s.held[key] = true
	return true, nil
}

func (s *fakeDedupStore) Release(ctx context.Context, key string) error {
	atomic.AddInt64(&s.releaseN, 1)
	delete(s.held, key)
	return nil
}

// TestIdempotencyDedupLock proves the R4 §6.2 mechanism: an opted-in
// non-idempotent write takes a SET-NX lock; a failing guarded write RELEASES the
// lock so a retry re-attempts; a second concurrent writer holding the lock is
// deduped; and a dedup-store error BLOCKS the write (pessimistic).
func TestIdempotencyDedupLock(t *testing.T) {
	policy := ResiliencePolicy{Timeout: time.Second}
	policy.Retry.MaxAttempts = 3

	t.Run("lock released on failure, retry re-attempts and succeeds", func(t *testing.T) {
		store := newFakeDedupStore()
		inner := &scriptedClient{key: "k", errs: []error{upstream("k"), nil}}
		c := newTestClient("k", policy, inner)
		c.dedup = newDedupGuard(store, time.Second)

		res, err := c.Execute(context.Background(), Operation{Kind: "exec", IdempotencyKey: "idem-1"})
		if err != nil {
			t.Fatalf("keyed write should succeed on retry: %v", err)
		}
		if res == nil {
			t.Fatal("expected a success result")
		}
		if atomic.LoadInt64(&inner.calls) != 2 {
			t.Fatalf("inner called %d times; want 2 (fail then retry)", inner.calls)
		}
		// Acquire runs each attempt; the first attempt's failure released the lock.
		if atomic.LoadInt64(&store.releaseN) != 1 {
			t.Fatalf("release count = %d; want 1 (released once on the failed attempt)", store.releaseN)
		}
	})

	t.Run("second writer holding the lock is deduped (skip)", func(t *testing.T) {
		store := newFakeDedupStore()
		store.held["nzr:dedup:k:exec:idem-dup"] = true // another writer already holds it
		inner := &scriptedClient{key: "k"}
		c := newTestClient("k", policy, inner)
		c.dedup = newDedupGuard(store, time.Second)

		res, err := c.Execute(context.Background(), Operation{Kind: "exec", IdempotencyKey: "idem-dup"})
		if err != nil {
			t.Fatalf("a deduped write should return a skip result, not an error: %v", err)
		}
		m, ok := res.(map[string]any)
		if !ok || m["deduped"] != true {
			t.Fatalf("expected a deduped:true result; got %v", res)
		}
		if atomic.LoadInt64(&inner.calls) != 0 {
			t.Fatalf("inner called %d times; want 0 (first writer owns the effect)", inner.calls)
		}
	})

	t.Run("dedup store error blocks the write (pessimistic)", func(t *testing.T) {
		store := newFakeDedupStore()
		store.failAcq = true
		inner := &scriptedClient{key: "k"}
		c := newTestClient("k", policy, inner)
		c.dedup = newDedupGuard(store, time.Second)

		_, err := c.Execute(context.Background(), Operation{Kind: "exec", IdempotencyKey: "idem-x"})
		if err == nil {
			t.Fatal("a dedup-store error must block the write, not pass it through")
		}
		if !errors.Is(err, ErrUpstream) {
			t.Fatalf("blocked-write error class = %v; want Upstream", err)
		}
		if atomic.LoadInt64(&inner.calls) != 0 {
			t.Fatalf("inner called %d times; want 0 (write blocked on lock failure)", inner.calls)
		}
	})

	t.Run("natural idempotent write does not take the lock", func(t *testing.T) {
		store := newFakeDedupStore()
		inner := &scriptedClient{key: "k"}
		c := newTestClient("k", policy, inner)
		c.dedup = newDedupGuard(store, time.Second)

		// A valkey set is naturally idempotent; even with a key present it must
		// not consume a dedup lock (the lock is only for non-idempotent writes).
		if _, err := c.Execute(context.Background(), Operation{Kind: "set", IdempotencyKey: "idem-set"}); err != nil {
			t.Fatalf("set failed: %v", err)
		}
		if atomic.LoadInt64(&store.acquireN) != 0 {
			t.Fatalf("acquire count = %d; want 0 (natural idempotent write skips the lock)", store.acquireN)
		}
	})
}
