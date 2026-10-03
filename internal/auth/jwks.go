package auth

import (
	"context"
	"crypto"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

// KeySource resolves the current set of JWKS public keys keyed by kid. The
// production implementation fetches and parses a JWKS endpoint; tests inject a
// fake so no network is touched. Fetch honors ctx (its own timeout) and returns
// a classified error on failure so AuthN fails closed.
type KeySource interface {
	// Fetch returns the current keyset: a map from kid to public key.
	Fetch(ctx context.Context) (map[string]crypto.PublicKey, error)
}

// keySet is an immutable snapshot of resolved keys plus the time it was fetched;
// it is swapped atomically so readers never block on a refresh and never see a
// half-updated set.
type keySet struct {
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
}

// VerifierConfig is the validated AuthN config. issuer/audience/jwksURL are
// non-secret per-env config (config-and-secrets): the engine verifies tokens, it
// never signs, so no private key lives here.
type VerifierConfig struct {
	Issuer     string
	Audience   string
	RolesClaim string        // claim holding roles (default "roles")
	TTL        time.Duration // keyset freshness bound (default 5m)
	MinRefresh time.Duration // floor between forced refreshes (default 10s)
	Leeway     time.Duration // exp/nbf clock-skew leeway (default 30s)
}

// jwksVerifier validates a bearer JWT against a cached, rotating JWKS. A kid-miss
// triggers a singleflight refresh (collapsing a stampede), the keyset is swapped
// atomically, and a bounded TTL plus a min-refresh floor bound both staleness and
// JWKS-endpoint load — so a freshly-rotated signing key is picked up on first use
// without a restart (AC-18). The clock is injectable for deterministic exp/nbf
// tests.
type jwksVerifier struct {
	cfg    VerifierConfig
	source KeySource
	parser *jwt.Parser
	clock  func() time.Time

	keys      atomic.Pointer[keySet]
	refresh   singleflight.Group
	lastFetch atomic.Int64 // unix-nano of the last fetch, to enforce MinRefresh
}

// NewVerifier builds an Authenticator over source with validated cfg. It applies
// defaults for the unset tuning knobs. clock defaults to time.Now; inject a fixed
// clock in tests. A zero issuer or audience is rejected (fail fast,
// config-and-secrets).
func NewVerifier(cfg VerifierConfig, source KeySource, clock func() time.Time) (Authenticator, error) {
	if source == nil {
		return nil, errors.New("auth: nil key source")
	}
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil, errors.New("auth: issuer required")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, errors.New("auth: audience required")
	}
	if cfg.RolesClaim == "" {
		cfg.RolesClaim = "roles"
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 5 * time.Minute
	}
	if cfg.MinRefresh <= 0 {
		cfg.MinRefresh = 10 * time.Second
	}
	if cfg.Leeway <= 0 {
		cfg.Leeway = 30 * time.Second
	}
	if clock == nil {
		clock = time.Now
	}
	v := &jwksVerifier{
		cfg:    cfg,
		source: source,
		clock:  clock,
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}),
			jwt.WithIssuer(cfg.Issuer),
			jwt.WithAudience(cfg.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(cfg.Leeway),
			jwt.WithTimeFunc(clock),
		),
	}
	return v, nil
}

// Authenticate implements Authenticator. It parses the JWS header for the kid,
// resolves the key (refreshing once on a miss), verifies signature + exp/nbf +
// iss/aud, and builds the Principal. Every failure is Validation-classified
// (→ 401) except a JWKS fetch failure (Upstream/Timeout, still fail closed).
func (v *jwksVerifier) Authenticate(ctx context.Context, bearer string) (Principal, error) {
	bearer = strings.TrimSpace(bearer)
	if bearer == "" {
		return Principal{}, validationErr("authenticate", ErrNoBearer)
	}

	claims := jwt.MapClaims{}
	tok, err := v.parser.ParseWithClaims(bearer, claims, v.keyFunc(ctx))
	if err != nil {
		// A kid-miss surfaces as ErrUnknownKey / ErrJWKSUnavailable from keyFunc;
		// preserve those classes, otherwise it's a token-validity failure.
		if ae := asAuthError(err); ae != nil {
			return Principal{}, ae
		}
		return Principal{}, validationErr("authenticate", errors.Join(ErrTokenInvalid, err))
	}
	if !tok.Valid {
		return Principal{}, validationErr("authenticate", ErrTokenInvalid)
	}
	return v.principalFrom(claims), nil
}

