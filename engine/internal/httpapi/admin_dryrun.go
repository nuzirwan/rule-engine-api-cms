package httpapi

import (
	"net/http"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
)

// dryRunRequest is the wire shape for POST /admin/flows/dry-run. version is
// optional (defaults to the active version); mocks is optional (omitted => reads
// hit real sources via deps.Conns, writes always suppressed). Writes are ALWAYS
// suppressed under dry-run (AC-14).
type dryRunRequest struct {
	Env     string                    `json:"env,omitempty"`
	FlowID  string                    `json:"flowId,omitempty"`
	Version int                       `json:"version,omitempty"`
	Input   dryRunInput               `json:"input"`
	Mocks   map[string]map[string]any `json:"mocks,omitempty"`
}

// dryRunInput carries the request-shaped input the data-plane edge would lift.
type dryRunInput struct {
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Params  map[string]any    `json:"params,omitempty"`
	Body    map[string]any    `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// dryRunFlow runs a flow with writes suppressed and returns the trace + the
// stitched response. It ALWAYS returns 200 with {trace, response, errors[]} on a
// result; a flow error is summarized in errors[] (dry-run is a debugger). A bad
// request shape (no resolvable flow) is a 400 (§2.7).
func (a *Admin) dryRunFlow(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	var req dryRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.checkEnv(w, req.Env) {
		return
	}

	ctx := r.Context()

	// Resolve the flow version: a positive version loads that exact version; else
	// default to the active version. Both need an identifier to resolve against.
	var (
		fv  config.FlowVersion
		err error
	)
	switch {
	case req.FlowID != "" && req.Version > 0:
		fv, err = a.store.GetFlowVersion(ctx, a.env, req.FlowID, req.Version)
	case req.Input.Method != "" && req.Input.Path != "":
		fv, err = a.store.ActiveFlow(ctx, a.env, req.Input.Method, req.Input.Path)
	default:
		writeError(w, http.StatusBadRequest, "invalid request: provide flowId+version or input.method+input.path")
		return
	}
	if err != nil {
		a.fail(w, ctx, "admin.dryRun.resolve", err)
		return
	}

	// Build the Ctx from the request params (same lift the data-plane edge does).
	input := map[string]any{}
	for k, v := range req.Input.Params {
		input[k] = v
	}
	if len(req.Input.Body) > 0 {
		input["body"] = req.Input.Body
	}
	c := flow.NewCtx("", "", a.env, input)

	// Attach the dry-run flag AND a trace collector. With the flow.actionHandler
	// fix, write ops record wrote:"suppressed" and perform no I/O (AC-14).
	collector := observ.NewTraceCollector(observ.NewRedactor())
	runCtx := observ.WithCollector(observ.WithDryRun(ctx), collector)

	// Reads: a mocks map (when provided) answers reads deterministically; else
	// reads hit the real wired sources (deps.Conns). Writes are suppressed either
	// way by the dry-run flag.
	var conns = a.deps.Conns
	if len(req.Mocks) > 0 {
		conns = newMockRegistry(req.Mocks)
	}
	deps := flow.Deps{
		Conns:  conns,
		Decide: a.deps.Decide,
		Trace:  a.deps.Trace,
		Log:    a.deps.Log,
	}

	tree := fv.Tree
	runErr := a.interp.Run(runCtx, &tree, flow.Version{FlowID: fv.FlowID, Version: fv.Version}, c, deps)

	rec := collector.Result(runCtx, true)
	errsOut := []string{}
	if runErr != nil {
		errsOut = append(errsOut, runErr.Error())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"trace":    rec.Steps,
		"response": c.Response,
		"errors":   errsOut,
	})
}
