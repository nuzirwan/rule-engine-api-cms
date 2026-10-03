package auth

import (
	"context"
	"errors"
	"testing"

	"nzr-rules-engine/internal/observ"
)

// fakeEvaluator is a decision.Evaluator double returning a fixed output or error.
// It records the input it was called with so a test can assert the projection and
// that no secret was projected.
type fakeEvaluator struct {
	out      map[string]any
	err      error
	gotInput map[string]any
}

func (f *fakeEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
	f.gotInput = input
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

// TestAuthZMatrix is the AC-19 allow/deny matrix including deny-by-default on an
// absent/non-bool allow and evaluator-error-never-allow.
func TestAuthZMatrix(t *testing.T) {
	cases := []struct {
		name      string
		out       map[string]any
		evalErr   error
		wantAllow bool
		wantErr   bool
	}{
		{"explicit allow", map[string]any{"allow": true}, nil, true, false},
		{"explicit deny", map[string]any{"allow": false}, nil, false, false},
		{"allow absent (deny by default)", map[string]any{}, nil, false, false},
		{"allow non-bool (deny by default)", map[string]any{"allow": "yes"}, nil, false, false},
		{"nil output (deny by default)", nil, nil, false, false},
		{"evaluator error (never allow)", nil, observ.Classify(observ.ClassTimeout, "eval", errors.New("deadline")), false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := &fakeEvaluator{out: c.out, err: c.evalErr}
			az, err := NewAuthorizer(ev, "authz-jdm")
			if err != nil {
				t.Fatalf("NewAuthorizer: %v", err)
			}
			dec, err := az.Authorize(context.Background(), AuthzInput{
				User: "u", Roles: []string{"viewer"}, Resource: "orders", Action: "read",
			})
			if c.wantErr && err == nil {
				t.Fatalf("want error; got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dec.Allow != c.wantAllow {
				t.Fatalf("allow = %v; want %v", dec.Allow, c.wantAllow)
			}
		})
	}
}

// TestAuthZProjection proves the core projection keys reach the evaluator and
// that Attrs cannot override them.
func TestAuthZProjection(t *testing.T) {
	ev := &fakeEvaluator{out: map[string]any{"allow": true}}
	az, _ := NewAuthorizer(ev, "authz-jdm")
	_, err := az.Authorize(context.Background(), AuthzInput{
		User: "alice", Roles: []string{"admin"}, Resource: "orders", Action: "write",
		Attrs: map[string]any{"tenant": "t1", "user": "SHOULD-NOT-OVERRIDE"},
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if ev.gotInput["user"] != "alice" {
		t.Fatalf("user projection overridden: %v", ev.gotInput["user"])
	}
	if ev.gotInput["resource"] != "orders" || ev.gotInput["action"] != "write" {
		t.Fatalf("resource/action not projected: %v", ev.gotInput)
	}
	if ev.gotInput["tenant"] != "t1" {
		t.Fatalf("attr not projected: %v", ev.gotInput["tenant"])
	}
}

// TestNewAuthorizerValidation proves nil evaluator / empty JDM id are rejected.
func TestNewAuthorizerValidation(t *testing.T) {
	if _, err := NewAuthorizer(nil, "x"); err == nil {
		t.Fatalf("nil evaluator accepted")
	}
	if _, err := NewAuthorizer(&fakeEvaluator{}, ""); err == nil {
		t.Fatalf("empty jdm id accepted")
	}
}
