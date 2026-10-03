package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
)

// adminStore is a configurable fake Store for the admin endpoints: it serves a
// canned ActiveFlow, a known JDM id set, and a known connection key set so
// structural ref resolution and dry-run resolution are deterministic.
type adminStore struct {
	fv       config.FlowVersion
	fvErr    error
	jdms     map[string]bool
	connKeys []string
}

func (s adminStore) ActiveFlow(ctx context.Context, env, method, path string) (config.FlowVersion, error) {
	return s.fv, s.fvErr
}
func (s adminStore) GetJDM(ctx context.Context, env, id string) ([]byte, int, error) {
	if s.jdms[id] {
		return []byte(`{}`), 1, nil
	}
	return nil, 0, config.ErrNotFound
}
func (s adminStore) Connections(ctx context.Context, env string) ([]connect.ConnectionDef, error) {
	out := make([]connect.ConnectionDef, 0, len(s.connKeys))
	for _, k := range s.connKeys {
		out = append(out, connect.ConnectionDef{Key: k, Type: "rest"})
	}
	return out, nil
}

// mkRaw marshals v into a json.RawMessage for a node Spec.
func mkRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// goodTree builds a structurally-valid Trigger -> Set -> Response tree (no I/O
// refs) so validate passes structurally.
func goodTree(t *testing.T) flow.Node {
	t.Helper()
	// Set and Response are both leaves (no children); the Trigger runs its
	// children linearly, so Set then Response on one path with Response terminal.
	set := flow.Node{ID: "set", Type: flow.TypeSet,
		Spec: mkRaw(t, flow.SetSpec{TargetPath: "ok", Value: true})}
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: mkRaw(t, flow.ResponseSpec{Status: 200})}
	return flow.Node{ID: "trig", Type: flow.TypeTrigger,
		Spec:     mkRaw(t, flow.TriggerSpec{Method: "POST", Path: "/x"}),
		Children: []flow.Node{set, resp}}
}

// writeTree builds Trigger -> Action(write, http) -> Response, with the write
// action saving under "decision". It is used by the dry-run suppression test.
func writeTree(t *testing.T) flow.Node {
	t.Helper()
	// Action and Response are leaves; the Trigger runs them linearly so the write
	// action executes (or is suppressed) then the Response terminates the path.
	action := flow.Node{ID: "post", Type: flow.TypeAction,
		Spec: mkRaw(t, flow.ActionSpec{
			ConnRef:   flow.ConnRef{Connection: "ship-rest"},
			Operation: connect.Operation{Kind: "http"},
			SaveAs:    "decision",
		})}
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: mkRaw(t, flow.ResponseSpec{Status: 200})}
	return flow.Node{ID: "trig", Type: flow.TypeTrigger,
		Spec:     mkRaw(t, flow.TriggerSpec{Method: "POST", Path: "/x"}),
		Children: []flow.Node{action, resp}}
}

