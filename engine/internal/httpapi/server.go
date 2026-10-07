// Package httpapi is the HTTP edge of the data plane (Slice E/A,
// lld-contracts.md). It owns the stdlib net/http router, resolves and PINS the
// active flow version once per request (slice-a-interpreter.md §5, AC-11), builds
// the per-request flow.Ctx from the request, runs the interpreter, and encodes
// the stitched response. It uses stdlib net/http only — no chi/gin.
//
// The router is CONFIG-DRIVEN and ZERO-RESTART: NewHandler registers a SINGLE
// catch-all handler on "/" that, on EVERY request, re-resolves the route against
// the LIVE config store (store.ActiveRoutes -> hits the Valkey cache then
// Postgres), matches the request method+path against each active flow's stored
// path pattern, lifts {name} segments into Ctx.Input, and runs the interpreter.
// Publishing a flow row makes its endpoint served on the very next request with
// NO restart or redeploy; deactivating one yields a 404 next request ("adding an
// API = adding config, no code deploy", docs/hld.md). The engine's own ops/control
// routes (/livez, /readyz, /metrics) are code-registered on the SAME mux and take
// precedence over the catch-all via ServeMux longest-pattern matching. The
// handler maps a classified engine error to an HTTP status (Validation->400,
// NotFound->404, Timeout->504, Upstream->502, Internal->500).
//
// Gateway mode: when dispatch.mode=gateway, the genericFlowHandler routes requests
// through Dispatcher to workers instead of running flow.Interpreter inline. The API
// remains transparent to consumers — same request/response format.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/gateway"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/internal/worker"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// defaultEnv is the environment the thin slice resolves flows and JDM under.
// Later increments stamp a real env from the request (ADR-006); the thin slice
// uses the empty env the seed is published under.
const defaultEnv = ""

// Store is the subset of config.Store the handler needs on the hot path: list
// the CURRENT active routes to match a request against (re-read every request so
// a newly published flow is served with no restart), and resolve+pin the active
// flow version for the matched route pattern. It is satisfied by the in-memory
// store and the real store alike.
type Store interface {
	ActiveFlow(ctx context.Context, env, method, path string) (config.FlowVersion, error)
	ActiveRoutes(ctx context.Context, env string) ([]config.RouteInfo, error)
}

// Deps carries the wired collaborators the handler threads into the interpreter
// and the ops endpoints. Conns and Decide are the frozen seams; Trace and Log are
// observability. The handler wraps Conns and Decide per request so flow templates
// resolve against the live Ctx and the decision output bridges to a condition
// branch key — both without touching the pure flow core.
//
// The ops endpoints reuse these collaborators: /readyz gates on Conns.HealthCheck
// AND Store.Ping, and /metrics scrapes Metrics. Store and Metrics are nil-safe —
// a nil Store skips the config-store readiness gate and a nil Metrics mounts
// /metrics over an empty registry.
type Deps struct {
	Conns  connect.Registry
	Decide decision.Evaluator
	Trace  observ.Tracer
	Log    observ.Logger
	Store  interface {
		Ping(ctx context.Context) error
	}
	Metrics prometheus.Gatherer

	// Admin is the control-plane method-set the /admin/* handlers depend on
	// (slice-f-admin-api.md §2.2). It is satisfied structurally by *config.PgStore;
	// cmd/engine passes the SAME store instance as both the hot-path `store`
	// argument and here. A nil Admin means admin writes return a classified
	// "requires config-store mode" error (in-memory mode), while the data plane
	// still serves.
	Admin AdminStore
	// OperAuth is the operator-plane authenticator (slice-f-admin-api.md §3). A nil
	// OperAuth mounts the operator plane CLOSED: every /admin/* request is 503
	// (deny-by-default), never mounted open.
	OperAuth auth.OperatorAuthenticator
	// RateLimit configures the token-bucket rate limiter applied to all
	// inbound requests (data-plane + admin). Env overrides: RATE_LIMIT_RPS,
	// RATE_LIMIT_BURST, RATE_LIMIT_BY_TOKEN. A zero-valued config (RPS ≤ 0)
	// disables rate limiting — the zero value is safe for tests.
	RateLimit RateLimitConfig
	// ExecTimeout is the execution timeout for scheduled flow runs (used by
	// schedule admin's /run endpoint). Defaults to 5m if zero.
	ExecTimeout time.Duration

	// Dispatcher is the gateway dispatcher for routing requests to workers when
	// dispatch.mode=gateway. Nil when mode=inline (requests executed locally).
	// Uses an interface to allow testing with mocks.
	Dispatcher Dispatcher
	// DispatchConfig holds the dispatch mode and related settings. Nil-safe — a
	// nil DispatchConfig is treated as inline mode.
	DispatchConfig *config.DispatchConfig
}

