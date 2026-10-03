package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"nzr-rules-engine/internal/observ"
)

// principalKey is the unexported context key under which Authn stores the
// authenticated Principal for downstream AuthZ and the interpreter.
type principalKey struct{}

// PrincipalFrom returns the Principal injected by Authn, if the request passed
// authentication.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// withPrincipal returns a child context carrying p.
func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// ResourceActionFunc derives the AuthZ resource and action from the request
// (e.g. route pattern + HTTP-method-to-verb). The composition root supplies it so
// auth carries no routing knowledge.
type ResourceActionFunc func(r *http.Request) (resource, action string)

// Middleware is the stdlib net/http auth chain (no third-party router). Authn
// runs first and injects the Principal; Authz runs next and gates on a ZEN
// decision. Both fail closed. log is used only for non-sensitive audit fields —
// never the raw token, full claims, or any secret value (AC-20).
type Middleware struct {
	authn Authenticator
	authz Authorizer
	ra    ResourceActionFunc
	log   observ.Logger
}

// NewMiddleware builds the chain. ra may be nil only if Authz is not mounted; a
// nil log is tolerated (audit lines are skipped). authn/authz may be nil to mount
// just one stage, but mounting a stage with its dependency nil is a programmer
// error caught when that stage runs.
func NewMiddleware(authn Authenticator, authz Authorizer, ra ResourceActionFunc, log observ.Logger) *Middleware {
	return &Middleware{authn: authn, authz: authz, ra: ra, log: log}
}

// Authn validates the bearer token and injects the Principal into the request
// context. Any failure writes 401 with a generic body (detail goes to logs only)
// and does NOT call next — deny by default (security-and-authz).
func (m *Middleware) Authn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, ok := bearerToken(r)
		if !ok {
			m.deny401(w, r, validationErr("authn", ErrNoBearer), "")
			return
		}
		p, err := m.authn.Authenticate(r.Context(), bearer)
		if err != nil {
			m.deny401(w, r, err, "")
			return
		}
		ctx := withPrincipal(r.Context(), p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Authz evaluates the ZEN authorization decision for the authenticated
// principal. A deny (or absent/non-bool allow) writes 403; an evaluator error is
// never an allow and writes 403 as well (fail closed) with the error logged. A
// missing principal (Authn not run) is treated as unauthenticated → 401.
func (m *Middleware) Authz(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			m.deny401(w, r, validationErr("authz", errors.New("no principal")), "")
			return
		}
		resource, action := "", ""
		if m.ra != nil {
			resource, action = m.ra(r)
		}
		in := AuthzInput{
			User:     p.Subject,
			Roles:    p.Roles,
			Resource: resource,
			Action:   action,
		}
		dec, err := m.authz.Authorize(r.Context(), in)
		if err != nil {
			// Fail closed: an evaluator error denies, logged as a security event.
			m.deny403(w, r, p, resource, action, "evaluator error", err)
			return
		}
		if !dec.Allow {
			m.deny403(w, r, p, resource, action, dec.Reason, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// deny401 writes a generic 401 and logs the failure with non-sensitive fields.
func (m *Middleware) deny401(w http.ResponseWriter, r *http.Request, err error, sub string) {
	m.audit(r, "warn", "authn denied", map[string]any{
		"outcome":     "deny",
		"sub":         sub,
		"error_class": string(observ.ClassOf(err)),
	})
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// deny403 writes a generic 403 and logs an authz-failure audit event (security
// event) with the reason and ids but never the token or any secret (AC-20).
func (m *Middleware) deny403(w http.ResponseWriter, r *http.Request, p Principal, resource, action, reason string, err error) {
	fields := map[string]any{
		"outcome":  "deny",
		"sub":      p.Subject,
		"resource": resource,
		"action":   action,
		"reason":   reason,
	}
	if err != nil {
		fields["error_class"] = string(observ.ClassOf(err))
	}
	m.audit(r, "warn", "authz denied", fields)
	http.Error(w, "forbidden", http.StatusForbidden)
}

// audit emits a log line when a logger is configured. The observ.Logger's
// central Redactor still scrubs any sensitive key as a backstop.
func (m *Middleware) audit(r *http.Request, level, label string, fields map[string]any) {
	if m.log == nil {
		return
	}
	m.log.Emit(r.Context(), level, label, fields)
}

// bearerToken extracts the token from an "Authorization: Bearer <jwt>" header.
// The match is case-insensitive on the scheme per RFC 7235.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}
