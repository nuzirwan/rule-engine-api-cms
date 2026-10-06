package httpapi

import (
	"context"
	"net/http"
	"reflect"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
)

// validateFlowRequest is the wire shape for POST /admin/flows/validate. Mode is
// selected by the fields (§2.6): stored mode = flowId + positive version;
// candidate mode = inline flow object; neither usable => 400.
type validateFlowRequest struct {
	Env     string             `json:"env,omitempty"`
	FlowID  string             `json:"flowId,omitempty"`
	Version int                `json:"version,omitempty"`
	Flow    *candidateFlowBody `json:"flow,omitempty"`
}

// candidateFlowBody is the inline candidate flow for candidate-mode validate.
type candidateFlowBody struct {
	FlowID   string         `json:"flowId"`
	Method   string         `json:"method"`
	Path     string         `json:"path"`
	Tree     flow.Node      `json:"tree"`
	Fixtures []AdminFixture `json:"fixtures,omitempty"`
}

// validateFlow validates a candidate OR stored flow version (publish-blocking).
// It ALWAYS returns 200 with an {ok, structural[], fixtures[]} body on a result;
// only a malformed request (400) or a stored-mode no-match (404) is non-200
// (§2.6). In stored mode an all-pass result flips validated=true (MarkValidated).
func (a *Admin) validateFlow(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	var req validateFlowRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.checkEnv(w, req.Env) {
		return
	}

	ctx := r.Context()
	stored := req.FlowID != "" && req.Version > 0

	var (
		tree     flow.Node
		fixtures []AdminFixture
	)
	switch {
	case stored:
		fv, err := a.store.GetFlowVersion(ctx, a.env, req.FlowID, req.Version)
		if err != nil {
			// A request to validate a thing that doesn't exist is a bad request,
			// not a negative result: NotFound => 404 (the one non-200 case).
			a.fail(w, ctx, "admin.validate", err)
			return
		}
		tree = fv.Tree
		fixtures = upMapFixtures(fv.Fixtures)
	case req.Flow != nil:
		tree = req.Flow.Tree
		fixtures = req.Flow.Fixtures
	default:
		writeError(w, http.StatusBadRequest, "invalid request: provide flowId+version or an inline flow")
		return
	}

	// 1. structural validation (collect-all), refs resolved against the store.
	refs := storeRefs{ctx: ctx, store: a.store, env: a.env}
	issues := flow.ValidateTree(tree, refs)

	// 2. fixture replay against a mock registry (zero real I/O).
	fixtureResults, allFixturesPass := a.replayFixtures(ctx, tree, fixtures)

	ok := len(issues) == 0 && allFixturesPass

	// In stored mode, an all-pass result marks the version validated so publish is
	// unblocked. A failing result changes no state (publish stays blocked).
	if stored && ok {
		if err := a.store.MarkValidated(ctx, a.env, req.FlowID, req.Version); err != nil {
			a.fail(w, ctx, "admin.validate.markValidated", err)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         ok,
		"structural": toIssueBodies(issues),
		"fixtures":   fixtureResults,
	})
}

// replayFixtures runs each fixture through the interpreter in a mocked-source
// harness and asserts its declared Expect. It returns the per-fixture result
// bodies and whether every fixture passed. A tree with no fixtures passes
// trivially.
func (a *Admin) replayFixtures(ctx context.Context, tree flow.Node, fixtures []AdminFixture) ([]map[string]any, bool) {
	results := make([]map[string]any, 0, len(fixtures))
	allPass := true
	for _, fx := range fixtures {
		passed, diff := a.replayOne(ctx, tree, fx)
		res := map[string]any{"name": fx.Name, "passed": passed}
		if !passed && diff != nil {
			res["diff"] = diff
		}
		results = append(results, res)
		if !passed {
			allPass = false
		}
	}
	return results, allPass
}