// Dispatcher is the interface for dispatching requests to workers.
type Dispatcher interface {
	Dispatch(ctx context.Context, flowID string, group string, input map[string]any) (*worker.ExecuteResponse, error)
}

// NewServer builds an *http.Server whose handler serves the config-driven routes
// plus the ops endpoints over the given store, interpreter and deps. It returns
// the NewHandler error (an unreadable route table fails fast at startup). The
// caller owns ListenAndServe and Shutdown; addr is the listen address (e.g. ":8080").
func NewServer(addr string, store Store, interp *flow.Interpreter, deps Deps) (*http.Server, error) {
	handler, err := NewHandler(store, interp, deps)
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr:    addr,
		Handler: handler,
	}, nil
}

// NewHandler builds the stdlib ServeMux with a SINGLE catch-all flow handler on
// "/" plus the code-registered ops endpoints (/livez, /readyz, /metrics). It
// never pre-registers business routes, so a flow published after startup is
// served on its next request with NO restart (zero-restart, docs/hld.md). It no
// longer reads the route table at construction, so it cannot fail on a store
// error here; the signature returns an error for forward-compatibility and so
// callers need not change again. It is exported so tests can exercise the real
// routing + Ctx construction without binding a socket.
func NewHandler(store Store, interp *flow.Interpreter, deps Deps) (http.Handler, error) {
	mux := http.NewServeMux()

	// Ops/control endpoints first — code-registered, NOT config-defined. Their
	// specific patterns win over the "/" catch-all by ServeMux longest-pattern
	// precedence, so /livez etc. never reach the flow resolver.
	ops := newOps(deps)
	ops.mount(mux)

	// Control plane: code-registered /admin/* routes, each guarded by the
	// operator-auth chain (deny-by-default). Mounted BEFORE the "/" catch-all for
	// readability — ServeMux precedence is by specificity, so the more-specific
	// /admin/* patterns win over "/" regardless of registration order
	// (slice-f-admin-api.md §2.1).
	admin := newAdmin(interp, deps)
	admin.mount(mux)

	// Webhook receiver: public endpoint, rate-limited but no operator auth.
	// POST /webhooks/{webhook_id} receives external events from providers.
	if whs, ok := deps.Admin.(WebhookStore); ok && deps.Conns != nil {
		// Get the secret provider from the registry if available.
		if rg, ok := deps.Conns.(interface{ SecretProvider() connect.SecretProvider }); ok {
			secrets := rg.SecretProvider()
			if secrets != nil {
				MountWebhookHandler(mux, WebhookHandlerDeps{
					WebhookStore: whs,
					FlowStore:    store,
					Interpreter:  interp,
					Deps:         deps,
					Secrets:      secrets,
				})
			}
		}
	}

	// Internal execute endpoint for cross-group calls (worker→gateway→worker).
	// Only mounted when Dispatcher is set (gateway mode). This endpoint accepts
	// worker.ExecuteRequest format and returns worker.ExecuteResponse.
	if deps.Dispatcher != nil {
		mux.HandleFunc("POST /internal/execute", internalExecuteHandler(store, deps))
	}

	// The single catch-all: every other request re-resolves against LIVE config.
	mux.HandleFunc("/", genericFlowHandler(store, interp, deps))

	// Wrap the entire mux with the token-bucket rate limiter so both the data
	// plane ("/") and the admin plane ("/admin/*") are protected. A nil limiter
	// (RPS ≤ 0) is a transparent no-op — existing tests that pass Deps{} are
	// unaffected. Ops endpoints (/livez, /readyz, /metrics) are also wrapped;
	// deploy behind an internal network or use a separate port to exempt them.
	rl := newRateLimiter(deps.RateLimit)
	return rateLimitMiddleware(rl)(mux), nil
}

