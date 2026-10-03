package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"nzr-rules-engine/internal/observ"
)

// This file is the PRIVILEGED operator-auth mechanism for the /admin control
// plane (slice-f-admin-api.md §3). It is intentionally SEPARATE from and
// independent of the public JWT data-plane path (Authenticator/Middleware above):
// a different trust domain, a different credential lifecycle, a different blast
// radius. It is deny-by-default and fails CLOSED — mounted-closed when the plane
// is not configured, 401 on a bad credential, 403 on a missing role. The token,
// its hash, and the Authorization header value are NEVER logged ([[config-and-secrets]]).

// Operator is the authenticated control-plane principal. Subject attributes an
// audit mutation to the operator; Roles drives per-route RBAC.
type Operator struct {
	Subject string
	Roles   []string
}

// OperatorAuthenticator validates an operator credential on a request and yields
// an Operator. It is a DISTINCT seam from Authenticator (the public JWT path), so
// a later increment can swap in mTLS or a separate operator OIDC issuer without
// touching any handler.
type OperatorAuthenticator interface {
	AuthenticateOperator(ctx context.Context, r *http.Request) (Operator, error)
}

// operatorKey carries the authenticated Operator on the request context so a
// handler reads the audit actor from it.
type operatorKey struct{}

// OperatorFrom returns the Operator injected by a passing OperatorGuard.
func OperatorFrom(ctx context.Context) (Operator, bool) {
	if ctx == nil {
		return Operator{}, false
	}
	o, ok := ctx.Value(operatorKey{}).(Operator)
	return o, ok
}

// RequireRoleFunc derives the role a route requires (RBAC), supplied by the
// composition root so auth carries no routing knowledge (same pattern as
// ResourceActionFunc). An empty role means "any authenticated operator".
type RequireRoleFunc func(r *http.Request) (role string)

// OperatorGuard is the deny-by-default middleware wrapping every /admin route.
// A nil authn means the operator plane is NOT configured: every request is
// mounted-closed (503), never mounted-open.
type OperatorGuard struct {
	authn   OperatorAuthenticator // nil => plane disabled (mount-closed)
	require RequireRoleFunc       // route -> required role; nil => no RBAC gate
	log     observ.Logger
}

// NewOperatorGuard builds the guard. A nil authn is valid and intentional — it
// is how cmd/engine signals "operator plane disabled" (the guard then fails
// closed with 503 on every request). A nil require skips the RBAC gate; a nil
// log skips audit lines.
func NewOperatorGuard(authn OperatorAuthenticator, require RequireRoleFunc, log observ.Logger) *OperatorGuard {
	return &OperatorGuard{authn: authn, require: require, log: log}
}

