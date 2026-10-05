package httpapi

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"nzr-rules-engine/internal/auth"
)

// RateLimitConfig controls the rate-limiting behaviour. A zero-valued config
// (RPS == 0) disables rate limiting entirely.
type RateLimitConfig struct {
	// RPS is the sustained token-replenishment rate (requests per second, float).
	// 0 or negative disables rate limiting.
	RPS float64
	// Burst is the token-bucket capacity — the maximum spike the bucket absorbs
	// before it begins denying requests. Promoted to 1 when RPS > 0 and Burst < 1.
	Burst int
	// ByToken, when true, keys the bucket by the Bearer token rather than by
	// client IP. Falls back to IP when no token is present in the request.
	ByToken bool
}

// bucket is a single token-bucket state. All access is guarded by
// rateLimiter.mu so the struct needs no lock of its own.
type bucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter holds per-key token buckets and the shared configuration.
// It is safe for concurrent use.
type rateLimiter struct {
	cfg     RateLimitConfig
	mu      sync.Mutex
	buckets map[string]*bucket
}

// newRateLimiter returns a *rateLimiter ready for use, or nil when rate
// limiting is disabled (cfg.RPS ≤ 0). A Burst < 1 is promoted to 1 so
// every key can handle at least one immediate request.
func newRateLimiter(cfg RateLimitConfig) *rateLimiter {
	if cfg.RPS <= 0 {
		return nil
	}
	if cfg.Burst < 1 {
		cfg.Burst = 1
	}
	return &rateLimiter{
		cfg:     cfg,
		buckets: make(map[string]*bucket),
	}
}

// allow consumes one token for key and reports whether the request is within
// the rate limit. Tokens refill continuously at cfg.RPS per second, capped at
// cfg.Burst (continuous token-bucket algorithm).
//
// A key not yet seen starts with a full bucket (Burst tokens) so the first
// burst of requests from a new IP/token is always allowed.
func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok {
		// Brand-new key: start at full capacity.
		b = &bucket{tokens: float64(rl.cfg.Burst), last: now}
		rl.buckets[key] = b
	}

	// Refill proportionally to wall-clock time elapsed.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = math.Min(b.tokens+elapsed*rl.cfg.RPS, float64(rl.cfg.Burst))
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// retryAfterSeconds returns the number of seconds the caller should wait
// before retrying (rounded up to the nearest integer, minimum 1).
func (rl *rateLimiter) retryAfterSeconds(key string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[key]
	if !ok || rl.cfg.RPS <= 0 {
		return 1
	}
	need := 1.0 - b.tokens // additional tokens required for one request
	if need <= 0 {
		return 1
	}
	secs := int(math.Ceil(need / rl.cfg.RPS))
	if secs < 1 {
		secs = 1
	}
	return secs
}

// rateLimitMiddleware returns an http.Handler middleware that enforces the
// per-key token-bucket limit. When rl is nil the middleware is a transparent
// no-op (rate limiting disabled). On a denied request it writes 429 with a
// Retry-After header indicating the soonest the caller may retry.
func rateLimitMiddleware(rl *rateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if rl == nil {
			return next // rate limiting disabled
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := limitKey(r, rl.cfg.ByToken)
			if !rl.allow(key) {
				retry := strconv.Itoa(rl.retryAfterSeconds(key))
				w.Header().Set("Retry-After", retry)
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// limitKey returns the rate-limit bucket key for r. When byToken is true and
// a Bearer token is present the raw token value becomes the key. Otherwise the
// client IP is used. A "type:" prefix keeps IP and token namespaces disjoint.
func limitKey(r *http.Request, byToken bool) string {
	if byToken {
		if tok, ok := auth.BearerToken(r); ok {
			return "token:" + tok
		}
	}
	return "ip:" + clientIP(r)
}

// clientIP extracts the best-effort client IP for r. It reads the first
// (client-supplied) entry from X-Forwarded-For when present — set by a trusted
// reverse proxy — and falls back to the TCP remote address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For: client, proxy1, proxy2 — take the leftmost.
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
