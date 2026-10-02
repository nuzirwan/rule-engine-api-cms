package connect

import "time"

// mergePolicy produces the effective policy for one Execute by overlaying an
// optional per-node override on the connection default. The merge is
// FIELD-LEVEL and node-wins (AC-7): each override field that is non-zero
// replaces the base field; a zero override field inherits the base. A nil
// override returns the base verbatim.
//
// The breaker is intentionally NOT merged per field here as an overridable unit:
// breaker state is a property of the downstream, one per connection key (R10).
// The thin slice ships timeout-only, so only Timeout is honored at Execute; the
// Retry/Breaker fields are merged for shape so later increments can read them.
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
