package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturingLogger records every emitted field set so a test can assert that no
// sensitive value (token/hash/header) ever reaches a sink.
type capturingLogger struct {
	entries []map[string]any
}

func (l *capturingLogger) Emit(ctx context.Context, level, label string, fields map[string]any) {
	cp := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		cp[k] = v
	}
	cp["_label"] = label
	l.entries = append(l.entries, cp)
}

// fakeOperatorAuth returns a canned operator or error.
type fakeOperatorAuth struct {
	op  Operator
	err error
}

func (f fakeOperatorAuth) AuthenticateOperator(ctx context.Context, r *http.Request) (Operator, error) {
	return f.op, f.err
}

// TestOperatorGuardDenyByDefault covers the four guard paths and asserts nothing
// sensitive is logged.
func TestOperatorGuardDenyByDefault(t *testing.T) {
	const token = "s3cr3t-operator-token"
	hash := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(hash[:])

	nextCalled := false
	var seenOp Operator
	next := func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		seenOp, _ = OperatorFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}

	requirePublish := func(r *http.Request) string { return "flow.publish" }

	tests := []struct {
		name       string
		authn      OperatorAuthenticator
		require    RequireRoleFunc
		wantStatus int
		wantNext   bool
	}{
		{
			name:       "disabled plane (nil authn) => 503",
			authn:      nil,
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "bad credential => 401",
			authn:      fakeOperatorAuth{err: ErrTokenInvalid},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong role => 403",
			authn:      fakeOperatorAuth{op: Operator{Subject: "op:alice", Roles: []string{"flow.read"}}},
			require:    requirePublish,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "valid => next with operator in ctx",
			authn:      fakeOperatorAuth{op: Operator{Subject: "op:alice", Roles: []string{"flow.publish"}}},
			require:    requirePublish,
			wantStatus: http.StatusOK,
			wantNext:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled = false
			seenOp = Operator{}
			log := &capturingLogger{}
			g := NewOperatorGuard(tc.authn, tc.require, log)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/admin/flows/orders/publish", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			g.Protect(next).ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d want %d", rec.Code, tc.wantStatus)
			}
			if nextCalled != tc.wantNext {
				t.Fatalf("nextCalled = %v want %v", nextCalled, tc.wantNext)
			}
			if tc.wantNext && seenOp.Subject != "op:alice" {
				t.Fatalf("operator in ctx = %+v want subject op:alice", seenOp)
			}
			// No sensitive value ever logged.
			for _, e := range log.entries {
				for k, v := range e {
					s, _ := v.(string)
					if s == token || s == hashHex || strings.Contains(s, token) {
						t.Fatalf("sensitive value leaked in log field %q: %v", k, v)
					}
				}
			}
		})
	}
}

// TestStaticTokenOperatorAuthConstructor covers fail-fast parsing and the
// constant-time match.
func TestStaticTokenOperatorAuthConstructor(t *testing.T) {
	tokA := "token-a"
	tokB := "token-b"
	hashA := hex.EncodeToString(sha256HexBytes(tokA))
	hashB := hex.EncodeToString(sha256HexBytes(tokB))

	t.Run("rejects an entry with no colons", func(t *testing.T) {
		if _, err := NewStaticTokenOperatorAuth("onlyonepart"); err == nil {
			t.Fatal("want error for a single-part entry")
		}
		if _, err := NewStaticTokenOperatorAuth(hashA + ":onlytwoparts"); err == nil {
			t.Fatal("want error for a two-part entry (no roles segment)")
		}
	})

	t.Run("rejects non-hex / wrong-length hash", func(t *testing.T) {
		if _, err := NewStaticTokenOperatorAuth("nothex:op:alice:flow.read"); err == nil {
			t.Fatal("want error for a non-hex hash")
		}
		if _, err := NewStaticTokenOperatorAuth("abcd:op:alice:flow.read"); err == nil {
			t.Fatal("want error for a wrong-length hash")
		}
	})

	t.Run("rejects duplicate subject", func(t *testing.T) {
		spec := hashA + ":op:alice:flow.read;" + hashB + ":op:alice:flow.write"
		if _, err := NewStaticTokenOperatorAuth(spec); err == nil {
			t.Fatal("want error for a duplicate subject")
		}
	})

	t.Run("rejects empty / zero-token config", func(t *testing.T) {
		if _, err := NewStaticTokenOperatorAuth(""); !errors.Is(err, ErrNoOperatorTokens) {
			t.Fatalf("empty spec err = %v want ErrNoOperatorTokens", err)
		}
		if _, err := NewStaticTokenOperatorAuth("  ;  "); !errors.Is(err, ErrNoOperatorTokens) {
			t.Fatalf("whitespace spec err = %v want ErrNoOperatorTokens", err)
		}
	})

	t.Run("accepts a well-formed allow-list and matches constant-time", func(t *testing.T) {
		spec := hashA + ":op:alice:flow.write,flow.publish;" + hashB + ":op:strapi:flow.read"
		a, err := NewStaticTokenOperatorAuth(spec)
		if err != nil {
			t.Fatalf("construct: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/admin/connections", nil)
		req.Header.Set("Authorization", "Bearer "+tokA)
		op, err := a.AuthenticateOperator(context.Background(), req)
		if err != nil {
			t.Fatalf("authenticate alice: %v", err)
		}
		if op.Subject != "op:alice" || len(op.Roles) != 2 {
			t.Fatalf("operator = %+v want op:alice with 2 roles", op)
		}

		// An unknown token is rejected.
		req2 := httptest.NewRequest(http.MethodGet, "/admin/connections", nil)
		req2.Header.Set("Authorization", "Bearer unknown-token")
		if _, err := a.AuthenticateOperator(context.Background(), req2); err == nil {
			t.Fatal("want error for an unknown token")
		}

		// A missing header is rejected.
		req3 := httptest.NewRequest(http.MethodGet, "/admin/connections", nil)
		if _, err := a.AuthenticateOperator(context.Background(), req3); err == nil {
			t.Fatal("want error for a missing bearer")
		}
	})
}

func sha256HexBytes(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}