// replayOne runs a single fixture: build a Ctx from its input, run the
// interpreter over the tree with a mock registry (fixture.Mocks) and a trace
// collector, then assert Expect.Output (deep-equal vs the stitched Response). A
// run error that the fixture did NOT expect fails the fixture.
func (a *Admin) replayOne(ctx context.Context, tree flow.Node, fx AdminFixture) (bool, map[string]any) {
	c := flow.NewCtx("", "", a.env, fx.Input)
	collector := observ.NewTraceCollector(observ.NewRedactor())
	runCtx := observ.WithCollector(ctx, collector)

	deps := flow.Deps{
		Conns:  newMockRegistry(fx.Mocks),
		Decide: a.deps.Decide,
		Trace:  a.deps.Trace,
		Log:    a.deps.Log,
	}

	treeCopy := tree
	err := a.interp.Run(runCtx, &treeCopy, flow.Version{}, c, deps)
	if err != nil {
		if len(fx.Expect.Errors) > 0 {
			// A classified error was expected: the fixture passes (v1 asserts an
			// error was produced, not its exact label).
			return true, nil
		}
		return false, map[string]any{"field": "error", "expected": nil, "actual": err.Error()}
	}

	if fx.Expect.Output != nil && !reflect.DeepEqual(fx.Expect.Output, c.Response) {
		return false, map[string]any{
			"field":    "output",
			"expected": fx.Expect.Output,
			"actual":   c.Response,
		}
	}
	return true, nil
}

// upMapFixtures lifts persisted config.FlowFixtures (stored-mode validate reads
// them off the version) into AdminFixtures so the replay path is uniform. The
// persisted shape carries only name/input/want, so Expect.Output = Want and
// there are no Mocks (stored validate replays against real reads suppressed to
// mocks only when provided — stored fixtures carry none, so reads are not mocked).
func upMapFixtures(in []config.FlowFixture) []AdminFixture {
	if len(in) == 0 {
		return nil
	}
	out := make([]AdminFixture, 0, len(in))
	for _, f := range in {
		var af AdminFixture
		af.Name = f.Name
		af.Input = f.Input
		af.Expect.Output = f.Want
		out = append(out, af)
	}
	return out
}

// ---- mock registry for fixture replay (no real I/O) ----

// mockRegistry implements connect.Registry answering every op from the fixture's
// Mocks map ("conn:{key}" -> "op:{name|kind}" -> result). It performs NO network
// I/O (deterministic). It implements only the public connect seams.
type mockRegistry struct {
	mocks map[string]map[string]any
}

func newMockRegistry(mocks map[string]map[string]any) *mockRegistry {
	return &mockRegistry{mocks: mocks}
}

func (m *mockRegistry) Client(ctx context.Context, key string) (connect.Client, error) {
	return &mockClient{conn: key, mocks: m.mocks}, nil
}
func (m *mockRegistry) Reload(ctx context.Context, defs []connect.ConnectionDef) error { return nil }
func (m *mockRegistry) HealthCheck(ctx context.Context) error                          { return nil }
func (m *mockRegistry) Close() error                                                   { return nil }
func (m *mockRegistry) SecretProvider() connect.SecretProvider                         { return connect.NewEnvSecretProvider() }

// mockClient answers an Operation from the fixture mocks keyed by the op kind. A
// missing mock returns an empty result (not an error) so a validate run does not
// fail merely because a fixture omitted a mock for a read.
type mockClient struct {
	conn  string
	mocks map[string]map[string]any
}

func (c *mockClient) Execute(ctx context.Context, op connect.Operation) (any, error) {
	byOp := c.mocks["conn:"+c.conn]
	if byOp == nil {
		return map[string]any{}, nil
	}
	if v, ok := byOp["op:"+op.Kind]; ok {
		return v, nil
	}
	return map[string]any{}, nil
}

func (c *mockClient) Close() error { return nil }

// compile-time: the mock satisfies the public connect seams.
var (
	_ connect.Registry = (*mockRegistry)(nil)
	_ connect.Client   = (*mockClient)(nil)
)