// Protect wraps next with the deny-by-default operator chain. Order:
//  1. plane disabled (authn == nil) => 503, mounted-closed (never mounted-open);
//  2. credential invalid/absent => 401, detail logged, never the credential;
//  3. required role not held => 403, security event logged (subject/resource/
//     action), never the credential;
//  4. otherwise inject the Operator into ctx and call next.
func (g *OperatorGuard) Protect(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.authn == nil {
			// Absence of operator config is DENIAL, not a bypass — the opposite of
			// the public chain's convenience. The control plane must never be
			// reachable unauthenticated.
			g.audit(r, "warn", "admin plane disabled", map[string]any{"outcome": "deny"})
			writeOperatorError(w, http.StatusServiceUnavailable, "admin plane disabled")
			return
		}

		op, err := g.authn.AuthenticateOperator(r.Context(), r)
		if err != nil {
			g.audit(r, "warn", "operator authn denied", map[string]any{
				"outcome":     "deny",
				"error_class": string(observ.ClassOf(err)),
			})
			writeOperatorError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		if g.require != nil {
			role := g.require(r)
			if role != "" && !hasRole(op.Roles, role) {
				g.audit(r, "warn", "operator authz denied", map[string]any{
					"outcome":  "deny",
					"sub":      op.Subject,
					"resource": r.URL.Path,
					"action":   r.Method,
					"role":     role,
				})
				writeOperatorError(w, http.StatusForbidden, "forbidden")
				return
			}
		}

		ctx := context.WithValue(r.Context(), operatorKey{}, op)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// audit emits a non-sensitive log line (never the token/hash/header value).
func (g *OperatorGuard) audit(r *http.Request, level, label string, fields map[string]any) {
	if g.log == nil {
		return
	}
	g.log.Emit(r.Context(), level, label, fields)
}

// writeOperatorError writes a coarse JSON error body; no internal detail or
// credential ever reaches the client.
func writeOperatorError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":` + jsonString(msg) + `}`))
}

// jsonString renders s as a minimal JSON string literal (the messages here are
// fixed ASCII, so a quote-wrap with escaped quotes/backslashes suffices).
func jsonString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// hasRole reports whether roles contains role.
func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// ---- StaticTokenOperatorAuth: v1 hashed bearer-token allow-list ----

// ErrNoOperatorTokens is the sentinel for an operator-auth config with zero
// valid tokens — an enabled-but-empty allow-list never boots (deny-by-default).
var ErrNoOperatorTokens = errors.New("operator token allow-list is empty")

// staticOperator is one entry of the allow-list: the SHA-256 hash of the token
// (never the plaintext), the subject it maps to, and its roles.
type staticOperator struct {
	hash    [sha256.Size]byte
	subject string
	roles   []string
}

// StaticTokenOperatorAuth authenticates an operator by matching the request's
// bearer token against a configured allow-list, comparing SHA-256 hashes with a
// constant-time compare (no timing oracle). It stores only hashes; the plaintext
// token is never retained (slice-f-admin-api.md §3.3).
type StaticTokenOperatorAuth struct {
	ops []staticOperator
}

// compile-time assertion that *StaticTokenOperatorAuth is an authenticator.
var _ OperatorAuthenticator = (*StaticTokenOperatorAuth)(nil)

// NewStaticTokenOperatorAuth parses a ';'-separated allow-list where each entry
// is "sha256hex:subject:comma,roles". It is FAIL-FAST: a malformed entry (not
// three ':'-parts, a non-hex / wrong-length hash, an empty subject, or a
// duplicate subject) returns an error, and an allow-list with zero valid tokens
// returns ErrNoOperatorTokens — so cmd/engine can treat ADMIN_ENABLED=true with a
// bad/empty config as a fatal boot error. A bad config must never silently come
// up with a weaker allow-list.
func NewStaticTokenOperatorAuth(spec string) (*StaticTokenOperatorAuth, error) {
	var ops []staticOperator
	seen := map[string]struct{}{}

	for _, raw := range strings.Split(spec, ";") {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		// Entry shape: "sha256hex:subject:comma,roles". The SUBJECT itself may
		// contain colons (e.g. "op:alice"), so the hash is the first ':'-segment
		// and the roles are the LAST ':'-segment; everything in between is the
		// subject. This keeps the documented "op:alice" subject form unambiguous.
		firstColon := strings.IndexByte(entry, ':')
		lastColon := strings.LastIndexByte(entry, ':')
		if firstColon < 0 || lastColon <= firstColon {
			return nil, fmt.Errorf("malformed operator token entry: want sha256hex:subject:roles")
		}
		hashHex := strings.TrimSpace(entry[:firstColon])
		subject := strings.TrimSpace(entry[firstColon+1 : lastColon])
		rolesCSV := strings.TrimSpace(entry[lastColon+1:])

		rawHash, err := hex.DecodeString(hashHex)
		if err != nil || len(rawHash) != sha256.Size {
			return nil, fmt.Errorf("malformed operator token hash for subject %q: want %d-byte sha256 hex", subject, sha256.Size)
		}
		if subject == "" {
			return nil, fmt.Errorf("operator token entry has empty subject")
		}
		if _, dup := seen[subject]; dup {
			return nil, fmt.Errorf("duplicate operator subject %q", subject)
		}
		seen[subject] = struct{}{}

		var roles []string
		for _, role := range strings.Split(rolesCSV, ",") {
			if r := strings.TrimSpace(role); r != "" {
				roles = append(roles, r)
			}
		}

		var h [sha256.Size]byte
		copy(h[:], rawHash)
		ops = append(ops, staticOperator{hash: h, subject: subject, roles: roles})
	}

	if len(ops) == 0 {
		return nil, ErrNoOperatorTokens
	}
	return &StaticTokenOperatorAuth{ops: ops}, nil
}

// AuthenticateOperator extracts the bearer token (reusing the single RFC-7235
// parser, BearerToken) and matches its SHA-256 hash against the allow-list with a
// constant-time compare. A missing/unknown token is ErrTokenInvalid (→ 401). The
// token is never logged or echoed.
func (a *StaticTokenOperatorAuth) AuthenticateOperator(ctx context.Context, r *http.Request) (Operator, error) {
	tok, ok := BearerToken(r)
	if !ok {
		return Operator{}, validationErr("operator authn", ErrNoBearer)
	}
	sum := sha256.Sum256([]byte(tok))

	// Constant-time scan over the whole allow-list: compare every entry so the
	// match time does not depend on which (or whether a) token matched.
	match := -1
	for i := range a.ops {
		if subtle.ConstantTimeCompare(sum[:], a.ops[i].hash[:]) == 1 {
			match = i
		}
	}
	if match < 0 {
		return Operator{}, validationErr("operator authn", ErrTokenInvalid)
	}
	return Operator{Subject: a.ops[match].subject, Roles: a.ops[match].roles}, nil
}
