package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/observ"
)

// WebhookAdminStore is the narrow method-set the webhook admin handlers depend on.
// It is satisfied structurally by *config.PgStore. It extends the WebhookStore
// interface with additional admin-only methods.
type WebhookAdminStore interface {
	// Core CRUD:
	CreateWebhook(ctx context.Context, env string, w config.Webhook) (version int, err error)
	GetWebhook(ctx context.Context, env, webhookID string) (config.Webhook, error)
	ListWebhooks(ctx context.Context, env string) ([]config.WebhookSummary, error)
	UpdateWebhook(ctx context.Context, env string, w config.Webhook) (version int, err error)
	DeleteWebhook(ctx context.Context, env, webhookID string) error

	// Version management:
	SetWebhookActive(ctx context.Context, env, webhookID string, version int) error

	// Logs:
	GetWebhookLogs(ctx context.Context, env, webhookID string, limit int) ([]config.WebhookLog, error)
}

// compile-time assertion that the concrete *config.PgStore satisfies the admin webhook method-set.
var _ WebhookAdminStore = (*config.PgStore)(nil)

// webhookAdmin is the control-plane surface for webhook management.
type webhookAdmin struct {
	store WebhookAdminStore
	guard *auth.OperatorGuard
	env   string
	log   observ.Logger
}

// newWebhookAdmin builds the webhook admin surface.
func newWebhookAdmin(store WebhookAdminStore, guard *auth.OperatorGuard, log observ.Logger) *webhookAdmin {
	return &webhookAdmin{
		store: store,
		guard: guard,
		env:   defaultEnv,
		log:   log,
	}
}

// mount registers the webhook admin routes on the mux.
func (wa *webhookAdmin) mount(mux *http.ServeMux) {
	h := func(fn http.HandlerFunc) http.Handler { return wa.guard.Protect(fn) }
	mux.Handle("GET /admin/webhooks", h(wa.listWebhooks))
	mux.Handle("POST /admin/webhooks", h(wa.createWebhook))
	mux.Handle("GET /admin/webhooks/{id}", h(wa.getWebhook))
	mux.Handle("PUT /admin/webhooks/{id}", h(wa.updateWebhook))
	mux.Handle("DELETE /admin/webhooks/{id}", h(wa.deleteWebhook))
	mux.Handle("POST /admin/webhooks/{id}/publish", h(wa.publishWebhook))
	mux.Handle("GET /admin/webhooks/{id}/logs", h(wa.getWebhookLogs))
}

// ---- Request/Response types ----

type createWebhookRequest struct {
	ID        string              `json:"id"`                  // webhook id (caller-assigned)
	Name      string              `json:"name"`                // display name (required)
	SecretRef string              `json:"secretRef"`           // secret reference (required)
	Provider  string              `json:"provider,omitempty"`  // defaults to "generic"
	FlowID    string              `json:"flowId"`              // flow to trigger (required)
	Mapping   map[string]string   `json:"mapping,omitempty"`   // JSONPath mapping
	Filter    map[string][]string `json:"filter,omitempty"`    // filter predicates
	Env       string              `json:"env,omitempty"`       // for env validation
}

type updateWebhookRequest struct {
	Name      *string              `json:"name,omitempty"`
	SecretRef *string              `json:"secretRef,omitempty"`
	Provider  *string              `json:"provider,omitempty"`
	FlowID    *string              `json:"flowId,omitempty"`
	Mapping   *map[string]string   `json:"mapping,omitempty"`
	Filter    *map[string][]string `json:"filter,omitempty"`
}

type publishWebhookRequest struct {
	Version int `json:"version"`
}

type webhookResponse struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Provider  string              `json:"provider"`
	FlowID    string              `json:"flowId"`
	Mapping   map[string]string   `json:"mapping,omitempty"`
	Filter    map[string][]string `json:"filter,omitempty"`
	Version   int                 `json:"version"`
	Active    bool                `json:"active"`
}

// ---- Handlers ----

