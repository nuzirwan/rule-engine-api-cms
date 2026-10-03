package connect

import (
	"context"
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/sony/gobreaker/v2"
)

// mergePolicy produces the effective policy for one Execute by overlaying an
// optional per-node override on the connection default. The merge is
// FIELD-LEVEL and node-wins (AC-7): each override field that is non-zero
// replaces the base field; a zero override field inherits the base. A nil
// override returns the base verbatim.
//
// The breaker is intentionally NOT merged per field here as an overridable unit:
// breaker state is a property of the downstream, one per connection key (R10).
// Timeout and Retry are honored per Execute from the merged policy; the Breaker
// fields are merged for shape but the live breaker instance is built once per key
// at construction and is NOT re-created from an override (§4.5).
func mergePolicy(base ResiliencePolicy, override *ResiliencePolicy) ResiliencePolicy {
	out := base
	if override == nil {
		return out
	}
	if override.Timeout != 0 {
		out.Timeout = override.Timeout
	}
	if override.Retry.MaxAttempts != 0 {
		out.Retry.MaxAttempts = override.Retry.MaxAttempts
	}
	if override.Retry.BaseBackoff != 0 {
		out.Retry.BaseBackoff = override.Retry.BaseBackoff
	}
	if override.Retry.MaxBackoff != 0 {
		out.Retry.MaxBackoff = override.Retry.MaxBackoff
	}
	if override.Breaker.FailureThreshold != 0 {
		out.Breaker.FailureThreshold = override.Breaker.FailureThreshold
	}
	if override.Breaker.FailureRatio != 0 {
		out.Breaker.FailureRatio = override.Breaker.FailureRatio
	}
	if override.Breaker.OpenTimeout != 0 {
		out.Breaker.OpenTimeout = override.Breaker.OpenTimeout
	}
	return out
}

// defaultTimeout is the fallback applied when neither the connection default nor
// the node override sets a timeout. It is deliberately aggressive, not 60s
// (retry-and-backoff: a 60s default is almost always too high).
const defaultTimeout = 5 * time.Second

// effectiveTimeout returns the timeout to apply, falling back to defaultTimeout
// when the merged policy leaves it unset.
func effectiveTimeout(p ResiliencePolicy) time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return defaultTimeout
}

// Retry defaults applied when the merged policy leaves a field zero. They are
// deliberately modest: a few attempts with a short base and a capped ceiling so
// a transient blip is smoothed without hammering a struggling upstream.
const (
	// defaultMaxAttempts is the total number of attempts (first try + retries).
	defaultMaxAttempts = 3
	// defaultBaseBackoff is the first backoff step before full jitter.
	defaultBaseBackoff = 20 * time.Millisecond
	// defaultMaxBackoff caps the computed backoff before full jitter.
	defaultMaxBackoff = 2 * time.Second
)

// effectiveMaxAttempts returns the retry attempt budget, falling back to the
// default when the merged policy leaves it unset. A value below 1 is treated as
// a single attempt (at-most-once).
func effectiveMaxAttempts(p ResiliencePolicy) int {
	if p.Retry.MaxAttempts > 0 {
		return p.Retry.MaxAttempts
	}
	return defaultMaxAttempts
}

// effectiveBaseBackoff returns the base backoff, falling back to the default.
func effectiveBaseBackoff(p ResiliencePolicy) time.Duration {
	if p.Retry.BaseBackoff > 0 {
		return p.Retry.BaseBackoff
	}
	return defaultBaseBackoff
}

// effectiveMaxBackoff returns the backoff ceiling, falling back to the default.
func effectiveMaxBackoff(p ResiliencePolicy) time.Duration {
	if p.Retry.MaxBackoff > 0 {
		return p.Retry.MaxBackoff
	}
	return defaultMaxBackoff
}

// isIdempotent reports whether op is safe to retry (slice §4.4/§6.2). An op is
// idempotent when it is naturally idempotent, OR the author supplied an
// IdempotencyKey making an otherwise-unsafe write replay-safe (R4). Everything
// else (exec, HTTP POST/PATCH without a key) is at-most-once.
func isIdempotent(op Operation) bool {
	if op.IdempotencyKey != "" {
		return true
	}
	return isNaturallyIdempotent(op)
}