// genericFlowHandler returns the single catch-all handler. On EVERY request it
// re-reads the CURRENT active routes from the store (hitting the Valkey cache
// then Postgres on miss), matches the request method+path against the stored
// path patterns (most-specific wins), resolves+PINS the matched flow version,
// lifts each {name} path segment into Ctx.Input, wraps the deps per request, runs
// the interpreter, and encodes the stitched Response. A path that matches no
// active route is a 404. Because the route table is read per request, publishing
// or deactivating a flow takes effect on the very next request — no restart.
//
// Gateway mode: when deps.DispatchConfig.Mode == gateway, the handler dispatches
// the request to a worker via deps.Dispatcher instead of running inline. The API
// is transparent to consumers — same request/response format.
func genericFlowHandler(store Store, interp *flow.Interpreter, deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Re-read the LIVE active routes and match this request against them. This
		// per-request read is what makes the router zero-restart.
		routes, err := store.ActiveRoutes(ctx, defaultEnv)
		if err != nil {
			writeError(w, statusForConfig(err), "cannot resolve routes")
			return
		}
		match, params, ok := matchRoute(routes, r.Method, r.URL.Path)
		if !ok {
			writeError(w, http.StatusNotFound, "no active flow for route")
			return
		}

		// Resolve + PIN the active flow version ONCE at request start (AC-11),
		// keyed by the matched route PATTERN (the stable key), not the concrete path.
		fv, err := store.ActiveFlow(ctx, defaultEnv, match.Method, match.Path)
		if err != nil {
			writeError(w, statusForConfig(err), "no active flow for route")
			return
		}

		// Parse the trigger spec to get the input extraction configuration.
		var triggerSpec flow.TriggerSpec
		if err := json.Unmarshal(fv.Tree.Spec, &triggerSpec); err != nil {
			writeError(w, http.StatusInternalServerError, "invalid trigger spec")
			return
		}

		// Extract and validate request input based on the trigger spec.
		// The compiled schema (if any) was built at flow load time.
		var compiledSchema *jsonschema.Schema
		if fv.CompiledInputSchema != nil {
			compiledSchema, _ = fv.CompiledInputSchema.(*jsonschema.Schema)
		}
		input, err := flow.ExtractAndValidate(r, params, triggerSpec.Input, compiledSchema)
		if err != nil {
			// Check if it's a validation error and return structured response.
			if verrs, ok := err.(flow.InputValidationErrors); ok {
				writeValidationErrors(w, verrs)
				return
			}
			// Other extraction errors (e.g., invalid JSON body).
			status, msg := statusForFlow(err)
			writeError(w, status, msg)
			return
		}

		// Check dispatch mode: gateway routes through dispatcher, inline runs locally.
		if deps.DispatchConfig != nil && deps.DispatchConfig.Mode == config.DispatchGateway && deps.Dispatcher != nil {
			// Gateway mode: dispatch to worker
			handleGatewayDispatch(ctx, w, r, fv, input, deps)
			return
		}

		// Inline mode: execute via interpreter locally
		handleInlineExecution(ctx, w, fv, input, interp, deps)
	}
}

// handleGatewayDispatch dispatches the request to a worker via the gateway dispatcher.
// It extracts the group from the flow, calls the dispatcher, and converts the
// worker.ExecuteResponse to an HTTP response.
func handleGatewayDispatch(ctx context.Context, w http.ResponseWriter, r *http.Request, fv config.FlowVersion, input map[string]any, deps Deps) {
	// Determine the group for this flow (fallback to default group)
	group := fv.Group
	if group == "" && deps.DispatchConfig != nil {
		group = deps.DispatchConfig.DefaultGroup
	}
	if group == "" {
		group = "default"
	}

	// Add request scope to context for trace propagation
	reqID := requestID(r)
	traceIDVal := traceID(r)
	ctx = observ.WithScope(ctx, observ.RequestScope{
		RequestID: reqID,
		TraceID:   traceIDVal,
	})

	// Dispatch to worker
	resp, err := deps.Dispatcher.Dispatch(ctx, fv.FlowID, group, input)
	if err != nil {
		status, msg := statusForDispatchError(err)
		if deps.Log != nil {
			deps.Log.Emit(ctx, "error", "httpapi.dispatch.failed", map[string]any{
				"flowId": fv.FlowID,
				"group":  group,
				"error":  err.Error(),
			})
		}
		writeError(w, status, msg)
		return
	}

	// Convert worker.ExecuteResponse to HTTP response
	if resp.Status == 0 {
		resp.Status = http.StatusOK
	}
	writeJSON(w, resp.Status, resp.Response)
}

