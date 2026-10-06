package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
)

// This file is the control-plane HTTP surface (slice-f-admin-api.md §2): a THIN
// HTTP + request-shape layer over the EXISTING config store methods. It contains
// no flow/business logic — the heavy lifting lives in config and the pure flow
// validator. Every /admin/* route is wrapped by the operator-auth guard
// (deny-by-default); request shapes are validated before the store is touched;
// classified store errors map to a fixed HTTP status via statusForAdmin.

// AdminStore is the narrow method-set the admin handlers depend on (DIP), so they
// stay testable with a fake. It is satisfied structurally by *config.PgStore (it
// mixes frozen-seam methods with store-internal ones that are deliberately NOT on
// the Store seam). It is NOT a frozen seam itself.
type AdminStore interface {
	// frozen Store seam:
	PutFlowVersion(ctx context.Context, env string, f config.FlowVersion) (version int, err error)
	SetActive(ctx context.Context, env, flowID string, version int) error
	Connections(ctx context.Context, env string) ([]connect.ConnectionDef, error)
	GetJDM(ctx context.Context, env, id string) (jdm []byte, version int, err error)
	ActiveFlow(ctx context.Context, env, method, path string) (config.FlowVersion, error)
	// store-internal (concrete PgStore exposes them; not on the Store seam):
	PutJDMVersion(ctx context.Context, env, jdmID string, doc []byte, version int) (int, error)
	PutConnectionVersion(ctx context.Context, env string, def connect.ConnectionDef) (int, error)
	MarkValidated(ctx context.Context, env, flowID string, version int) error
	GetFlowVersion(ctx context.Context, env, flowID string, version int) (config.FlowVersion, error)
	AuditTrail(ctx context.Context, env, objectType, objectID string) ([]config.AuditEntry, error)
	// list endpoints for CMS sync:
	ListFlows(ctx context.Context, env string) ([]config.FlowSummary, error)
	ListFlowVersions(ctx context.Context, env, flowID string) ([]config.VersionSummary, error)
	ListJDMs(ctx context.Context, env string) ([]config.JDMSummary, error)
	GetConnection(ctx context.Context, env, key string) (connect.ConnectionDef, error)
}

// compile-time assertion that the concrete *config.PgStore satisfies the admin
// method-set, so cmd/engine can pass it as both the hot-path store and deps.Admin.
var _ AdminStore = (*config.PgStore)(nil)

// knownMethods bounds the HTTP methods a flow may declare on create.
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodHead: true,
	http.MethodOptions: true,
}

// knownAuditTypes bounds {type} on GET /admin/audit/{type}/{id}.
var knownAuditTypes = map[string]bool{"flow": true, "jdm": true, "connection": true}

// Admin is the control-plane surface. It holds the admin method-set, the
// interpreter + deps for validate/dry-run, the operator-auth guard, and the
// single env this engine process serves ("").
type Admin struct {
	store  AdminStore
	interp *flow.Interpreter
	deps   Deps
	guard  *auth.OperatorGuard
	env    string
	log    observ.Logger
}

// newAdmin builds the admin surface from Deps. deps.Admin is the method-set
// (nil => admin writes return a classified "requires config-store mode" error);
// deps.OperAuth builds the operator guard (nil => plane mount-closed). The
// route->role RBAC map (requireRole) is supplied here so auth carries no routing
// knowledge.
func newAdmin(interp *flow.Interpreter, deps Deps) *Admin {
	return &Admin{
		store:  deps.Admin,
		interp: interp,
		deps:   deps,
		guard:  auth.NewOperatorGuard(deps.OperAuth, requireRole, deps.Log),
		env:    defaultEnv,
		log:    deps.Log,
	}
}

// mount registers the Go 1.22 method-aware /admin/* patterns, each wrapped by the
// operator guard. ServeMux resolves /admin/flows/validate to the literal pattern
// (more specific than {id}/publish), so "validate" is never captured as an {id}.
func (a *Admin) mount(mux *http.ServeMux) {
	h := func(fn http.HandlerFunc) http.Handler { return a.guard.Protect(fn) }
	mux.Handle("POST /admin/flows", h(a.createFlow))
	mux.Handle("POST /admin/flows/{id}/publish", h(a.publishFlow))
	mux.Handle("POST /admin/flows/{id}/rollback", h(a.rollbackFlow))
	mux.Handle("POST /admin/flows/validate", h(a.validateFlow))
	mux.Handle("POST /admin/flows/dry-run", h(a.dryRunFlow))
	mux.Handle("POST /admin/jdms", h(a.createJDM))
	mux.Handle("POST /admin/connections", h(a.createConnection))
	mux.Handle("GET /admin/connections", h(a.listConnections))
	mux.Handle("GET /admin/audit/{type}/{id}", h(a.auditTrail))
	// Read endpoints for CMS sync:
	mux.Handle("GET /admin/flows", h(a.listFlows))
	mux.Handle("GET /admin/flows/{id}", h(a.getFlow))
	mux.Handle("GET /admin/flows/{id}/versions", h(a.listFlowVersions))
	mux.Handle("GET /admin/jdms", h(a.listJdms))
	mux.Handle("GET /admin/jdms/{id}", h(a.getJdm))
	mux.Handle("GET /admin/connections/{key}", h(a.getConnection))

	// Webhook admin endpoints (protected by operator guard with webhook.read/write RBAC):
	if webhookStore, ok := a.deps.Admin.(WebhookAdminStore); ok {
		wa := newWebhookAdmin(webhookStore, a.guard, a.log)
		wa.mount(mux)
	}
}

