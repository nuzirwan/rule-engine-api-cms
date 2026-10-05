package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
)

// fakeStore returns a canned ActiveFlow result (or error) for the one route, and
// advertises that same route via ActiveRoutes so the dynamic router registers it.
type fakeStore struct {
	fv     config.FlowVersion
	err    error
	routes []config.RouteInfo // routes to register; defaults to fv's method/path
}

func (s fakeStore) ActiveFlow(ctx context.Context, env, method, path string) (config.FlowVersion, error) {
	return s.fv, s.err
}

func (s fakeStore) ActiveRoutes(ctx context.Context, env string) ([]config.RouteInfo, error) {
	if s.routes != nil {
		return s.routes, nil
	}
	// Default: register the single route the canned FlowVersion describes.
	return []config.RouteInfo{{FlowID: s.fv.FlowID, Method: s.fv.Method, Path: s.fv.Path}}, nil
}

// TestHandlerRouting proves the one hard-coded route is reachable and that a
// non-matching method/path is rejected by the stdlib ServeMux (not the handler).
func TestHandlerRouting(t *testing.T) {
	// A minimal flow whose root is a trigger with a single response child, so the
	// walk completes with no I/O: Trigger -> Response.
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{ID: "t", Type: flow.TypeTrigger, Spec: json.RawMessage(`{"method":"GET","path":"/orders/{id}","input":{"params":["id"]}}`), Children: []flow.Node{resp}}
	store := fakeStore{fv: config.FlowVersion{FlowID: "f", Version: 1, Method: "GET", Path: "/orders/{id}", Tree: trig}}

	h, err := NewHandler(store, flow.New(), Deps{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
	}{
		{"matched route", http.MethodGet, "/orders/42", http.StatusOK},
		// The catch-all receives every method; the live resolver finds no route
		// for the wrong method and returns 404 (there is no per-method ServeMux
		// registration to produce a 405).
		{"wrong method", http.MethodPost, "/orders/42", http.StatusNotFound},
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

// TestHandlerConfigError maps a store NotFound at request time (ActiveFlow) to a
// 404 at the edge, even though the route is registered from ActiveRoutes.
func TestHandlerConfigError(t *testing.T) {
	store := fakeStore{
		err:    config.ErrNotFound,
		routes: []config.RouteInfo{{FlowID: "f", Method: "GET", Path: "/orders/{id}"}},
	}
	h, err := NewHandler(store, flow.New(), Deps{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404", rec.Code)
	}
}

// TestGenericHandlerMultiWildcard proves the catch-all resolver lifts EVERY
// {name} path segment in the matched pattern into Ctx.Input by name. The flow is
// a trigger->response pair, so a 200 only results if the request matched the
// multi-wildcard pattern and ran the pinned flow.
func TestGenericHandlerMultiWildcard(t *testing.T) {
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"GET","path":"/order/msisdn/{msisdn}/{seq}","input":{"params":["msisdn","seq"]}}`),
		Children: []flow.Node{resp},
	}
	store := fakeStore{
		fv:     config.FlowVersion{FlowID: "f", Version: 1, Method: "GET", Path: "/order/msisdn/{msisdn}/{seq}", Tree: trig},
		routes: []config.RouteInfo{{FlowID: "f", Method: "GET", Path: "/order/msisdn/{msisdn}/{seq}"}},
	}

	h, err := NewHandler(store, flow.New(), Deps{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/order/msisdn/628123/9", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestDynamicRouteAbsent proves a path with no active route is a 404 — the
// catch-all resolver matched nothing in the live route table.
func TestDynamicRouteAbsent(t *testing.T) {
	store := fakeStore{routes: []config.RouteInfo{}}
	h, err := NewHandler(store, flow.New(), Deps{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/42", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404 for an unregistered route", rec.Code)
	}
}

// mutableStore is a Store whose active-route table can be swapped AT RUNTIME,
// standing in for a config publish/deactivate against the SAME running handler.
// ActiveFlow echoes the single canned flow for any resolve of a currently-active
// route pattern; an unknown pattern is a NotFound.
type mutableStore struct {
	mu     sync.RWMutex
	fv     config.FlowVersion
	routes []config.RouteInfo
}

func (s *mutableStore) setRoutes(routes []config.RouteInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = routes
}

func (s *mutableStore) ActiveRoutes(ctx context.Context, env string) ([]config.RouteInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]config.RouteInfo, len(s.routes))
	copy(out, s.routes)
	return out, nil
}

func (s *mutableStore) ActiveFlow(ctx context.Context, env, method, path string) (config.FlowVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rt := range s.routes {
		if rt.Method == method && rt.Path == path {
			return s.fv, nil
		}
	}
	return config.FlowVersion{}, config.ErrNotFound
}

var _ Store = (*mutableStore)(nil)

// TestZeroRestartRouting proves the router is zero-restart: a path 404s, then a
// flow row for that path is activated IN THE SAME RUNNING HANDLER (no rebuild,
// no restart), and the very next request is served — because every request
// re-resolves against the live store. Deactivating the route 404s again.
func TestZeroRestartRouting(t *testing.T) {
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	trig := flow.Node{
		ID:       "t",
		Type:     flow.TypeTrigger,
		Spec:     json.RawMessage(`{"method":"GET","path":"/order/{order_id}","input":{"params":["order_id"]}}`),
		Children: []flow.Node{resp},
	}
	store := &mutableStore{
		fv:     config.FlowVersion{FlowID: "f", Version: 1, Method: "GET", Path: "/order/{order_id}", Tree: trig},
		routes: nil, // start with NO active routes
	}

	h, err := NewHandler(store, flow.New(), Deps{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// 1. No route yet -> 404.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/order/42", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("pre-publish status = %d want 404", rec.Code)
	}

	// 2. Publish the flow row into the SAME running handler (no restart).
	store.setRoutes([]config.RouteInfo{{FlowID: "f", Method: "GET", Path: "/order/{order_id}"}})

	// 3. Next request is served immediately.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/order/42", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("post-publish status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// 4. Deactivate -> 404 again on the next request.
	store.setRoutes(nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/order/42", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("post-deactivate status = %d want 404", rec.Code)
	}
}

// TestOpsRoutesPrecedeCatchAll proves the code-registered ops routes win over the
// "/" catch-all even when a config flow is also active (ServeMux longest-pattern
// precedence). /livez returns the ops body, never the flow 404/200.
func TestOpsRoutesPrecedeCatchAll(t *testing.T) {
	store := fakeStore{routes: []config.RouteInfo{{FlowID: "f", Method: "GET", Path: "/livez"}}}
	h, err := NewHandler(store, flow.New(), Deps{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/livez status = %d want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("/livez body = %q want ops %q (catch-all intercepted the ops route)", rec.Body.String(), "ok")
	}
}

// TestMatchRoute covers the pattern matcher directly: literal match, single and
// multi wildcard capture, segment-count mismatch, method mismatch, and
// most-specific-wins when two patterns could match the same path.
func TestMatchRoute(t *testing.T) {
	routes := []config.RouteInfo{
		{FlowID: "a", Method: "GET", Path: "/order/{order_id}"},
		{FlowID: "b", Method: "GET", Path: "/order/msisdn/{msisdn}"},
		{FlowID: "c", Method: "GET", Path: "/order/latest"}, // more specific than /order/{order_id}
		{FlowID: "p", Method: "POST", Path: "/order/{order_id}"},
	}

	m, params, ok := matchRoute(routes, "GET", "/order/42")
	if !ok || m.FlowID != "a" || params["order_id"] != "42" {
		t.Fatalf("GET /order/42 -> %+v %v %v; want flow a order_id=42", m, params, ok)
	}

	m, params, ok = matchRoute(routes, "GET", "/order/msisdn/628123")
	if !ok || m.FlowID != "b" || params["msisdn"] != "628123" {
		t.Fatalf("GET /order/msisdn/628123 -> %+v %v %v; want flow b msisdn=628123", m, params, ok)
	}

	// Most-specific wins: /order/latest (literal) beats /order/{order_id}.
	m, _, ok = matchRoute(routes, "GET", "/order/latest")
	if !ok || m.FlowID != "c" {
		t.Fatalf("GET /order/latest -> %+v %v; want flow c (literal beats wildcard)", m, ok)
	}

	// Method mismatch for a path that only has a GET route of that shape.
	if _, _, ok := matchRoute(routes, "DELETE", "/order/42"); ok {
		t.Fatal("DELETE /order/42 matched; want no match")
	}

	// Segment-count mismatch.
	if _, _, ok := matchRoute(routes, "GET", "/order/42/extra"); ok {
		t.Fatal("GET /order/42/extra matched; want no match")
	}

	// Correct method selects the POST route.
	m, _, ok = matchRoute(routes, "POST", "/order/42")
	if !ok || m.FlowID != "p" {
		t.Fatalf("POST /order/42 -> %+v %v; want flow p", m, ok)
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

// TestPathParamsLiftAsStrings proves the edge no longer guesses a path param's
// type from its string shape: every captured {name} segment is lifted into
// Ctx.Input as a STRING in its natural wire form, including a digit-only one. A
// flow whose SQL targets a non-text column casts the PARAMETER to that type in
// its own config ($1::int), so type handling is config-driven, not an edge
// heuristic. The flow is trigger->set(echo the lifted param)->response, so the
// echoed value's type is exactly what reached Ctx.Input.
func TestPathParamsLiftAsStrings(t *testing.T) {
	resp := flow.Node{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)}
	echo := flow.Node{ID: "echo", Type: flow.TypeSet, Spec: json.RawMessage(`{"targetPath":"got","from":"id"}`), Children: []flow.Node{resp}}
	trig := flow.Node{ID: "t", Type: flow.TypeTrigger, Spec: json.RawMessage(`{"method":"GET","path":"/orders/{id}","input":{"params":["id"]}}`), Children: []flow.Node{echo}}
	store := fakeStore{fv: config.FlowVersion{FlowID: "f", Version: 1, Method: "GET", Path: "/orders/{id}", Tree: trig}}

	h, err := NewHandler(store, flow.New(), Deps{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// A digit-only segment is lifted as the STRING "1500", not an int.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/1500", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if derr := json.Unmarshal(rec.Body.Bytes(), &body); derr != nil {
		t.Fatalf("decode body: %v", derr)
	}
	if body["got"] != "1500" {
		t.Fatalf("echoed param = %#v want the string \"1500\" (no shape-based int coercion)", body["got"])
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
// non-row result (rest body) is passed through. The unwrapSingleRow parameter
// controls whether single rows are unwrapped (nil/true) or kept as arrays (false).
func TestNormalizeResult(t *testing.T) {
	// Default (nil): single row unwrapped to map
	single := normalizeResult([]map[string]any{{"amount": 1500}}, nil)
	if m, ok := single.(map[string]any); !ok || m["amount"] != 1500 {
		t.Errorf("single row not unwrapped to a map: %#v", single)
	}

	// Explicit true: same as nil
	tr := true
	singleTrue := normalizeResult([]map[string]any{{"amount": 1500}}, &tr)
	if m, ok := singleTrue.(map[string]any); !ok || m["amount"] != 1500 {
		t.Errorf("single row with true not unwrapped: %#v", singleTrue)
	}

	// Explicit false: single row stays as array
	fa := false
	singleFalse := normalizeResult([]map[string]any{{"amount": 1500}}, &fa)
	if s, ok := singleFalse.([]any); !ok || len(s) != 1 {
		t.Errorf("single row with false should be array: %#v", singleFalse)
	}

	// Many rows always converted to []any
	many := normalizeResult([]map[string]any{{"id": 1}, {"id": 2}}, nil)
	if s, ok := many.([]any); !ok || len(s) != 2 {
		t.Errorf("many rows not converted to []any: %#v", many)
	}

	// Empty rows: depends on unwrap setting
	emptyUnwrap := normalizeResult([]map[string]any{}, nil)
	if _, ok := emptyUnwrap.(map[string]any); !ok {
		t.Errorf("empty rows with unwrap should be empty map: %#v", emptyUnwrap)
	}
	emptyNoUnwrap := normalizeResult([]map[string]any{}, &fa)
	if s, ok := emptyNoUnwrap.([]any); !ok || len(s) != 0 {
		t.Errorf("empty rows with no unwrap should be empty array: %#v", emptyNoUnwrap)
	}

	// Non-row result passed through unchanged
	passthrough := normalizeResult(map[string]any{"status": 200}, nil)
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