// handleInlineExecution runs the flow via the interpreter locally.
func handleInlineExecution(ctx context.Context, w http.ResponseWriter, fv config.FlowVersion, input map[string]any, interp *flow.Interpreter, deps Deps) {
	c := flow.NewCtx("", "", defaultEnv, input)

	// Stamp env on ctx for per-env JDM resolution (ADR-006).
	ctx = decision.WithEnv(ctx, defaultEnv)

	// Per-request deps: template-resolving registry + branch-bridging
	// evaluator, both closing over this request's Ctx. The pure flow core is
	// untouched — these are edge adapters over the frozen seams.
	runDeps := flow.Deps{
		Conns:  newTemplatingRegistry(deps.Conns, c),
		Decide: newBranchingEvaluator(deps.Decide),
		Trace:  deps.Trace,
		Log:    deps.Log,
	}

	if err := interp.Run(ctx, &fv.Tree, flow.Version{FlowID: fv.FlowID, Version: fv.Version}, c, runDeps); err != nil {
		status, msg := statusForFlow(err)
		writeError(w, status, msg)
		return
	}

	writeJSON(w, http.StatusOK, c.Response)
}

// internalExecuteHandler handles POST /internal/execute for cross-group calls.
// Workers call this endpoint to dispatch requests to other workers through the
// gateway. It accepts worker.ExecuteRequest and returns worker.ExecuteResponse.
func internalExecuteHandler(store Store, deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Decode the ExecuteRequest from body
		var req worker.ExecuteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp := worker.ExecuteResponse{
				Status: http.StatusBadRequest,
				Error: &worker.ErrorDetail{
					Code:      worker.ErrCodeInvalidRequest,
					Message:   "invalid request body",
					RequestID: req.RequestID,
				},
			}
			writeJSON(w, http.StatusBadRequest, resp)
			return
		}

		// Resolve the flow to get the group
		routes, err := store.ActiveRoutes(ctx, defaultEnv)
		if err != nil {
			resp := worker.ExecuteResponse{
				Status: http.StatusInternalServerError,
				Error: &worker.ErrorDetail{
					Code:      worker.ErrCodeInternal,
					Message:   "cannot resolve routes",
					RequestID: req.RequestID,
				},
			}
			writeJSON(w, http.StatusInternalServerError, resp)
			return
		}

		// Find the flow by ID to get its group
		var fv config.FlowVersion
		var found bool
		for _, rt := range routes {
			if rt.FlowID == req.FlowID {
				fv, err = store.ActiveFlow(ctx, defaultEnv, rt.Method, rt.Path)
				if err == nil {
					found = true
					break
				}
			}
		}
		if !found {
			resp := worker.ExecuteResponse{
				Status: http.StatusNotFound,
				Error: &worker.ErrorDetail{
					Code:      worker.ErrCodeFlowNotFound,
					Message:   "flow not found",
					RequestID: req.RequestID,
				},
			}
			writeJSON(w, http.StatusNotFound, resp)
			return
		}

		// Determine the group
		group := fv.Group
		if group == "" && deps.DispatchConfig != nil {
			group = deps.DispatchConfig.DefaultGroup
		}
		if group == "" {
			group = "default"
		}

		// Add request scope to context for trace propagation
		ctx = observ.WithScope(ctx, observ.RequestScope{
			RequestID: req.RequestID,
			TraceID:   req.TraceID,
		})

		// Dispatch to worker
		resp, dispatchErr := deps.Dispatcher.Dispatch(ctx, req.FlowID, group, req.Input)
		if dispatchErr != nil {
			status, msg := statusForDispatchError(dispatchErr)
			errResp := worker.ExecuteResponse{
				Status: status,
				Error: &worker.ErrorDetail{
					Code:      classifyDispatchError(dispatchErr),
					Message:   msg,
					RequestID: req.RequestID,
				},
			}
			writeJSON(w, status, errResp)
			return
		}

		// Return the worker response as-is
		writeJSON(w, resp.Status, resp)
	}
}

// statusForDispatchError maps a dispatch error to an HTTP status and safe message.
func statusForDispatchError(err error) (int, string) {
	var dispatchErr *gateway.DispatchError
	if errors.As(err, &dispatchErr) {
		switch dispatchErr.Code {
		case gateway.ErrCodeWorkerNotFound:
			return http.StatusServiceUnavailable, "worker not found"
		case gateway.ErrCodeWorkerNotReady:
			return http.StatusServiceUnavailable, "worker not ready"
		case gateway.ErrCodeBreakerOpen:
			return http.StatusServiceUnavailable, "circuit breaker open"
		case gateway.ErrCodeTimeout:
			return http.StatusGatewayTimeout, "worker timeout"
		case gateway.ErrCodeUpstream:
			return http.StatusBadGateway, "worker error"
		case gateway.ErrCodeValidation:
			return http.StatusBadRequest, "validation error"
		case gateway.ErrCodeNotFound:
			return http.StatusNotFound, "not found"
		}
	}
	return http.StatusInternalServerError, "internal error"
}

