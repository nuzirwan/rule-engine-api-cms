package gateway

import (
	"time"

	"github.com/sony/gobreaker/v2"

	"nzr-rules-engine/internal/worker"
)

// BreakerConfig holds configuration for the per-group circuit breaker.
type BreakerConfig struct {
	// FailureThreshold is the consecutive-failure count that trips the breaker.
	// Defaults to 5 if zero.
	FailureThreshold uint32
	// OpenTimeout is the open-state cooldown before a half-open probe.
	// Defaults to 10s if zero.
	OpenTimeout time.Duration
}

// Default breaker configuration values matching the resilience pattern in
// internal/connect/resilience.go.
const (
	defaultBreakerFailureThreshold uint32 = 5
	defaultBreakerOpenTimeout             = 10 * time.Second
	breakerHalfOpenMaxRequests     uint32 = 1
)

// newGroupBreaker builds a circuit breaker for a specific worker group.
// It follows the same pattern as internal/connect/resilience.go newBreaker:
// - Trips on consecutive failures (threshold) OR failure ratio
// - Half-open state admits one probe request before deciding
// - Context cancellation is excluded from failure accounting
func newGroupBreaker(group string, cfg BreakerConfig) *gobreaker.CircuitBreaker[*worker.ExecuteResponse] {
	threshold := cfg.FailureThreshold
	if threshold == 0 {
		threshold = defaultBreakerFailureThreshold
	}
	openTimeout := cfg.OpenTimeout
	if openTimeout == 0 {
		openTimeout = defaultBreakerOpenTimeout
	}

	return gobreaker.NewCircuitBreaker[*worker.ExecuteResponse](gobreaker.Settings{
		Name:        "worker-" + group,
		MaxRequests: breakerHalfOpenMaxRequests,
		Timeout:     openTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			// Trip when consecutive failures exceed threshold.
			return c.ConsecutiveFailures >= threshold
		},
		IsSuccessful: func(err error) bool {
			// nil error is a success.
			return err == nil
		},
	})
}