func postJSON(t *testing.T, h http.Handler, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(b))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestAdminValidate_StructuralBadTreeBlocks proves a structurally-bad tree
// returns ok:false with a structural issue and HTTP 200 (never a 5xx).
func TestAdminValidate_StructuralBadTreeBlocks(t *testing.T) {
	store := adminStore{}
	h := NewHandler(store, flow.New(), Deps{})

	// A root that is NOT a trigger and has no response: structural failures.
	badTree := flow.Node{ID: "x", Type: flow.TypeSet,
		Spec: mkRaw(t, flow.SetSpec{TargetPath: "a", Value: 1})}

	rec := postJSON(t, h, "/admin/flows/validate", validateRequest{
		Flow: validateFlowCandidate{FlowID: "f", Tree: badTree},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp validateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK {
		t.Fatalf("ok = true, want false for a bad tree")
	}
	if len(resp.Structural) == 0 {
		t.Fatalf("expected structural issues, got none")
	}
}

// TestAdminValidate_FailingFixtureBlocks proves a structurally-valid tree with a
// fixture whose Want does not match returns ok:false with a fixture diff and
// HTTP 200.
func TestAdminValidate_FailingFixtureBlocks(t *testing.T) {
	store := adminStore{}
	h := NewHandler(store, flow.New(), Deps{})

	rec := postJSON(t, h, "/admin/flows/validate", validateRequest{
		Flow: validateFlowCandidate{
			FlowID: "f",
			Tree:   goodTree(t),
			Fixtures: []config.FlowFixture{
				{Name: "wants wrong value", Input: map[string]any{}, Want: map[string]any{"ok": false}},
			},
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp validateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK {
		t.Fatalf("ok = true, want false for a failing fixture")
	}
	if len(resp.Fixtures) != 1 || resp.Fixtures[0].Passed {
		t.Fatalf("fixture result = %#v, want one failing", resp.Fixtures)
	}
	if resp.Fixtures[0].Diff == nil {
		t.Fatalf("expected a diff on the failing fixture")
	}
}

// TestAdminValidate_GoodFlowPasses proves a structurally-valid tree with a
// passing fixture returns ok:true.
func TestAdminValidate_GoodFlowPasses(t *testing.T) {
	store := adminStore{}
	h := NewHandler(store, flow.New(), Deps{})

	rec := postJSON(t, h, "/admin/flows/validate", validateRequest{
		Flow: validateFlowCandidate{
			FlowID: "f",
			Tree:   goodTree(t),
			Fixtures: []config.FlowFixture{
				{Name: "ok is true", Input: map[string]any{}, Want: map[string]any{"ok": true}},
			},
		},
	})

	var resp validateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Fatalf("ok = false, want true (structural=%v fixtures=%v)", resp.Structural, resp.Fixtures)
	}
}

// TestAdminDryRun_SuppressesWriteAndTraces proves a dry-run over a write flow
// returns a node trace, records the write node as suppressed, and the fake
// client's Execute was NOT called for the write (no side effect).
func TestAdminDryRun_SuppressesWriteAndTraces(t *testing.T) {
	fc := &countingClient{}
	store := adminStore{
		fv: config.FlowVersion{FlowID: "f", Version: 1, Method: "POST", Path: "/x", Tree: writeTree(t)},
	}
	h := NewHandler(store, flow.New(), Deps{Conns: countingRegistry{client: fc}})

	rec := postJSON(t, h, "/admin/flows/dry-run", dryRunRequest{
		Env: "", FlowID: "f", Method: "POST", Path: "/x", Input: map[string]any{},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp dryRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Trace) == 0 {
		t.Fatalf("expected a non-empty trace")
	}
	if fc.calls != 0 {
		t.Fatalf("write op executed %d times under dry-run, want 0", fc.calls)
	}
	// The write node is recorded as suppressed.
	foundSuppressed := false
	for _, s := range resp.Trace {
		if s.NodeID == "post" && s.Attrs != nil && s.Attrs["wrote"] == "suppressed" {
			foundSuppressed = true
		}
	}
	if !foundSuppressed {
		t.Fatalf("write node not recorded as suppressed: %#v", resp.Trace)
	}
}

// countingRegistry/countingClient count Execute calls so a test can assert a
// write was suppressed (zero calls).
type countingRegistry struct{ client *countingClient }

func (r countingRegistry) Client(ctx context.Context, key string) (connect.Client, error) {
	return r.client, nil
}
func (r countingRegistry) Reload(ctx context.Context, defs []connect.ConnectionDef) error { return nil }
func (r countingRegistry) HealthCheck(ctx context.Context) error                          { return nil }

type countingClient struct{ calls int }

func (c *countingClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	c.calls++
	return map[string]any{}, nil
}
func (c *countingClient) Close() error { return nil }

// compile-time: the admin fakes satisfy the seams they stand in for.
var (
	_ Store            = adminStore{}
	_ connect.Registry = countingRegistry{}
)