// listWebhooks handles GET /admin/webhooks.
func (wa *webhookAdmin) listWebhooks(w http.ResponseWriter, r *http.Request) {
	if !wa.requireStore(w) {
		return
	}
	ctx := r.Context()
	webhooks, err := wa.store.ListWebhooks(ctx, wa.env)
	if err != nil {
		wa.fail(w, ctx, "admin.listWebhooks", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": webhooks})
}

// getWebhook handles GET /admin/webhooks/{id}.
func (wa *webhookAdmin) getWebhook(w http.ResponseWriter, r *http.Request) {
	if !wa.requireStore(w) {
		return
	}
	webhookID := r.PathValue("id")
	if webhookID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing webhook id")
		return
	}

	ctx := r.Context()
	wh, err := wa.store.GetWebhook(ctx, wa.env, webhookID)
	if err != nil {
		wa.fail(w, ctx, "admin.getWebhook", err)
		return
	}

	// Redact secretRef as it's a pointer, not the value, but still sensitive info indicator.
	redactor := observ.NewRedactor()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        wh.ID,
		"name":      wh.Name,
		"secretRef": wh.SecretRef, // pointer is safe to return (not the secret value)
		"provider":  wh.Provider,
		"flowId":    wh.FlowID,
		"mapping":   redactor.Scrub(toAnyMap(wh.Mapping)),
		"filter":    wh.Filter,
		"version":   wh.Version,
		"env":       wh.Env,
	})
}

// createWebhook handles POST /admin/webhooks.
func (wa *webhookAdmin) createWebhook(w http.ResponseWriter, r *http.Request) {
	if !wa.requireStore(w) {
		return
	}
	var req createWebhookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate required fields.
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid request: name is required")
		return
	}
	if req.SecretRef == "" {
		writeError(w, http.StatusBadRequest, "invalid request: secretRef is required")
		return
	}
	if req.FlowID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: flowId is required")
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: id is required")
		return
	}

	// Default provider to generic.
	provider := req.Provider
	if provider == "" {
		provider = config.ProviderGeneric
	}
	if !isValidProvider(provider) {
		writeError(w, http.StatusBadRequest, "invalid request: unknown provider")
		return
	}

	ctx := wa.auditCtx(r.Context())
	wh := config.Webhook{
		ID:        req.ID,
		Name:      req.Name,
		SecretRef: req.SecretRef,
		Provider:  provider,
		FlowID:    req.FlowID,
		Mapping:   req.Mapping,
		Filter:    req.Filter,
		Env:       wa.env,
	}

	version, err := wa.store.CreateWebhook(ctx, wa.env, wh)
	if err != nil {
		wa.fail(w, ctx, "admin.createWebhook", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      req.ID,
		"version": version,
	})
}

// updateWebhook handles PUT /admin/webhooks/{id}.
func (wa *webhookAdmin) updateWebhook(w http.ResponseWriter, r *http.Request) {
	if !wa.requireStore(w) {
		return
	}
	webhookID := r.PathValue("id")
	if webhookID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing webhook id")
		return
	}

	var req updateWebhookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := wa.auditCtx(r.Context())

	// Fetch current webhook to merge updates.
	current, err := wa.store.GetWebhook(ctx, wa.env, webhookID)
	if err != nil {
		wa.fail(w, ctx, "admin.updateWebhook", err)
		return
	}

	// Apply partial updates.
	if req.Name != nil {
		current.Name = *req.Name
	}
	if req.SecretRef != nil {
		current.SecretRef = *req.SecretRef
	}
	if req.Provider != nil {
		if !isValidProvider(*req.Provider) {
			writeError(w, http.StatusBadRequest, "invalid request: unknown provider")
			return
		}
		current.Provider = *req.Provider
	}
	if req.FlowID != nil {
		current.FlowID = *req.FlowID
	}
	if req.Mapping != nil {
		current.Mapping = *req.Mapping
	}
	if req.Filter != nil {
		current.Filter = *req.Filter
	}

	newVersion, err := wa.store.UpdateWebhook(ctx, wa.env, current)
	if err != nil {
		wa.fail(w, ctx, "admin.updateWebhook", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      webhookID,
		"version": newVersion,
	})
}