// requireRole is the route->required-role RBAC map (slice-f-admin-api.md §3.4).
// A route not listed requires no specific role (any authenticated operator).
func requireRole(r *http.Request) string {
	path := r.URL.Path

	// Webhook routes: webhook.read for GET, webhook.write for POST/PUT/DELETE.
	if strings.HasPrefix(path, "/admin/webhooks") {
		switch r.Method {
		case http.MethodGet:
			return "webhook.read"
		case http.MethodPost, http.MethodPut, http.MethodDelete:
			return "webhook.write"
		}
	}

	// Flow routes.
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/publish"):
		return "flow.publish"
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/rollback"):
		return "flow.publish"
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/validate"):
		return "flow.read"
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/dry-run"):
		return "flow.read"
	case r.Method == http.MethodGet:
		return "flow.read"
	case r.Method == http.MethodPost:
		return "flow.write"
	default:
		return ""
	}
}

// ---- shared helpers ----

// errRequiresConfigStore is returned when deps.Admin is nil (in-memory mode): the
// admin control plane is Postgres-backed only. It is classified Internal so
// statusForAdmin maps it to 500 with a clear message.
var errRequiresConfigStore = config.ErrInternal

// requireStore returns false (after writing the error) when no admin store is
// wired. The handler must return immediately when it returns false.
func (a *Admin) requireStore(w http.ResponseWriter) bool {
	if a.store == nil {
		writeError(w, http.StatusInternalServerError, "admin requires config-store mode")
		return false
	}
	return true
}

// decodeJSON strictly decodes the request body into v, rejecting unknown fields
// so a client typo is a clean 400 rather than a silently-ignored field.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// checkEnv enforces the single-env rule (§2.9): absent/"" => ok; any non-empty
// env => 400. Returns false (after writing 400) when the env is rejected.
func (a *Admin) checkEnv(w http.ResponseWriter, env string) bool {
	if env != "" && env != a.env {
		writeError(w, http.StatusBadRequest, `engine serves env "" only`)
		return false
	}
	return true
}

// auditCtx stamps the authenticated operator subject as the audit actor so each
// store write is attributed to the operator who made it.
func (a *Admin) auditCtx(ctx context.Context) context.Context {
	if op, ok := auth.OperatorFrom(ctx); ok {
		return config.WithAuditActor(ctx, op.Subject)
	}
	return ctx
}

// fail logs the full classified error at the seam (never a secret) and writes a
// coarse client body mapped from the taxonomy.
func (a *Admin) fail(w http.ResponseWriter, ctx context.Context, label string, err error) {
	status, msg := statusForAdmin(err)
	if a.log != nil {
		a.log.Emit(ctx, "warn", label, map[string]any{
			"status":      status,
			"error_class": classLabel(err),
		})
	}
	writeError(w, status, msg)
}

// statusForAdmin maps a classified config.ConfigError (plus the edge-level
// validation cases) to an HTTP status + coarse client message (§2.9). Routing is
// by errors.Is/As, never string-match.
func statusForAdmin(err error) (int, string) {
	switch {
	case errors.Is(err, config.ErrRouteConflict):
		return http.StatusConflict, "route already owned by another flow"
	case errors.Is(err, config.ErrUnvalidated):
		return http.StatusUnprocessableEntity, "flow version not validated"
	case errors.Is(err, config.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, config.ErrValidation):
		return http.StatusBadRequest, "invalid request"
	case errors.Is(err, config.ErrTimeout):
		return http.StatusGatewayTimeout, "store timeout"
	case errors.Is(err, config.ErrUpstream):
		return http.StatusBadGateway, "store unavailable"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// classLabel renders the config error class for a log line, falling back to a
// generic label for a non-config error.
func classLabel(err error) string {
	var ce *config.ConfigError
	if errors.As(err, &ce) {
		return ce.Class.String()
	}
	return "unclassified"
}
