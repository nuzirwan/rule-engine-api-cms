package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAuthn is an Authenticator double.
type fakeAuthn struct {
	p   Principal
	err error
}

func (f fakeAuthn) Authenticate(ctx context.Context, bearer string) (Principal, error) {
	return f.p, f.err
}

// fakeAuthz is an Authorizer double.
type fakeAuthz struct {
	dec Decision
	err error
}

func (f fakeAuthz) Authorize(ctx context.Context, in AuthzInput) (Decision, error) {
	return f.dec, f.err
}

// captureLogger records every emitted field set so a test can scan for secret
// leakage. It implements observ.Logger.
type captureLogger struct {
	lines []map[string]any
}

func (c *captureLogger) Emit(ctx context.Context, level, label string, fields map[string]any) {
	cp := map[string]any{"_level": level, "_label": label}
	for k, v := range fields {
		cp[k] = v
	}
	c.lines = append(c.lines, cp)
}

// TestAuthnMiddleware proves the AuthN stage injects the Principal on success and
// returns 401 deny-by-default on any failure, without calling next.
func TestAuthnMiddleware(t *testing.T) {
	nextCalled := false
	var gotPrincipal Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		gotPrincipal, _ = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	t.Run("success injects principal", func(t *testing.T) {
		nextCalled = false
		m := NewMiddleware(fakeAuthn{p: Principal{Subject: "u1", Roles: []string{"admin"}}}, nil, nil, nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
		req.Header.Set("Authorization", "Bearer good.token")
		m.Authn(next).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200", rr.Code)
		}
		if !nextCalled || gotPrincipal.Subject != "u1" {
			t.Fatalf("principal not injected (next=%v, sub=%q)", nextCalled, gotPrincipal.Subject)
		}
	})

	t.Run("missing header -> 401, next not called", func(t *testing.T) {
		nextCalled = false
		m := NewMiddleware(fakeAuthn{}, nil, nil, nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
		m.Authn(next).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d; want 401", rr.Code)
		}
		if nextCalled {
			t.Fatalf("next called despite missing bearer")
		}
	})

	t.Run("authn error -> 401", func(t *testing.T) {
		nextCalled = false
		m := NewMiddleware(fakeAuthn{err: validationErr("authn", ErrTokenInvalid)}, nil, nil, nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
		req.Header.Set("Authorization", "Bearer bad")
		m.Authn(next).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d; want 401", rr.Code)
		}
		if nextCalled {
			t.Fatalf("next called despite authn failure")
		}
	})
}

// TestAuthzMiddleware proves the AuthZ stage allows on allow==true, returns 403
// on deny, 403 (fail-closed) on evaluator error, and 401 when no principal.
func TestAuthzMiddleware(t *testing.T) {
	ra := func(r *http.Request) (string, string) { return "orders", "read" }
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	withPrincipalReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
		return req.WithContext(withPrincipal(req.Context(), Principal{Subject: "u1", Roles: []string{"viewer"}}))
	}

	cases := []struct {
		name       string
		authz      Authorizer
		req        func() *http.Request
		wantStatus int
	}{
		{"allow", fakeAuthz{dec: Decision{Allow: true}}, withPrincipalReq, http.StatusOK},
		{"deny", fakeAuthz{dec: Decision{Allow: false, Reason: "viewer cannot read"}}, withPrincipalReq, http.StatusForbidden},
		{"evaluator error fails closed", fakeAuthz{err: errors.New("boom")}, withPrincipalReq, http.StatusForbidden},
		{"no principal -> 401", fakeAuthz{dec: Decision{Allow: true}}, func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/orders/1", nil)
		}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := NewMiddleware(nil, c.authz, ra, &captureLogger{})
			rr := httptest.NewRecorder()
			m.Authz(next).ServeHTTP(rr, c.req())
			if rr.Code != c.wantStatus {
				t.Fatalf("status = %d; want %d", rr.Code, c.wantStatus)
			}
		})
	}
}

// TestNoSecretLeakInAuditLogs is the AC-20 scan: run a full AuthN+AuthZ denial
// chain and assert no emitted field value contains the raw bearer token or a
// secret-looking value — only non-sensitive ids/outcome appear.
func TestNoSecretLeakInAuditLogs(t *testing.T) {
	const rawToken = "super-secret-bearer-jwt-value"
	log := &captureLogger{}

	// AuthN fails (so a token is in play) -> 401 audit line.
	m := NewMiddleware(fakeAuthn{err: validationErr("authn", ErrTokenInvalid)}, nil, nil, log)
	req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	m.Authn(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).
		ServeHTTP(httptest.NewRecorder(), req)

	// AuthZ denies -> 403 audit line with reason (safe) but no token.
	m2 := NewMiddleware(nil, fakeAuthz{dec: Decision{Allow: false, Reason: "policy deny"}},
		func(r *http.Request) (string, string) { return "orders", "write" }, log)
	req2 := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
	req2 = req2.WithContext(withPrincipal(req2.Context(), Principal{Subject: "u1"}))
	m2.Authz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).
		ServeHTTP(httptest.NewRecorder(), req2)

	if len(log.lines) == 0 {
		t.Fatalf("no audit lines emitted")
	}
	for i, line := range log.lines {
		for k, v := range line {
			s := fmt.Sprintf("%v", v)
			if s == rawToken || contains(s, rawToken) {
				t.Fatalf("line %d field %q leaked the raw token: %q", i, k, s)
			}
		}
	}
}

// contains reports whether needle appears in haystack.
func contains(haystack, needle string) bool {
	return needle != "" && strings.Contains(haystack, needle)
}