// deleteWebhook handles DELETE /admin/webhooks/{id}.
func (wa *webhookAdmin) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	if !wa.requireStore(w) {
		return
	}
	webhookID := r.PathValue("id")
	if webhookID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing webhook id")
		return
	}

	ctx := wa.auditCtx(r.Context())
	if err := wa.store.DeleteWebhook(ctx, wa.env, webhookID); err != nil {
		wa.fail(w, ctx, "admin.deleteWebhook", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// publishWebhook handles POST /admin/webhooks/{id}/publish.
func (wa *webhookAdmin) publishWebhook(w http.ResponseWriter, r *http.Request) {
	if !wa.requireStore(w) {
		return
	}
	webhookID := r.PathValue("id")
	if webhookID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing webhook id")
		return
	}

	var req publishWebhookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Version <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request: version is required")
		return
	}

	ctx := wa.auditCtx(r.Context())
	if err := wa.store.SetWebhookActive(ctx, wa.env, webhookID, req.Version); err != nil {
		wa.fail(w, ctx, "admin.publishWebhook", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"webhookId":     webhookID,
		"activeVersion": req.Version,
		"action":        "publish",
	})
}

// getWebhookLogs handles GET /admin/webhooks/{id}/logs.
func (wa *webhookAdmin) getWebhookLogs(w http.ResponseWriter, r *http.Request) {
	if !wa.requireStore(w) {
		return
	}
	webhookID := r.PathValue("id")
	if webhookID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing webhook id")
		return
	}

	// Parse query params.
	limitStr := r.URL.Query().Get("limit")
	limit := 100 // default
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	// Status filter is accepted but not yet implemented in store layer.
	// Future enhancement: pass to store for filtering.
	// _ = r.URL.Query().Get("status")

	ctx := r.Context()
	logs, err := wa.store.GetWebhookLogs(ctx, wa.env, webhookID, limit)
	if err != nil {
		wa.fail(w, ctx, "admin.getWebhookLogs", err)
		return
	}

	// Transform logs for response (headers already redacted in store, but double-check).
	// Do NOT include raw payload - only hash.
	out := make([]map[string]any, 0, len(logs))
	for _, log := range logs {
		out = append(out, map[string]any{
			"id":            log.ID,
			"webhookId":     log.WebhookID,
			"at":            log.At,
			"provider":      log.Provider,
			"eventType":     log.EventType,
			"flowTriggered": log.FlowTriggered,
			"status":        log.Status,
			"payloadHash":   log.PayloadHash,
			"error":         log.Error,
			// Omit requestHeaders from response as they may contain sensitive data.
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"logs": out})
}

// ---- Helpers ----

// requireStore returns false when no admin store is wired.
func (wa *webhookAdmin) requireStore(w http.ResponseWriter) bool {
	if wa.store == nil {
		writeError(w, http.StatusInternalServerError, "admin requires config-store mode")
		return false
	}
	return true
}

// auditCtx stamps the authenticated operator subject as the audit actor.
func (wa *webhookAdmin) auditCtx(ctx context.Context) context.Context {
	if op, ok := auth.OperatorFrom(ctx); ok {
		return config.WithAuditActor(ctx, op.Subject)
	}
	return ctx
}

// fail logs the error and writes a coarse client body.
func (wa *webhookAdmin) fail(w http.ResponseWriter, ctx context.Context, label string, err error) {
	status, msg := statusForAdmin(err)
	if wa.log != nil {
		wa.log.Emit(ctx, "warn", label, map[string]any{
			"status":      status,
			"error_class": classLabel(err),
		})
	}
	writeError(w, status, msg)
}

// isValidProvider checks if the provider is a known type.
func isValidProvider(provider string) bool {
	switch provider {
	case config.ProviderStripe, config.ProviderGitHub, config.ProviderGeneric:
		return true
	default:
		return false
	}
}

// toAnyMap converts map[string]string to map[string]any for the redactor.
func toAnyMap(m map[string]string) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
