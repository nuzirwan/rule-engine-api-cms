package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
)

// This file holds the control-plane admin endpoints (Slice D §6b). Both are
// side-effect-free and registered on the SAME mux as the flow route, but NOT
// behind the flow auth chain (control-plane facing). They never return a 5xx for
// an expected negative outcome (a failing validate is ok:false; AC-13).

// registerAdminRoutes wires POST /admin/flows/validate and
// POST /admin/flows/dry-run onto mux.
func registerAdminRoutes(mux *http.ServeMux, store Store, interp *flow.Interpreter, deps Deps) {
	mux.HandleFunc("POST /admin/flows/validate", validateHandler(store, interp, deps))
	mux.HandleFunc("POST /admin/flows/dry-run", dryRunHandler(store, interp, deps))
}

// ---- validate ----

// validateRequest is the POST /admin/flows/validate body: a candidate flow
// version (never persisted) to check structurally and via its fixtures.
type validateRequest struct {
	Env  string                `json:"env"`
	Flow validateFlowCandidate `json:"flow"`
}

// validateFlowCandidate carries the candidate tree + fixtures to validate.
type validateFlowCandidate struct {
	FlowID   string               `json:"flowId"`
	Method   string               `json:"method"`
	Path     string               `json:"path"`
	Tree     flow.Node            `json:"tree"`
	Fixtures []config.FlowFixture `json:"fixtures,omitempty"`
}

// validateResponse is the publish-blocking result. ok is true only when there
// are no structural issues and every fixture passed. It is always HTTP 200 — a
// failing validate is an expected negative outcome, never a 5xx (AC-13).
type validateResponse struct {
	OK         bool                   `json:"ok"`
	Structural []flow.ValidationIssue `json:"structural"`
	Fixtures   []fixtureResult        `json:"fixtures"`
}

// fixtureResult reports one fixture's outcome.
type fixtureResult struct {
	Name   string         `json:"name"`
	Passed bool           `json:"passed"`
	Diff   map[string]any `json:"diff,omitempty"`
}

// validateHandler runs structural ValidateTree plus each fixture through the
// interpreter in a MOCKED-source harness (zero real I/O), collecting pass/fail +
// diffs. It responds 200 with ok=false on any failure (publish-blocking) and
// never a 5xx for a failed fixture.
func validateHandler(store Store, interp *flow.Interpreter, deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req validateRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		ctx := r.Context()

		// Structural validation, refs backed by the store (collect-all).
		refs := newStoreRefs(ctx, store, req.Env)
		structural := flow.ValidateTree(req.Flow.Tree, refs)
		if structural == nil {
			structural = []flow.ValidationIssue{}
		}

		// Run each fixture through the interpreter with a mocked registry + the
		// branch-bridging evaluator over the store (JDM eval is pure, no I/O).
		results := make([]fixtureResult, 0, len(req.Flow.Fixtures))
		allPassed := true
		for _, fx := range req.Flow.Fixtures {
			res := runFixture(ctx, interp, store, deps, req.Env, req.Flow.Tree, fx)
			if !res.Passed {
				allPassed = false
			}
			results = append(results, res)
		}

		writeJSON(w, http.StatusOK, validateResponse{
			OK:         len(structural) == 0 && allPassed,
			Structural: structural,
			Fixtures:   results,
		})
	}
}

// runFixture runs one fixture through the interpreter with a mocked registry (no
// real I/O) and the store-backed evaluator. The fixture passes when the walk
// completes without error and, if the fixture declares Want, the response
// matches. Any walk error or mismatch is a non-passing result with a diff —
// never a thrown 5xx.
func runFixture(ctx context.Context, interp *flow.Interpreter, store Store, deps Deps, env string, tree flow.Node, fx config.FlowFixture) fixtureResult {
	c := flow.NewCtx("", "", env, fx.Input)
	ctx = decision.WithEnv(ctx, env)

	runDeps := flow.Deps{
		Conns:  mockRegistry{},
		Decide: newBranchingEvaluator(deps.Decide),
		Trace:  deps.Trace,
		Log:    deps.Log,
	}

	if err := interp.Run(ctx, &tree, flow.Version{FlowID: "", Version: 0}, c, runDeps); err != nil {
		return fixtureResult{Name: fx.Name, Passed: false, Diff: map[string]any{"error": err.Error()}}
	}
	if len(fx.Want) > 0 {
		if diff := diffResponse(fx.Want, c.Response); diff != nil {
			return fixtureResult{Name: fx.Name, Passed: false, Diff: diff}
		}
	}
	return fixtureResult{Name: fx.Name, Passed: true}
}

// diffResponse returns a diff map when the actual response does not contain the
// wanted key/value pairs (shallow compare), or nil when every wanted entry
// matches. It is a presence+equality check, not a strict deep equal, so a
// fixture asserts only the fields it declares.
func diffResponse(want, got map[string]any) map[string]any {
	for k, wv := range want {
		gv, ok := got[k]
		if !ok || !jsonEqual(wv, gv) {
			return map[string]any{"expected": want, "actual": got}
		}
	}
	return nil
}