// classifyDispatchError returns the error code for a dispatch error.
func classifyDispatchError(err error) string {
	var dispatchErr *gateway.DispatchError
	if errors.As(err, &dispatchErr) {
		return dispatchErr.Code
	}
	return worker.ErrCodeInternal
}

// matchRoute finds the active route whose method matches and whose stored path
// pattern matches reqPath, returning the matched route, the captured {name}->value
// params, and ok. Matching is segment-wise: a literal pattern segment must equal
// the request segment; a "{name}" segment captures the request segment. Segment
// counts must be equal. When several patterns match, the MOST SPECIFIC wins —
// the one with the most literal (non-wildcard) segments; ties break on the lexically
// smaller pattern for determinism (routes already arrive Path-then-Method sorted).
func matchRoute(routes []config.RouteInfo, method, reqPath string) (config.RouteInfo, map[string]string, bool) {
	reqSegs := splitPath(reqPath)

	var (
		best       config.RouteInfo
		bestParams map[string]string
		bestLits   = -1
		found      bool
	)
	for _, rt := range routes {
		if rt.Method != method {
			continue
		}
		params, lits, ok := matchPattern(rt.Path, reqSegs)
		if !ok {
			continue
		}
		if !found || lits > bestLits || (lits == bestLits && rt.Path < best.Path) {
			best, bestParams, bestLits, found = rt, params, lits, true
		}
	}
	return best, bestParams, found
}

// matchPattern matches a single stored path pattern against the already-split
// request segments, returning the captured params, the count of literal segments
// (specificity), and whether it matched.
func matchPattern(pattern string, reqSegs []string) (map[string]string, int, bool) {
	patSegs := splitPath(pattern)
	if len(patSegs) != len(reqSegs) {
		return nil, 0, false
	}
	params := map[string]string{}
	lits := 0
	for i, ps := range patSegs {
		if len(ps) >= 2 && ps[0] == '{' && ps[len(ps)-1] == '}' {
			name := strings.TrimSuffix(ps[1:len(ps)-1], "...")
			if name != "" {
				params[name] = reqSegs[i]
			}
			continue
		}
		if ps != reqSegs[i] {
			return nil, 0, false
		}
		lits++
	}
	return params, lits, true
}

// splitPath splits a URL/pattern path into its non-empty segments so a leading
// or trailing slash does not produce empty segments (e.g. "/orders/42" ->
// ["orders","42"]).
func splitPath(p string) []string {
	parts := strings.Split(p, "/")
	segs := parts[:0]
	for _, s := range parts {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// writeJSON encodes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError encodes a minimal JSON error body with the given status. The
// classified detail is deliberately coarse so no internal detail leaks to the
// client; the full classified error is logged at the seam.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// writeValidationErrors writes a structured 400 response for input validation
// errors. The response body contains field paths and messages from JSON Schema
// validation, providing actionable feedback to API consumers.
func writeValidationErrors(w http.ResponseWriter, errs flow.InputValidationErrors) {
	details := make([]map[string]string, 0, len(errs))
	for _, e := range errs {
		details = append(details, map[string]string{
			"field":   e.Field,
			"code":    e.Code,
			"message": e.Message,
		})
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error":   "invalid request",
		"details": details,
	})
}

// statusForFlow maps a classified flow error to an HTTP status and a safe
// client message. Classification is by errors.Is against the flow sentinels,
// never by string match.
func statusForFlow(err error) (int, string) {
	switch {
	case errors.Is(err, flow.ErrValidation):
		return http.StatusBadRequest, "invalid request"
	case errors.Is(err, flow.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, flow.ErrTimeout):
		return http.StatusGatewayTimeout, "upstream timeout"
	case errors.Is(err, flow.ErrUpstream):
		return http.StatusBadGateway, "upstream error"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// statusForConfig maps a config.Store resolution error to an HTTP status: a
// missing route is a 404, anything else is a 500.
func statusForConfig(err error) int {
	if errors.Is(err, config.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// requestID returns a request id from the X-Request-Id header, or empty when
// absent (the thin slice does not synthesize one).
func requestID(r *http.Request) string { return r.Header.Get("X-Request-Id") }

// traceID returns a trace id from the X-Trace-Id header, or empty when absent.
func traceID(r *http.Request) string { return r.Header.Get("X-Trace-Id") }
