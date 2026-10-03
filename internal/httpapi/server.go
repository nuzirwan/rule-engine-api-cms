// Package httpapi is the HTTP edge of the data plane (Slice E/A,
// lld-contracts.md). It owns the stdlib net/http router, resolves and PINS the
// active flow version once per request (slice-a-interpreter.md §5, AC-11), builds
// the per-request flow.Ctx from the request, runs the interpreter, and encodes
// the stitched response. It uses stdlib net/http only — no chi/gin.
//
// The thin slice exposes exactly one hard-coded route, GET /orders/{id}, and no
// auth middleware. The handler maps a classified engine error to an HTTP status
// (Validation->400, NotFound->404, Timeout->504, Upstream->502, Internal->500).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
)

// defaultEnv is the environment the thin slice resolves flows and JDM under.
// Later increments stamp a real env from the request (ADR-006); the thin slice
// uses the empty env the seed is published under.
const defaultEnv = ""

// Store is the subset of config.Store the handlers need: resolve the active flow
// for a route (flow route), plus GetJDM/Connections so the admin validate/dry-run
// endpoints can resolve stored flows, probe JDM/connection refs, and run the
// interpreter. It is satisfied by the in-memory store and the real PgStore alike.
type Store interface {
	ActiveFlow(ctx context.Context, env, method, path string) (config.FlowVersion, error)
	GetJDM(ctx context.Context, env, id string) (jdm []byte, version int, err error)
	Connections(ctx context.Context, env string) ([]connect.ConnectionDef, error)
}

// Deps carries the wired collaborators the handler threads into the interpreter.
// Conns and Decide are the frozen seams; Trace and Log are observability. The
// handler wraps Conns and Decide per request so flow templates resolve against
// the live Ctx and the decision output bridges to a condition branch key — both
// without touching the pure flow core.
type Deps struct {
	Conns  connect.Registry
	Decide decision.Evaluator
	Trace  observ.Tracer
	Log    observ.Logger

	// Auth, when non-nil, is the AuthN+AuthZ middleware mounted in front of the
	// flow route (AuthN then AuthZ). A nil Auth leaves the route unauthenticated
	// so the no-auth integration test and local dev work unchanged — auth is
	// toggleable/bypassable when unconfigured (cmd/engine logs ENABLED/DISABLED).
	Auth *auth.Middleware
}

// NewServer builds an *http.Server whose handler serves the thin-slice routes
// over the given store, interpreter and deps. The caller owns ListenAndServe and
// Shutdown; addr is the listen address (e.g. ":8080").
func NewServer(addr string, store Store, interp *flow.Interpreter, deps Deps) *http.Server {
	return &http.Server{
		Addr:    addr,
		Handler: NewHandler(store, interp, deps),
	}
}

// NewHandler builds the stdlib ServeMux with the thin-slice routes registered.
// It is exported so the integration test can exercise the real routing + Ctx
// construction without binding a socket.
func NewHandler(store Store, interp *flow.Interpreter, deps Deps) http.Handler {
	mux := http.NewServeMux()
	// Go 1.22 pattern-with-method routing: method + path pattern with a {id}
	// wildcard. This is the data-plane flow route.
	var flowHandler http.Handler = ordersHandler(store, interp, deps)
	// Auth chain in front of the flow route: AuthN first (injects the Principal),
	// then AuthZ (ZEN decision). Mounted only when configured (deps.Auth != nil);
	// otherwise the bare handler serves so the no-auth path is unchanged.
	if deps.Auth != nil {
		flowHandler = deps.Auth.Authn(deps.Auth.Authz(flowHandler))
	}
	mux.Handle("GET /orders/{id}", flowHandler)

	// Admin endpoints (Slice D §6b): structural validate + fixtures (publish-
	// blocking) and side-effect-free dry-run. They are control-plane facing and
	// NOT behind the flow auth chain (same mux, separate concern).
	registerAdminRoutes(mux, store, interp, deps)
	return mux
}

// ordersHandler returns the GET /orders/{id} handler. It resolves+pins the flow
// version once, builds Ctx from the request, wraps the deps per request, runs
// the interpreter, and encodes the stitched Response with the Response node's
// status.
func ordersHandler(store Store, interp *flow.Interpreter, deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "missing order id")
			return
		}

		// Open the request root span so the whole walk is one trace, then stamp
		// the request scope (trace_id/request_id) so every downstream span and log
		// line auto-carries it (observ reads it off ctx).
		var rootSpan observ.Span
		if deps.Trace != nil {
			ctx, rootSpan = deps.Trace.StartSpan(ctx, "http.flow", map[string]any{
				"method": r.Method,
				"route":  "/orders/{id}",
			})
			defer func() { rootSpan.End(nil) }()
		}
		ctx = observ.WithScope(ctx, observ.RequestScope{
			TraceID:   firstNonEmpty(observ.TraceIDFromContext(ctx), traceID(r)),
			RequestID: requestID(r),
			Env:       defaultEnv,
		})

		// Resolve + PIN the active flow version ONCE at request start (AC-11).
		// The matched route pattern is the stable key, not the concrete path.
		fv, err := store.ActiveFlow(ctx, defaultEnv, http.MethodGet, "/orders/{id}")
		if err != nil {
			writeError(w, statusForConfig(err), "no active flow for route")
			return
		}

		// Enrich the scope the moment the version is pinned so flow_id/
		// flow_version/environment ride on every subsequent span and log line.
		ctx = observ.EnrichScope(ctx, fv.FlowID, fv.Version, defaultEnv)

		// Build the per-request Ctx. The trigger declares input.params=["id"];
		// httpapi performs that mapping before Run (slice-a §2.1). A path param
		// arrives as a string; map a purely-numeric id to an integer so it binds
		// to the orders INT primary key as a positional SQL param (pgx rejects a
		// text value against an int4 column under the extended protocol). A
		// non-numeric id is left as a string.
		input := map[string]any{"id": coercePathParam(id)}
		c := flow.NewCtx(requestID(r), traceID(r), defaultEnv, input)

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

// coercePathParam maps a purely-numeric path param to an int so it binds to an
// integer SQL parameter; any other value (leading zero, non-numeric) is returned
// as the original string unchanged.
func coercePathParam(s string) any {
	if n, err := strconv.Atoi(s); err == nil && strconv.Itoa(n) == s {
		return n
	}
	return s
}

// firstNonEmpty returns the first non-empty string of its arguments, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// requestID returns a request id from the X-Request-Id header, or empty when
// absent (the thin slice does not synthesize one).
func requestID(r *http.Request) string { return r.Header.Get("X-Request-Id") }

// traceID returns a trace id from the X-Trace-Id header, or empty when absent.
func traceID(r *http.Request) string { return r.Header.Get("X-Trace-Id") }
