package httpapi

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/observ"
)

// AuthConfig is the toggleable auth configuration read from flags/env by
// cmd/engine. Auth is ENABLED only when Issuer, Audience, AuthzJDMID and a key
// source (JWKSURL or inline StaticJWKS) are all present; otherwise the chain is
// skipped (BuildAuthMiddleware returns enabled=false) so the no-auth integration
// test and local dev keep working. This matches the plan's "toggleable/bypassable
// when unconfigured" rule.
type AuthConfig struct {
	Issuer     string
	Audience   string
	JWKSURL    string // JWKS endpoint fetched + parsed to crypto.PublicKey
	StaticJWKS string // optional inline JWKS JSON (preferred by tests; no network)
	AuthzJDMID string // ZEN JDM id evaluated by the authorizer
	RolesClaim string // JWT claim carrying roles (default "roles")
}

// Enabled reports whether the config is complete enough to enforce auth. A
// missing issuer/audience/authz-jdm or no key source means auth is DISABLED.
func (c AuthConfig) Enabled() bool {
	if strings.TrimSpace(c.Issuer) == "" ||
		strings.TrimSpace(c.Audience) == "" ||
		strings.TrimSpace(c.AuthzJDMID) == "" {
		return false
	}
	return strings.TrimSpace(c.JWKSURL) != "" || strings.TrimSpace(c.StaticJWKS) != ""
}

// BuildAuthMiddleware builds the AuthN+AuthZ middleware over the shared decision
// evaluator. It returns (nil, false, nil) when cfg is incomplete (auth disabled)
// so the caller mounts the bare flow route and logs a clear DISABLED warning.
// When enabled it builds auth.NewVerifier over a key source (an http JWKS
// fetcher, or a static source parsed from inline JWKS JSON) and
// auth.NewAuthorizer over decide, wrapping both with auth.NewMiddleware and the
// route-pattern resource/action mapper.
func BuildAuthMiddleware(cfg AuthConfig, decide decision.Evaluator, log observ.Logger) (*auth.Middleware, bool, error) {
	if !cfg.Enabled() {
		return nil, false, nil
	}

	source, err := keySourceFor(cfg)
	if err != nil {
		return nil, false, err
	}

	verifier, err := auth.NewVerifier(auth.VerifierConfig{
		Issuer:     cfg.Issuer,
		Audience:   cfg.Audience,
		RolesClaim: cfg.RolesClaim,
	}, source, nil)
	if err != nil {
		return nil, false, err
	}

	authorizer, err := auth.NewAuthorizer(decide, cfg.AuthzJDMID)
	if err != nil {
		return nil, false, err
	}

	mw := auth.NewMiddleware(verifier, authorizer, routeResourceAction, log)
	return mw, true, nil
}

// keySourceFor picks the key source: inline static JWKS JSON when provided
// (deterministic, no network — the choice used by the integration test), else an
// http JWKS fetcher over the configured URL (the production path). Only ONE is
// used; inline JWKS wins when both are set.
func keySourceFor(cfg AuthConfig) (auth.KeySource, error) {
	if strings.TrimSpace(cfg.StaticJWKS) != "" {
		keys, err := parseJWKS([]byte(cfg.StaticJWKS))
		if err != nil {
			return nil, fmt.Errorf("parse static jwks: %w", err)
		}
		return auth.NewStaticKeySource(keys), nil
	}
	return &httpKeySource{url: cfg.JWKSURL, client: &http.Client{Timeout: 5 * time.Second}}, nil
}

// routeResourceAction maps a request to the AuthZ (resource, action) pair: the
// matched route pattern is the resource (stable, bounded cardinality — not the
// concrete path), and the HTTP method maps to a verb (GET/HEAD -> "read", else
// "write"). The composition root supplies this so auth carries no routing
// knowledge (see auth.ResourceActionFunc).
func routeResourceAction(r *http.Request) (resource, action string) {
	resource = r.Pattern
	if resource == "" {
		resource = r.URL.Path
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		action = "read"
	default:
		action = "write"
	}
	return resource, action
}

// httpKeySource fetches a JWKS document from url and parses it into a kid ->
// crypto.PublicKey map. It is the genuinely-new piece of wiring (no real fetcher
// existed on mainline); the parse supports RSA (kty "RSA") and EC (kty "EC")
// public keys, the two families the verifier accepts (RS*/ES*).
type httpKeySource struct {
	url    string
	client *http.Client
}

// Fetch implements auth.KeySource. It honors ctx and returns a parsed keyset.
func (s *httpKeySource) Fetch(ctx context.Context) (map[string]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch: status %d", resp.StatusCode)
	}
	body := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if rerr != nil {
			break
		}
		if len(body) > 1<<20 { // 1 MiB cap on a JWKS document
			return nil, errors.New("jwks document too large")
		}
	}
	return parseJWKS(body)
}

// jwksDoc is the subset of the JWKS schema the parser reads (RFC 7517).
type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

// jwk is one JSON Web Key (RSA or EC public key fields).
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	// RSA
	N string `json:"n"`
	E string `json:"e"`
	// EC
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKS parses a JWKS document into a kid -> crypto.PublicKey map, decoding
// RSA and EC public keys. A key without a kid is skipped (the verifier resolves
// strictly by kid). An unsupported kty is skipped rather than failing the whole
// document so one odd key does not break rotation.
func parseJWKS(doc []byte) (map[string]crypto.PublicKey, error) {
	var parsed jwksDoc
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, err
	}
	out := make(map[string]crypto.PublicKey, len(parsed.Keys))
	for _, k := range parsed.Keys {
		if k.Kid == "" {
			continue
		}
		switch k.Kty {
		case "RSA":
			pub, err := rsaPublicKey(k)
			if err != nil {
				return nil, fmt.Errorf("jwk %q: %w", k.Kid, err)
			}
			out[k.Kid] = pub
		case "EC":
			pub, err := ecPublicKey(k)
			if err != nil {
				return nil, fmt.Errorf("jwk %q: %w", k.Kid, err)
			}
			out[k.Kid] = pub
		default:
			// Unsupported key type: skip it.
		}
	}
	if len(out) == 0 {
		return nil, errors.New("jwks: no usable keys")
	}
	return out, nil
}

// rsaPublicKey builds an *rsa.PublicKey from a JWK's base64url n/e fields.
func rsaPublicKey(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}
	// Left-pad e to 8 bytes for a big-endian uint64, then narrow to int.
	var eBuf [8]byte
	copy(eBuf[8-len(eBytes):], eBytes)
	e := binary.BigEndian.Uint64(eBuf[:])
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(e),
	}, nil
}

// ecPublicKey builds an *ecdsa.PublicKey from a JWK's crv/x/y fields.
func ecPublicKey(k jwk) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported curve %q", k.Crv)
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("decode x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("decode y: %w", err)
	}
	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}