// jsonEqual compares two decoded-JSON values by their canonical JSON encoding so
// numeric types (int vs float64) and nested shapes compare structurally.
func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

// ---- dry-run ----

// dryRunRequest is the POST /admin/flows/dry-run body: resolve a stored flow and
// run it with writes suppressed, returning the node-by-node trace.
type dryRunRequest struct {
	Env     string         `json:"env"`
	FlowID  string         `json:"flowId"`
	Method  string         `json:"method"`
	Path    string         `json:"path"`
	Version int            `json:"version,omitempty"`
	Input   map[string]any `json:"input"`
}

// dryRunResponse carries the suppressed-write trace plus the stitched response.
type dryRunResponse struct {
	Trace    []observ.TraceStep `json:"trace"`
	Response map[string]any     `json:"response"`
	Errors   []string           `json:"errors"`
}

// dryRunHandler resolves the active flow for the request's route and runs it with
// observ.WithDryRun(ctx) set (so write actions are suppressed) and a collector
// attached (so the trace captures every node). Reads may hit real sources. It
// returns the node-by-node trace and performs NO write.
func dryRunHandler(store Store, interp *flow.Interpreter, deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req dryRunRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		ctx := r.Context()

		// Resolve the flow to dry-run by its route (active version).
		method, path := req.Method, req.Path
		if method == "" {
			method = http.MethodGet
		}
		fv, err := store.ActiveFlow(ctx, req.Env, method, path)
		if err != nil {
			writeError(w, statusForConfig(err), "no active flow for route")
			return
		}

		// Build the per-request Ctx + the dry-run context (suppress writes +
		// attach a collector so the trace is captured).
		c := flow.NewCtx("", "", req.Env, req.Input)
		ctx = decision.WithEnv(ctx, req.Env)
		ctx = observ.EnrichScope(ctx, fv.FlowID, fv.Version, req.Env)
		ctx = observ.WithDryRun(ctx)
		collector := observ.NewTraceCollector(nil)
		ctx = observ.WithCollector(ctx, collector)

		runDeps := flow.Deps{
			Conns:  newTemplatingRegistry(deps.Conns, c),
			Decide: newBranchingEvaluator(deps.Decide),
			Trace:  deps.Trace,
			Log:    deps.Log,
		}

		errs := []string{}
		if runErr := interp.Run(ctx, &fv.Tree, flow.Version{FlowID: fv.FlowID, Version: fv.Version}, c, runDeps); runErr != nil {
			errs = append(errs, runErr.Error())
		}

		rec := collector.Result(ctx, true)
		writeJSON(w, http.StatusOK, dryRunResponse{
			Trace:    rec.Steps,
			Response: c.Response,
			Errors:   errs,
		})
	}
}

// ---- shared helpers ----

// decodeJSON strictly decodes the request body into v, rejecting unknown fields.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// storeRefs backs flow.ValidateTree ref resolution with the config store: a
// connection exists when it appears in Connections(env); a JDM exists when
// GetJDM(env,id) resolves. Lookups are cached per request so a tree with many
// refs probes the store once per key.
type storeRefs struct {
	ctx   context.Context
	store Store
	env   string
	conns map[string]bool
	jdms  map[string]bool
}

// newStoreRefs builds a storeRefs, eagerly loading the connection key set (one
// query) and resolving JDM refs lazily.
func newStoreRefs(ctx context.Context, store Store, env string) *storeRefs {
	s := &storeRefs{ctx: ctx, store: store, env: env, conns: map[string]bool{}, jdms: map[string]bool{}}
	if defs, err := store.Connections(ctx, env); err == nil {
		for _, d := range defs {
			s.conns[d.Key] = true
		}
	}
	return s
}

// HasConnection implements flow.RefResolver.
func (s *storeRefs) HasConnection(key string) bool { return s.conns[key] }

// HasJDM implements flow.RefResolver, probing + caching GetJDM per id.
func (s *storeRefs) HasJDM(id string) bool {
	if v, ok := s.jdms[id]; ok {
		return v
	}
	_, _, err := s.store.GetJDM(s.ctx, s.env, id)
	ok := err == nil
	s.jdms[id] = ok
	return ok
}

// mockRegistry is the zero-I/O connect.Registry used by validate fixtures: every
// Client returns a mock client that answers Execute with a benign empty result,
// so the structural walk and branch selection run without touching Postgres or
// any REST endpoint. Writes are harmless (no real side effect).
type mockRegistry struct{}

// compile-time assertion that mockRegistry satisfies connect.Registry.
var _ connect.Registry = mockRegistry{}

func (mockRegistry) Client(ctx context.Context, key string) (connect.Client, error) {
	return mockClient{}, nil
}
func (mockRegistry) Reload(ctx context.Context, defs []connect.ConnectionDef) error { return nil }
func (mockRegistry) HealthCheck(ctx context.Context) error                          { return nil }

// mockClient returns an empty result for every operation (deterministic, no I/O).
type mockClient struct{}

func (mockClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	return map[string]any{}, nil
}
func (mockClient) Close() error { return nil }
