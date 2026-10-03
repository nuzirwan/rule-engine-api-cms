package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "https://issuer.test"
	testAudience = "nzr-api"
)

// fixedClock returns a clock function pinned at t.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// signRSA signs claims with key under kid using RS256.
func signRSA(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

// validClaims builds a standard claim set valid at `now`.
func validClaims(now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"sub":   "user-1",
		"iss":   testIssuer,
		"aud":   testAudience,
		"roles": []any{"admin", "viewer"},
		"iat":   now.Add(-time.Minute).Unix(),
		"nbf":   now.Add(-time.Minute).Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
}

// errKeySource always fails Fetch, simulating a JWKS endpoint being down.
type errKeySource struct{ err error }

func (e errKeySource) Fetch(ctx context.Context) (map[string]crypto.PublicKey, error) {
	return nil, e.err
}

// TestTokenValidityMatrix is the AC-18 token-validity matrix including rotation
// without restart. Each case drives Authenticate against an injected clock and
// keyset state.
func TestTokenValidityMatrix(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	k1, _ := rsa.GenerateKey(rand.Reader, 2048)
	k2, _ := rsa.GenerateKey(rand.Reader, 2048) // the rotated-in key

	// Build the token under test lazily per case so mutations are isolated.
	cases := []struct {
		name     string
		token    func(src *staticKeySource) string
		wantErr  error
		wantSub  string
		wantRoot error // optional errors.Is target
	}{
		{
			name:    "valid",
			token:   func(_ *staticKeySource) string { return signRSA(t, k1, "k1", validClaims(now)) },
			wantSub: "user-1",
		},
		{
			name: "expired",
			token: func(_ *staticKeySource) string {
				c := validClaims(now)
				c["exp"] = now.Add(-time.Hour).Unix()
				return signRSA(t, k1, "k1", c)
			},
			wantRoot: ErrTokenInvalid,
		},
		{
			name: "not yet valid",
			token: func(_ *staticKeySource) string {
				c := validClaims(now)
				c["nbf"] = now.Add(time.Hour).Unix()
				return signRSA(t, k1, "k1", c)
			},
			wantRoot: ErrTokenInvalid,
		},
		{
			name: "wrong issuer",
			token: func(_ *staticKeySource) string {
				c := validClaims(now)
				c["iss"] = "https://evil.test"
				return signRSA(t, k1, "k1", c)
			},
			wantRoot: ErrTokenInvalid,
		},
		{
			name: "wrong audience",
			token: func(_ *staticKeySource) string {
				c := validClaims(now)
				c["aud"] = "other-api"
				return signRSA(t, k1, "k1", c)
			},
			wantRoot: ErrTokenInvalid,
		},
		{
			name: "bad signature (unknown signer)",
			token: func(_ *staticKeySource) string {
				// signed by k2 but presented under kid k1 (resolves k1's pubkey)
				return signRSA(t, k2, "k1", validClaims(now))
			},
			wantRoot: ErrTokenInvalid,
		},
		{
			name: "unknown kid, still missing after refresh",
			token: func(_ *staticKeySource) string {
				return signRSA(t, k2, "zzz", validClaims(now))
			},
			wantRoot: ErrUnknownKey,
		},
		{
			name: "unknown kid, refresh resolves (rotation without restart)",
			token: func(src *staticKeySource) string {
				// Source starts with only k1; rotate k2 in. The verifier caches
				// k1, misses k2's kid, refreshes once, and resolves it — no restart.
				src.Set(map[string]crypto.PublicKey{"k1": k1.Public(), "k2": k2.Public()})
				return signRSA(t, k2, "k2", validClaims(now))
			},
			wantSub: "user-1",
		},
		{
			name:     "missing bearer",
			token:    func(_ *staticKeySource) string { return "" },
			wantRoot: ErrNoBearer,
		},
		{
			name:     "garbled token",
			token:    func(_ *staticKeySource) string { return "not-a-jwt" },
			wantRoot: ErrTokenInvalid,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Each case gets a fresh verifier primed with only k1 so the rotation
			// case genuinely exercises a cache miss -> refresh.
			src := NewStaticKeySource(map[string]crypto.PublicKey{"k1": k1.Public()})
			v, err := NewVerifier(VerifierConfig{Issuer: testIssuer, Audience: testAudience}, src, fixedClock(now))
			if err != nil {
				t.Fatalf("NewVerifier: %v", err)
			}
			// Prime the cache with the initial keyset.
			if _, perr := v.(*jwksVerifier).refreshKeys(context.Background(), "k1"); perr != nil {
				t.Fatalf("prime: %v", perr)
			}

			tok := c.token(src)
			p, err := v.Authenticate(context.Background(), tok)
			if c.wantRoot != nil {
				if err == nil {
					t.Fatalf("want error %v; got nil (principal %+v)", c.wantRoot, p)
				}
				if !errors.Is(err, c.wantRoot) {
					t.Fatalf("error = %v; want errors.Is %v", err, c.wantRoot)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Subject != c.wantSub {
				t.Fatalf("subject = %q; want %q", p.Subject, c.wantSub)
			}
			if len(p.Roles) != 2 || p.Roles[0] != "admin" {
				t.Fatalf("roles = %v; want [admin viewer]", p.Roles)
			}
		})
	}
}

// TestJWKSUnavailableFailsClosed proves a JWKS fetch failure on a cache miss
// denies (never fail-open) and is Upstream-classified.
func TestJWKSUnavailableFailsClosed(t *testing.T) {
	now := time.Now()
	k1, _ := rsa.GenerateKey(rand.Reader, 2048)
	v, err := NewVerifier(VerifierConfig{Issuer: testIssuer, Audience: testAudience},
		errKeySource{err: errors.New("connection refused")}, fixedClock(now))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	tok := signRSA(t, k1, "k1", validClaims(now))

	_, err = v.Authenticate(context.Background(), tok)
	if err == nil {
		t.Fatalf("want fail-closed error; got nil")
	}
	if !errors.Is(err, ErrJWKSUnavailable) {
		t.Fatalf("error = %v; want errors.Is ErrJWKSUnavailable", err)
	}
}

// countingKeySource counts Fetch calls to assert the singleflight collapse.
type countingKeySource struct {
	inner   *staticKeySource
	fetches int
}

func (c *countingKeySource) Fetch(ctx context.Context) (map[string]crypto.PublicKey, error) {
	c.fetches++
	return c.inner.Fetch(ctx)
}

// TestRotationRefreshesOnce proves an unknown-kid token triggers exactly one
// JWKS refresh (singleflight) and that the swapped-in key then validates
// subsequent tokens from cache (rotation without restart, bounded fetch).
func TestRotationRefreshesOnce(t *testing.T) {
	now := time.Now()
	k1, _ := rsa.GenerateKey(rand.Reader, 2048)
	k2, _ := rsa.GenerateKey(rand.Reader, 2048)

	src := &countingKeySource{inner: NewStaticKeySource(map[string]crypto.PublicKey{"k1": k1.Public()})}
	v, err := NewVerifier(VerifierConfig{Issuer: testIssuer, Audience: testAudience}, src, fixedClock(now))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// First token under k1: one fetch to prime the keyset on the miss.
	if _, err := v.Authenticate(context.Background(), signRSA(t, k1, "k1", validClaims(now))); err != nil {
		t.Fatalf("k1 authenticate: %v", err)
	}
	if src.fetches != 1 {
		t.Fatalf("fetches after first auth = %d; want 1", src.fetches)
	}

	// Rotate k2 in, then present a k2 token: a single refresh resolves it.
	src.inner.Set(map[string]crypto.PublicKey{"k1": k1.Public(), "k2": k2.Public()})
	if _, err := v.Authenticate(context.Background(), signRSA(t, k2, "k2", validClaims(now))); err != nil {
		t.Fatalf("k2 authenticate (rotation): %v", err)
	}
	if src.fetches != 2 {
		t.Fatalf("fetches after rotation = %d; want 2 (one refresh on the k2 miss)", src.fetches)
	}

	// A second k2 token is served from the swapped-in cache: no extra fetch.
	if _, err := v.Authenticate(context.Background(), signRSA(t, k2, "k2", validClaims(now))); err != nil {
		t.Fatalf("second k2 authenticate: %v", err)
	}
	if src.fetches != 2 {
		t.Fatalf("fetches after cached k2 = %d; want 2 (served from cache)", src.fetches)
	}
}

// TestVerifierConfigValidation proves missing issuer/audience fail fast.
func TestVerifierConfigValidation(t *testing.T) {
	src := NewStaticKeySource(nil)
	if _, err := NewVerifier(VerifierConfig{Audience: testAudience}, src, nil); err == nil {
		t.Fatalf("missing issuer accepted")
	}
	if _, err := NewVerifier(VerifierConfig{Issuer: testIssuer}, src, nil); err == nil {
		t.Fatalf("missing audience accepted")
	}
}
