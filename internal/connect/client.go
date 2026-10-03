package connect

import (
	"context"
	"errors"

	"github.com/sony/gobreaker/v2"
)

// resilientClient is the OUTER client stored by the registry and handed to every
// caller for a key. It wraps the inner driver Client with the full resilience
// envelope composed OUTER -> INNER as timeout -> breaker -> retry -> inner
// (slice-b-connections.md §4.1):
//
//	Execute(op):
//	  policy ← merge(connectionPolicy, op.Override)      // per-node override wins (AC-7)
//	  ctx    ← WithTimeout(ctx, policy.Timeout)          // (1) bound the WHOLE budget
//	  breaker.Execute(func() {                           // (2) per-key circuit breaker (R10)
//	    retry(policy, idempotent, func() {               // (3) retry transient+idempotent only
//	      inner.Execute(ctx, op)                         //     innermost raw I/O
//	    })
//	  })
//
// The timeout is OUTERMOST so retries cannot exceed the deadline; the breaker
// wraps retry so a tripped circuit short-circuits before any attempt and so
// retry failures count toward tripping it; retry is innermost and is a no-op for
// non-idempotent or non-transient work.
type resilientClient struct {
	key     string
	inner   Client
	policy  ResiliencePolicy               // connection default (ConnectionDef.Resilience)
	breaker *gobreaker.CircuitBreaker[any] // ONE per key, per-instance (R10); not node-overridable
	sleep   sleeper                        // injectable backoff clock (real in prod, fake in tests)
	dedup   *dedupGuard                    // idempotency-key dedup lock for non-idempotent writes (R4)
}

// newResilientClient wraps an inner driver client with the connection-default
// policy, a per-key breaker, and the real (cancellable) backoff sleeper.
func newResilientClient(key string, inner Client, policy ResiliencePolicy) *resilientClient {
	return &resilientClient{
		key:     key,
		inner:   inner,
		policy:  policy,
		breaker: newBreaker(key, policy),
		sleep:   realSleep,
	}
}

// Execute applies the resilience envelope around the inner driver call. See the
// type doc for the composition order. A merged-policy timeout bounds the whole
// operation (including retries); the per-key breaker short-circuits a sustained
// outage; retry replays only transient failures of idempotent operations.
func (c *resilientClient) Execute(ctx context.Context, op Operation) (any, error) {
	policy := mergePolicy(c.policy, op.Override)
	timeout := effectiveTimeout(policy)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	idempotent := isIdempotent(op)

	// (2) per-key breaker wraps (3) retry wraps the inner call.
	res, err := c.breaker.Execute(func() (any, error) {
		return retry(ctx, policy, idempotent, c.sleep, func() (any, error) {
			return c.callInner(ctx, op)
		})
	})
	if err != nil {
		if isBreakerOpen(err) {
			// The circuit short-circuited before any inner call; surface a
			// classified Upstream error (not retried in-call — the breaker owns
			// recovery via its half-open probe).
			return nil, breakerOpenErr(c.key, op.Kind)
		}
		return nil, c.classify(op.Kind, err)
	}
	return res, nil
}

// callInner runs one inner driver attempt, optionally guarded by an idempotency
// dedup lock for an opted-in non-idempotent write (§6.2). The lock is acquired
// before the write and RELEASED if the guarded write fails, so a legitimate
// retry can re-attempt; a lock that is already held (first-writer-wins) skips the
// write and reports a dedup short-circuit. The dedup store failure posture is
// PESSIMISTIC: a lock-store error blocks the write rather than risking a
// duplicate (idempotency-and-dedup).
func (c *resilientClient) callInner(ctx context.Context, op Operation) (any, error) {
	if c.dedup != nil && op.IdempotencyKey != "" && !isNaturallyIdempotent(op) {
		return c.dedup.guard(ctx, c.key, op, func() (any, error) {
			return c.inner.Execute(ctx, op)
		})
	}
	return c.inner.Execute(ctx, op)
}

// Close closes the inner driver client (and its pool).
func (c *resilientClient) Close() error { return c.inner.Close() }

// classify ensures an error leaving the client is a *ConnError. A deadline that
// expired is reported as Timeout even when the inner driver returned a bare
// context error; an already-classified ConnError passes through with its key
// filled in.
func (c *resilientClient) classify(opKind string, err error) error {
	if err == nil {
		return nil
	}
	var ce *ConnError
	if errors.As(err, &ce) {
		if ce.Key == "" {
			ce.Key = c.key
		}
		if ce.Op == "" {
			ce.Op = opKind
		}
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return wrapErr(Timeout, c.key, opKind, "operation deadline exceeded", err)
	}
	return wrapErr(classOf(err), c.key, opKind, "execute failed", err)
}