// isNaturallyIdempotent reports whether op is replay-safe WITHOUT an idempotency
// key: a read/natural write by kind (query/get/set/del/ping), or an HTTP op whose
// method is GET/PUT/DELETE. A blind exec write or an HTTP POST/PATCH is NOT
// naturally idempotent — those only become retryable via an IdempotencyKey, and
// only those take the dedup lock (§6.2).
func isNaturallyIdempotent(op Operation) bool {
	switch op.Kind {
	case "query", "get", "set", "del", "ping":
		return true
	case "http":
		switch strings.ToUpper(httpMethod(op)) {
		case "GET", "PUT", "DELETE":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// httpMethod reads the HTTP method from an op payload, matching the rest driver
// (payload key "method"); an empty/missing method defaults to GET as the driver
// does.
func httpMethod(op Operation) string {
	if op.Payload == nil {
		return "GET"
	}
	if m, ok := op.Payload["method"].(string); ok && m != "" {
		return m
	}
	return "GET"
}

// isRetryable reports whether err is a transient class worth retrying. Only
// Timeout and Upstream are transient; Validation/NotFound/Internal are not
// (slice §8). ErrBreakerOpen classifies as Upstream but is never produced inside
// the retry loop (the breaker is OUTER), so it is not retried in-call.
func isRetryable(err error) bool {
	switch classOf(err) {
	case Timeout, Upstream:
		return true
	default:
		return false
	}
}

// sleeper is the injectable clock seam for backoff waits. A sleeper blocks for d
// or until ctx is cancelled, returning ctx.Err() on cancel and nil otherwise.
// Unit tests inject a fake that advances no real time so retry tests never
// sleep for real.
type sleeper func(ctx context.Context, d time.Duration) error

// realSleep is the default sleeper: it waits for d or ctx cancellation.
func realSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoff computes the wait before a given retry attempt (0-based): the
// exponential step min(max, base*2^attempt) with FULL jitter applied — a random
// duration in [0, step]. Full jitter spreads retries so a fleet does not
// stampede a recovering upstream in lockstep.
func backoff(attempt int, base, max time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	step := base
	for i := 0; i < attempt && step < max; i++ {
		step *= 2
	}
	if step > max {
		step = max
	}
	if step <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(step) + 1))
}

// retry runs fn under the policy's retry budget. It calls fn once, then while
// attempts remain AND the op is idempotent AND the last error is retryable, it
// sleeps the jittered backoff (through the injectable sleeper, cancellable by
// ctx) and calls fn again. A non-idempotent or non-retryable error returns
// immediately (at-most-once). A cancelled sleep returns the ctx error without a
// further attempt. The last result/error is returned.
func retry(ctx context.Context, policy ResiliencePolicy, idempotent bool, sleep sleeper, fn func() (any, error)) (any, error) {
	maxAttempts := effectiveMaxAttempts(policy)
	base := effectiveBaseBackoff(policy)
	max := effectiveMaxBackoff(policy)

	res, err := fn()
	for attempt := 1; attempt < maxAttempts; attempt++ {
		if err == nil || !idempotent || !isRetryable(err) {
			return res, err
		}
		if serr := sleep(ctx, backoff(attempt, base, max)); serr != nil {
			return res, serr
		}
		res, err = fn()
	}
	return res, err
}

// Breaker defaults applied when the merged policy leaves a breaker field zero.
// They follow retry-and-backoff: trip on sustained failure, cool down, then
// probe. The defaults are modest so a short blip does not open the circuit.
const (
	// defaultBreakerFailureThreshold is the consecutive-failure count that trips
	// the breaker when FailureRatio is unset.
	defaultBreakerFailureThreshold uint32 = 5
	// defaultBreakerOpenTimeout is the open-state cooldown before a half-open probe.
	defaultBreakerOpenTimeout = 10 * time.Second
	// breakerMinRequests is the minimum sampled requests before a failure-ratio
	// trip is considered, so a single early failure can't open the circuit.
	breakerMinRequests uint32 = 3
	// breakerHalfOpenMaxRequests is how many probe requests the half-open state
	// admits before deciding to close or re-open.
	breakerHalfOpenMaxRequests uint32 = 1
)

// newBreaker builds the per-key circuit breaker (sony/gobreaker) from the
// connection's breaker policy. The breaker is PER-INSTANCE and PER-KEY (R10):
// one breaker per connection key, built once at construction and never re-created
// from a per-node override — breaker state is a property of the downstream, not
// the call site (§4.3, §4.5). A context cancellation / deadline is EXCLUDED from
// the breaker's accounting so a caller abandoning a request does not trip the
// circuit; only genuine downstream failures (Timeout/Upstream) count.
func newBreaker(key string, p ResiliencePolicy) *gobreaker.CircuitBreaker[any] {
	threshold := p.Breaker.FailureThreshold
	if threshold == 0 {
		threshold = defaultBreakerFailureThreshold
	}
	ratio := p.Breaker.FailureRatio
	openTimeout := p.Breaker.OpenTimeout
	if openTimeout == 0 {
		openTimeout = defaultBreakerOpenTimeout
	}
	return gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
		Name:        key,
		MaxRequests: breakerHalfOpenMaxRequests,
		Timeout:     openTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			if c.ConsecutiveFailures >= threshold {
				return true
			}
			if ratio > 0 && c.Requests >= breakerMinRequests {
				return float64(c.TotalFailures)/float64(c.Requests) >= ratio
			}
			return false
		},
		IsSuccessful: func(err error) bool {
			// Only transient downstream failures count against the breaker; a
			// Validation/NotFound (bad input, missing key) is not a downstream
			// health signal and must not trip the circuit.
			if err == nil {
				return true
			}
			return !isRetryable(err)
		},
		IsExcluded: func(err error) bool {
			// A caller-driven cancellation is neither a success nor a failure.
			return errors.Is(err, context.Canceled)
		},
	})
}

// isBreakerOpen reports whether err is the breaker's open/half-open rejection
// (gobreaker.ErrOpenState / ErrTooManyRequests), so the client can surface it as
// an Upstream ErrBreakerOpen without the inner call having run.
func isBreakerOpen(err error) bool {
	return errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests)
}
