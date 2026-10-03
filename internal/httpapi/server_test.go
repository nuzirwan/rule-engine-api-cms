package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
)

// fakeStore returns a canned ActiveFlow result (or error) for the one route.
type fakeStore struct {
	fv  config.FlowVersion
	err error
}

func (s fakeStore) ActiveFlow(ctx context.Context, env, method, path string) (config.FlowVersion, error) {
	return s.fv, s.err
}

// GetJDM/Connections satisfy the widened Store seam (admin endpoints). The
// routing/edge tests do not exercise them, so they return empty/no-error.
func (s fakeStore) GetJDM(ctx context.Context, env, id string) ([]byte, int, error) {
	return nil, 0, config.ErrNotFound
}

func (s fakeStore) Connections(ctx context.Context, env string) ([]connect.ConnectionDef, error) {
	return nil, nil
}

// TestHandlerRouting proves the one hard-coded route is reachable and that a
// non-matching method/path is rejected by the stdlib ServeMux (not the handler).
func TestHandlerRouting(t *testing.T) {
	// A minimal flow whose root is a trigger with a single response child, so the
	// walk completes with no I/O: Trigger -> Response.
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{ID: "t", Type: flow.TypeTrigger, Spec: json.RawMessage(`{"method":"GET","path":"/orders/{id}","input":{"params":["id"]}}`), Children: []flow.Node{resp}}
	store := fakeStore{fv: config.FlowVersion{FlowID: "f", Version: 1, Method: "GET", Path: "/orders/{id}", Tree: trig}}

	h := NewHandler(store, flow.New(), Deps{})

	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
	}{
		{"matched route", http.MethodGet, "/orders/42", http.StatusOK},
		{"wrong method", http.MethodPost, "/orders/42", http.StatusMethodNotAllowed},
		{"unknown path", http.MethodGet, "/widgets/42", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestHandlerConfigError maps a store NotFound to a 404 at the edge.
func TestHandlerConfigError(t *testing.T) {
	store := fakeStore{err: config.ErrNotFound}
	h := NewHandler(store, flow.New(), Deps{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404", rec.Code)
	}
}

// TestStatusForFlow proves each classified flow error maps to its HTTP status
// by errors.Is, never by string match.
func TestStatusForFlow(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{flow.ErrValidation, http.StatusBadRequest},
		{flow.ErrNotFound, http.StatusNotFound},
		{flow.ErrTimeout, http.StatusGatewayTimeout},
		{flow.ErrUpstream, http.StatusBadGateway},
		{flow.ErrInternal, http.StatusInternalServerError},
		{errors.New("unclassified"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got, _ := statusForFlow(tc.err); got != tc.want {
			t.Errorf("statusForFlow(%v) = %d want %d", tc.err, got, tc.want)
		}
	}
}

// TestCoercePathParam maps a numeric id to an int and leaves other values as
// strings.
func TestCoercePathParam(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"1", 1},
		{"1500", 1500},
		{"007", "007"}, // leading zero: not a clean round-trip, stays a string
		{"abc", "abc"},
	}
	for _, tc := range cases {
		if got := coercePathParam(tc.in); got != tc.want {
			t.Errorf("coercePathParam(%q) = %v (%T) want %v (%T)", tc.in, got, got, tc.want, tc.want)
		}
	}
}

// TestResolveTemplates proves template resolution: a whole-string placeholder
// keeps its native type (int id for a SQL param), the "input." namespace maps to
// the Ctx.Input root, and an embedded placeholder substitutes as a string.
func TestResolveTemplates(t *testing.T) {
	c := flow.NewCtx("", "", "", map[string]any{"id": 7})
	c.Data["order"] = map[string]any{"amount": 1500}

	payload := map[string]any{
		"params": []any{"{{input.id}}"},
		"note":   "order {{order.amount}} ready",
		"nested": map[string]any{"ref": "{{order}}"},
	}
	out := resolveTemplates(payload, c).(map[string]any)

	params := out["params"].([]any)
	if params[0] != 7 {
		t.Errorf("params[0] = %v (%T) want int 7", params[0], params[0])
	}
	if out["note"] != "order 1500 ready" {
		t.Errorf("note = %q want substituted string", out["note"])
	}
	if _, ok := out["nested"].(map[string]any)["ref"].(map[string]any); !ok {
		t.Errorf("nested.ref = %v want the order map (native type preserved)", out["nested"])
	}
}

// TestNormalizeResult proves the pg []map[string]any result is bridged to the
// shapes GetPath descends: a single row to a map, many rows to a []any, and a
// non-row result (rest body) is passed through.
func TestNormalizeResult(t *testing.T) {
	single := normalizeResult([]map[string]any{{"amount": 1500}})
	if m, ok := single.(map[string]any); !ok || m["amount"] != 1500 {
		t.Errorf("single row not unwrapped to a map: %#v", single)
	}
	many := normalizeResult([]map[string]any{{"id": 1}, {"id": 2}})
	if s, ok := many.([]any); !ok || len(s) != 2 {
		t.Errorf("many rows not converted to []any: %#v", many)
	}
	passthrough := normalizeResult(map[string]any{"status": 200})
	if m, ok := passthrough.(map[string]any); !ok || m["status"] != 200 {
		t.Errorf("non-row result mangled: %#v", passthrough)
	}
}

// TestBranchingEvaluator proves a sole-value JDM output is promoted to "branch"
// while an output that already names branch/result is left untouched.
func TestBranchingEvaluator(t *testing.T) {
	be := newBranchingEvaluator(fakeEvaluator{out: map[string]any{"shipping": "expedited"}})
	out, err := be.Evaluate(context.Background(), "order", nil)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out["branch"] != "expedited" {
		t.Errorf("branch = %v want expedited", out["branch"])
	}

	be2 := newBranchingEvaluator(fakeEvaluator{out: map[string]any{"branch": "x", "shipping": "y"}})
	out2, _ := be2.Evaluate(context.Background(), "order", nil)
	if out2["branch"] != "x" {
		t.Errorf("existing branch overwritten: %v", out2["branch"])
	}
}

// fakeEvaluator returns a canned decision output.
type fakeEvaluator struct {
	out map[string]any
	err error
}

func (f fakeEvaluator) Evaluate(ctx context.Context, jdmID string, input map[string]any) (map[string]any, error) {
	return f.out, f.err
}

// compile-time: the fakes satisfy the seams they stand in for.
var (
	_ Store              = fakeStore{}
	_ decision.Evaluator = fakeEvaluator{}
	_ connect.Registry   = (*templatingRegistry)(nil)
)