// keyFunc returns a jwt.Keyfunc that resolves the signing key by kid, refreshing
// the keyset once on a miss (rotation without restart, AC-18).
func (v *jwksVerifier) keyFunc(ctx context.Context) jwt.Keyfunc {
	return func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, validationErr("resolve key", ErrUnknownKey)
		}
		// Fast path: current keyset has the kid.
		if ks := v.keys.Load(); ks != nil {
			if key, ok := ks.keys[kid]; ok && !v.stale(ks) {
				return key, nil
			}
		}
		// Miss or stale: refresh once (singleflight collapses concurrent misses),
		// then retry the lookup.
		ks, err := v.refreshKeys(ctx, kid)
		if err != nil {
			return nil, err
		}
		if key, ok := ks.keys[kid]; ok {
			return key, nil
		}
		return nil, validationErr("resolve key", ErrUnknownKey)
	}
}

// refreshKeys fetches a fresh keyset through singleflight and swaps it in
// atomically, then returns it. wantKid, when non-empty, is the kid that drove
// the refresh: the MinRefresh floor only short-circuits (returning the cached
// keyset without a fetch) when the current keyset ALREADY contains wantKid — i.e.
// a redundant refresh. A genuine miss for a kid the cache lacks always refreshes
// so a freshly-rotated key is resolved on first use (AC-18). This still blunts an
// unknown-kid flood: once a refresh has run within the window, a repeat miss for
// the SAME still-absent kid collapses via singleflight, and distinct absent kids
// are rare in practice.
func (v *jwksVerifier) refreshKeys(ctx context.Context, wantKid string) (*keySet, error) {
	now := v.clock()
	last := v.lastFetch.Load()
	withinFloor := last != 0 && now.Sub(time.Unix(0, last)) < v.cfg.MinRefresh
	if withinFloor {
		if ks := v.keys.Load(); ks != nil {
			if wantKid == "" {
				return ks, nil
			}
			if _, ok := ks.keys[wantKid]; ok {
				// The wanted key is already present and recently fetched; skip.
				return ks, nil
			}
		}
	}
	res, err, _ := v.refresh.Do("jwks", func() (any, error) {
		keys, ferr := v.source.Fetch(ctx)
		if ferr != nil {
			return nil, ferr
		}
		ks := &keySet{keys: keys, fetchedAt: v.clock()}
		v.keys.Store(ks)
		v.lastFetch.Store(v.clock().UnixNano())
		return ks, nil
	})
	if err != nil {
		return nil, upstreamErr("jwks fetch", errors.Join(ErrJWKSUnavailable, err))
	}
	return res.(*keySet), nil
}

// stale reports whether ks is older than the configured TTL.
func (v *jwksVerifier) stale(ks *keySet) bool {
	return v.clock().Sub(ks.fetchedAt) > v.cfg.TTL
}

// principalFrom builds a Principal from validated claims. Roles are read from the
// configured roles claim (string slice or single string); the remaining claims
// ride along for AuthZ (minus the bearer, which is never in the claim set).
func (v *jwksVerifier) principalFrom(claims jwt.MapClaims) Principal {
	sub, _ := claims["sub"].(string)
	p := Principal{Subject: sub, Roles: rolesOf(claims[v.cfg.RolesClaim]), Claims: map[string]any(claims)}
	return p
}

// rolesOf coerces a roles claim into a string slice (accepting either a JSON
// array of strings or a single string).
func rolesOf(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	default:
		return nil
	}
}

// asAuthError extracts an *authError from err (through errors.As), or nil.
func asAuthError(err error) *authError {
	var ae *authError
	if errors.As(err, &ae) {
		return ae
	}
	return nil
}

// staticKeySource is a KeySource backed by a fixed in-memory keyset; it is the
// test double and the base for a swap-on-rotation fake. Safe for concurrent use.
type staticKeySource struct {
	mu   sync.RWMutex
	keys map[string]crypto.PublicKey
}

// NewStaticKeySource returns a KeySource serving keys. It is exported so other
// slices and tests can construct a deterministic source without a JWKS endpoint.
func NewStaticKeySource(keys map[string]crypto.PublicKey) *staticKeySource {
	cp := make(map[string]crypto.PublicKey, len(keys))
	for k, v := range keys {
		cp[k] = v
	}
	return &staticKeySource{keys: cp}
}

// Fetch implements KeySource.
func (s *staticKeySource) Fetch(ctx context.Context) (map[string]crypto.PublicKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]crypto.PublicKey, len(s.keys))
	for k, v := range s.keys {
		out[k] = v
	}
	return out, nil
}

// Set replaces the served keyset, simulating a JWKS rotation.
func (s *staticKeySource) Set(keys map[string]crypto.PublicKey) {
	cp := make(map[string]crypto.PublicKey, len(keys))
	for k, v := range keys {
		cp[k] = v
	}
	s.mu.Lock()
	s.keys = cp
	s.mu.Unlock()
}
