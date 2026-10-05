package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---- newRateLimiter --------------------------------------------------------

func TestNewRateLimiter_Disabled(t *testing.T) {
	if rl := newRateLimiter(RateLimitConfig{RPS: 0}); rl != nil {
		t.Fatalf("expected nil for disabled config, got %+v", rl)
	}
	if rl := newRateLimiter(RateLimitConfig{RPS: -1}); rl != nil {
		t.Fatalf("expected nil for negative RPS, got %+v", rl)
	}
}

func TestNewRateLimiter_BurstFloor(t *testing.T) {
	// Burst < 1 should be promoted to 1.
	rl := newRateLimiter(RateLimitConfig{RPS: 10, Burst: 0})
	if rl.cfg.Burst != 1 {
		t.Fatalf("Burst = %d, want 1", rl.cfg.Burst)
	}
}

// ---- allow -----------------------------------------------------------------

func TestAllow_FirstBurstAllowed(t *testing.T) {
	rl := newRateLimiter(RateLimitConfig{RPS: 10, Burst: 3})
	const key = "ip:10.0.0.1"

	// A full bucket grants Burst consecutive requests.
	for i := range 3 {
		if !rl.allow(key) {
			t.Fatalf("request %d should be allowed (burst=3)", i+1)
		}
	}
}

func TestAllow_DeniedAfterBurstExhausted(t *testing.T) {
	rl := newRateLimiter(RateLimitConfig{RPS: 10, Burst: 2})
	const key = "ip:10.0.0.2"

	rl.allow(key) // 1
	rl.allow(key) // 2 — bucket now empty

	if rl.allow(key) {
		t.Fatal("3rd request should be denied (burst=2, bucket empty)")
	}
}

func TestAllow_RefillAfterDelay(t *testing.T) {
	// RPS=100 means one token per 10 ms; after faking 100 ms elapsed, the
	// bucket receives 10 tokens back (capped at Burst=3).
	rl := newRateLimiter(RateLimitConfig{RPS: 100, Burst: 3})
	const key = "ip:10.0.0.3"

	// Drain the full bucket.
	for range 3 {
		rl.allow(key)
	}
	if rl.allow(key) {
		t.Fatal("should be denied after draining burst=3")
	}

	// Rewind the bucket's last-seen timestamp to simulate elapsed time.
	rl.mu.Lock()
	rl.buckets[key].last = time.Now().Add(-100 * time.Millisecond) // +10 tokens
	rl.mu.Unlock()

	if !rl.allow(key) {
		t.Fatal("should be allowed after token refill")
	}
}

func TestAllow_IndependentKeys(t *testing.T) {
	rl := newRateLimiter(RateLimitConfig{RPS: 10, Burst: 1})

	if !rl.allow("ip:1.1.1.1") {
		t.Fatal("key A: first request should be allowed")
	}
	if rl.allow("ip:1.1.1.1") {
		t.Fatal("key A: second request should be denied")
	}
	// Key B has its own full bucket — unaffected by key A.
	if !rl.allow("ip:2.2.2.2") {
		t.Fatal("key B: first request should be allowed (independent bucket)")
	}
}

// ---- retryAfterSeconds -----------------------------------------------------

func TestRetryAfterSeconds_AtLeastOne(t *testing.T) {
	rl := newRateLimiter(RateLimitConfig{RPS: 1, Burst: 1})
	const key = "ip:9.9.9.9"
	rl.allow(key) // drain
	secs := rl.retryAfterSeconds(key)
	if secs < 1 {
		t.Fatalf("retryAfterSeconds = %d, want >= 1", secs)
	}
}

func TestRetryAfterSeconds_UnknownKey(t *testing.T) {
	rl := newRateLimiter(RateLimitConfig{RPS: 5, Burst: 5})
	secs := rl.retryAfterSeconds("ip:unknown")
	if secs < 1 {
		t.Fatalf("retryAfterSeconds = %d for unknown key, want >= 1", secs)
	}
}

// ---- rateLimitMiddleware ---------------------------------------------------

func TestRateLimitMiddleware_Disabled(t *testing.T) {
	wrap := rateLimitMiddleware(nil) // nil => disabled
	called := false
	h := wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Fatal("inner handler not called when rate limiting is disabled")
	}
}

func TestRateLimitMiddleware_FirstRequestAllowed(t *testing.T) {
	rl := newRateLimiter(RateLimitConfig{RPS: 10, Burst: 5})
	wrap := rateLimitMiddleware(rl)
	h := wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/flow", nil)
	req.RemoteAddr = "203.0.113.1:9000"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", rec.Code)
	}
}

func TestRateLimitMiddleware_429AfterBurstExhausted(t *testing.T) {
	rl := newRateLimiter(RateLimitConfig{RPS: 100, Burst: 1})
	wrap := rateLimitMiddleware(rl)
	h := wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/run", nil)
	req.RemoteAddr = "198.51.100.7:1234"

	// First request uses the single burst token.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", rec.Code)
	}

	// Second request hits an empty bucket → 429.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing on 429 response")
	}
}

func TestRateLimitMiddleware_RetryAfterIsPositive(t *testing.T) {
	rl := newRateLimiter(RateLimitConfig{RPS: 1, Burst: 1})
	wrap := rateLimitMiddleware(rl)
	h := wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:80"

	h.ServeHTTP(httptest.NewRecorder(), req) // drain
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	ra := rec.Header().Get("Retry-After")
	if ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q, want a positive integer", ra)
	}
}

// ---- limitKey --------------------------------------------------------------

func TestLimitKey_ByIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.42:5678"

	key := limitKey(req, false)
	if key != "ip:192.168.1.42" {
		t.Fatalf("got %q, want %q", key, "ip:192.168.1.42")
	}
}

func TestLimitKey_XForwardedFor_TakesFirst(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1, 172.16.0.3")

	key := limitKey(req, false)
	if key != "ip:203.0.113.5" {
		t.Fatalf("got %q, want %q", key, "ip:203.0.113.5")
	}
}

func TestLimitKey_XForwardedFor_Single(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "  203.0.113.99  ")

	key := limitKey(req, false)
	if key != "ip:203.0.113.99" {
		t.Fatalf("got %q, want %q", key, "ip:203.0.113.99")
	}
}

func TestLimitKey_ByToken_UsesBearerToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer mytoken-abc123")

	key := limitKey(req, true)
	if key != "token:mytoken-abc123" {
		t.Fatalf("got %q, want %q", key, "token:mytoken-abc123")
	}
}

func TestLimitKey_ByToken_FallsBackToIP(t *testing.T) {
	// ByToken=true but no Authorization header → fall back to IP.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.10.10.10:4321"

	key := limitKey(req, true)
	if key != "ip:10.10.10.10" {
		t.Fatalf("got %q, want %q", key, "ip:10.10.10.10")
	}
}

// ---- clientIP --------------------------------------------------------------

func TestClientIP_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.16.0.5:9090"

	if got := clientIP(req); got != "172.16.0.5" {
		t.Fatalf("got %q, want %q", got, "172.16.0.5")
	}
}
