package connect

import (
	"context"
	"errors"
)

// resilientClient is the OUTER client stored by the registry and handed to every
// caller for a key. It wraps the inner driver Client with the resilience
// envelope. The thin slice applies the timeout as the OUTERMOST wrap (via
// context.WithTimeout) and honors Operation.Override for it; retry and breaker
// are minimal/stubbed with a clear TODO — they ship in a later increment.
type resilientClient struct {
	key    string
	inner  Client
	policy ResiliencePolicy // connection default (ConnectionDef.Resilience)
}

// newResilientClient wraps an inner driver client with a connection-default policy.
func newResilientClient(key string, inner Client, policy ResiliencePolicy) *resilientClient {
	return &resilientClient{key: key, inner: inner, policy: policy}
}

// Execute merges the connection default with the operation's per-node override
// (node wins, AC-7), applies the merged timeout as the outermost context bound,
// calls the inner driver, and returns a classified error.
//
// TODO(resilience): the thin slice ships timeout-only. Retry (idempotent ops,
// capped jittered backoff) and a per-key circuit breaker (sony/gobreaker) wrap
// the inner call here in a later increment — composition order timeout -> breaker
// -> retry -> inner (slice-b-connections.md §4.1). The breaker is per-key, not
// per-node override.
func (c *resilientClient) Execute(ctx context.Context, op Operation) (any, error) {
	policy := mergePolicy(c.policy, op.Override)
	timeout := effectiveTimeout(policy)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := c.inner.Execute(ctx, op)
	if err != nil {
		return nil, c.classify(op.Kind, err)
	}
	return res, nil
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
