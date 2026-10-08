package httpapi

import (
	"encoding/json"
	"net/http"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/observ"
)

// ---- POST /admin/flows -> PutFlowVersion ----

// createFlowRequest is the wire shape for creating a flow version. The store
// assigns the version (max+1), so a client-supplied version is ignored.
type createFlowRequest struct {
	Env      string          `json:"env,omitempty"`
	FlowID   string          `json:"flowId"`
	Method   string          `json:"method"`
	Path     string          `json:"path"`
	Tree     flow.Node       `json:"tree"`
	Fixtures []AdminFixture  `json:"fixtures,omitempty"`
	Note     string          `json:"note,omitempty"`
	Version  json.RawMessage `json:"version,omitempty"` // accepted + ignored (store assigns)
}

func (a *Admin) createFlow(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	var req createFlowRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.checkEnv(w, req.Env) {
		return
	}
	if req.FlowID == "" || req.Method == "" || req.Path == "" {
		writeError(w, http.StatusBadRequest, "invalid request: flowId, method and path are required")
		return
	}
	if !knownMethods[req.Method] {
		writeError(w, http.StatusBadRequest, "invalid request: unknown method")
		return
	}

	// Structural + dangling-ref validation BEFORE the store is touched, so a bad
	// tree never reaches Postgres.
	ctx := a.auditCtx(r.Context())
	refs := storeRefs{ctx: ctx, store: a.store, env: a.env}
	if issues := flow.ValidateTree(req.Tree, refs); len(issues) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":  "flow tree invalid",
			"issues": toIssueBodies(issues),
		})
		return
	}

	fv := config.FlowVersion{
		FlowID:   req.FlowID,
		Method:   req.Method,
		Path:     req.Path,
		Tree:     req.Tree,
		Fixtures: downMapFixtures(req.Fixtures),
	}
	version, err := a.store.PutFlowVersion(ctx, a.env, fv)
	if err != nil {
		a.fail(w, ctx, "admin.createFlow", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"flowId":    req.FlowID,
		"version":   version,
		"validated": false,
	})
}

// ---- POST /admin/flows/{id}/publish and /rollback -> SetActive ----

type setActiveRequest struct {
	Env     string `json:"env,omitempty"`
	Version int    `json:"version"`
	Reason  string `json:"reason,omitempty"`
}

func (a *Admin) publishFlow(w http.ResponseWriter, r *http.Request)  { a.setActive(w, r, "publish") }
func (a *Admin) rollbackFlow(w http.ResponseWriter, r *http.Request) { a.setActive(w, r, "rollback") }

// setActive is the shared publish/rollback handler. The response `action` is
// DERIVED FROM THE ROUTE (SetActive returns only an error; the audit log is the
// source of truth for the effective action — §2.4/§2.5). A publish of an
// un-validated version maps to 422 (publish-blocking, AC-13).
func (a *Admin) setActive(w http.ResponseWriter, r *http.Request, action string) {
	if !a.requireStore(w) {
		return
	}
	flowID := r.PathValue("id")
	if flowID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing flow id")
		return
	}
	var req setActiveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.checkEnv(w, req.Env) {
		return
	}
	if req.Version <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request: version must be a positive int")
		return
	}

	ctx := a.auditCtx(r.Context())
	if err := a.store.SetActive(ctx, a.env, flowID, req.Version); err != nil {
		// A publish of an un-validated version surfaces as config.ErrUnvalidated,
		// which statusForAdmin maps to 422 (publish-blocking, AC-13).
		a.fail(w, ctx, "admin.setActive", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"flowId":        flowID,
		"activeVersion": req.Version,
		"action":        action,
	})
}

// ---- POST /admin/jdms -> PutJDMVersion ----

type createJDMRequest struct {
	Env     string          `json:"env,omitempty"`
	JDMID   string          `json:"jdmId"`
	Doc     json.RawMessage `json:"doc"`
	Version int             `json:"version,omitempty"`
	Note    string          `json:"note,omitempty"`
}

func (a *Admin) createJDM(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	var req createJDMRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.checkEnv(w, req.Env) {
		return
	}
	if req.JDMID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: jdmId is required")
		return
	}
	if jsonRawEmpty(req.Doc) {
		writeError(w, http.StatusBadRequest, "invalid request: doc is required")
		return
	}

	ctx := a.auditCtx(r.Context())
	version, err := a.store.PutJDMVersion(ctx, a.env, req.JDMID, req.Doc, req.Version)
	if err != nil {
		a.fail(w, ctx, "admin.createJDM", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"jdmId": req.JDMID, "version": version})
}

// ---- POST /admin/connections -> PutConnectionVersion (secret_ref ONLY) ----

type createConnectionRequest struct {
	Env        string                    `json:"env,omitempty"`
	Key        string                    `json:"key"`
	Type       string                    `json:"type"`
	Settings   map[string]any            `json:"settings,omitempty"`
	SecretRef  string                    `json:"secretRef,omitempty"`  // legacy single secret ref (backward compat)
	SecretRefs map[string]string         `json:"secretRefs,omitempty"` // multi-secret refs keyed by role (e.g. {"password": "env:X", "apiKey": "env:Y"})
	Resilience *connect.ResiliencePolicy `json:"resilience,omitempty"`
	// The secret-value fields below are REJECTED if present; they are declared so
	// strict decoding does not fail with "unknown field" but the handler can tell
	// a client it must use secretRef (AC-20). They are never stored.
	Password rejectIfPresent `json:"password,omitempty"`
	Secret   rejectIfPresent `json:"secret,omitempty"`
	Token    rejectIfPresent `json:"token,omitempty"`
	APIKey   rejectIfPresent `json:"apiKey,omitempty"`
}

func (a *Admin) createConnection(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	var req createConnectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !a.checkEnv(w, req.Env) {
		return
	}
	if req.Key == "" || req.Type == "" {
		writeError(w, http.StatusBadRequest, "invalid request: key and type are required")
		return
	}
	// Reject any top-level secret-value field, or a secret value nested in
	// settings (e.g. settings.password): secret_ref ONLY on the wire (AC-20).
	if req.Password.present || req.Secret.present || req.Token.present || req.APIKey.present ||
		hasSecretValueInSettings(req.Settings) {
		writeError(w, http.StatusBadRequest, "secret value not allowed; use secretRef")
		return
	}

	def := connect.ConnectionDef{
		Key:        req.Key,
		Type:       req.Type,
		Settings:   req.Settings,
		SecretRef:  req.SecretRef,
		SecretRefs: req.SecretRefs,
	}
	if req.Resilience != nil {
		def.Resilience = *req.Resilience
	}

	ctx := a.auditCtx(r.Context())
	version, err := a.store.PutConnectionVersion(ctx, a.env, def)
	if err != nil {
		a.fail(w, ctx, "admin.createConnection", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": req.Key, "version": version})
}

// ---- GET /admin/connections -> Connections (redacted backstop) ----

func (a *Admin) listConnections(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	ctx := r.Context()
	defs, err := a.store.Connections(ctx, a.env)
	if err != nil {
		a.fail(w, ctx, "admin.listConnections", err)
		return
	}
	// Backstop redaction (AC-20): a connection def should carry only non-secret
	// data (settings + a secret_ref POINTER, never a value), but as a defense in
	// depth the free-form settings map is run through the central Redactor so a
	// stray secret-looking value (e.g. an operator mistakenly stored a password in
	// settings) is masked. The explicit top-level fields are known-safe and NOT
	// run through the Redactor, because secretRef is a non-secret pointer whose
	// key ("secretRef") would otherwise be masked by the deny-list's "secret"
	// substring rule — it must survive (slice-f-admin-api.md §2.8).
	redactor := observ.NewRedactor()
	out := make([]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			"key":        d.Key,
			"type":       d.Type,
			"settings":   redactor.Scrub(d.Settings),
			"secretRef":  d.SecretRef,
			"secretRefs": d.SecretRefs,
			"resilience": d.Resilience,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

// ---- GET /admin/audit/{type}/{id} -> AuditTrail ----

func (a *Admin) auditTrail(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	objectType := r.PathValue("type")
	objectID := r.PathValue("id")
	if !knownAuditTypes[objectType] {
		writeError(w, http.StatusBadRequest, "invalid request: unknown audit type")
		return
	}
	if objectID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing object id")
		return
	}

	ctx := r.Context()
	entries, err := a.store.AuditTrail(ctx, a.env, objectType, objectID)
	if err != nil {
		a.fail(w, ctx, "admin.auditTrail", err)
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"action":      e.Action,
			"fromVersion": e.FromVersion, // nil => JSON null
			"toVersion":   e.ToVersion,   // nil => JSON null
			"actor":       e.Actor,
			"at":          e.At,
			"reason":      e.Reason,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"objectType": objectType,
		"objectId":   objectID,
		"entries":    out,
	})
}

// ---- GET /admin/flows -> ListFlows ----

func (a *Admin) listFlows(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	ctx := r.Context()
	flows, err := a.store.ListFlows(ctx, a.env)
	if err != nil {
		a.fail(w, ctx, "admin.listFlows", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"flows": flows})
}

// ---- GET /admin/flows/{id} -> GetFlowVersion (active version) ----

func (a *Admin) getFlow(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	flowID := r.PathValue("id")
	if flowID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing flow id")
		return
	}

	ctx := r.Context()
	// Get versions list to find the active version.
	versions, err := a.store.ListFlowVersions(ctx, a.env, flowID)
	if err != nil {
		a.fail(w, ctx, "admin.getFlow", err)
		return
	}
	if len(versions) == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// Get the flow summary to find active version.
	flows, err := a.store.ListFlows(ctx, a.env)
	if err != nil {
		a.fail(w, ctx, "admin.getFlow", err)
		return
	}
	var activeVersion *int
	for _, f := range flows {
		if f.ID == flowID {
			activeVersion = f.ActiveVersion
			break
		}
	}

	// Get the flow version body (active version if published, otherwise latest).
	version := versions[0].Version // Latest version.
	if activeVersion != nil {
		version = *activeVersion
	}
	fv, err := a.store.GetFlowVersion(ctx, a.env, flowID, version)
	if err != nil {
		a.fail(w, ctx, "admin.getFlow", err)
		return
	}
	writeJSON(w, http.StatusOK, fv)
}

// ---- GET /admin/flows/{id}/versions -> ListFlowVersions ----

func (a *Admin) listFlowVersions(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	flowID := r.PathValue("id")
	if flowID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing flow id")
		return
	}

	ctx := r.Context()
	versions, err := a.store.ListFlowVersions(ctx, a.env, flowID)
	if err != nil {
		a.fail(w, ctx, "admin.listFlowVersions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"flowId":   flowID,
		"versions": versions,
	})
}

// ---- GET /admin/jdms -> ListJDMs ----

func (a *Admin) listJdms(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	ctx := r.Context()
	jdms, err := a.store.ListJDMs(ctx, a.env)
	if err != nil {
		a.fail(w, ctx, "admin.listJdms", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jdms": jdms})
}

// ---- GET /admin/jdms/{id} -> GetJDM ----

func (a *Admin) getJdm(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	jdmID := r.PathValue("id")
	if jdmID == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing jdm id")
		return
	}

	ctx := r.Context()
	doc, version, err := a.store.GetJDM(ctx, a.env, jdmID)
	if err != nil {
		a.fail(w, ctx, "admin.getJdm", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jdmId":   jdmID,
		"version": version,
		"doc":     json.RawMessage(doc),
	})
}

// ---- GET /admin/connections/{key} -> GetConnection (redacted) ----

func (a *Admin) getConnection(w http.ResponseWriter, r *http.Request) {
	if !a.requireStore(w) {
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing connection key")
		return
	}

	ctx := r.Context()
	def, err := a.store.GetConnection(ctx, a.env, key)
	if err != nil {
		a.fail(w, ctx, "admin.getConnection", err)
		return
	}

	// Backstop redaction (same as listConnections).
	redactor := observ.NewRedactor()
	writeJSON(w, http.StatusOK, map[string]any{
		"key":        def.Key,
		"type":       def.Type,
		"settings":   redactor.Scrub(def.Settings),
		"secretRef":  def.SecretRef,
		"secretRefs": def.SecretRefs,
		"resilience": def.Resilience,
	})
}

// ---- POST /admin/connections/test -> Test connection (ephemeral) ----

// testConnectionRequest is the wire shape for testing a connection without
// persisting it. The secret is passed in plaintext (never stored, only used
// transiently for the test). Supports both legacy single secret (Secret field)
// and multi-secret (Secrets map) formats.
type testConnectionRequest struct {
	Type     string            `json:"type"`
	Settings map[string]any    `json:"settings,omitempty"`
	Secret   string            `json:"secret,omitempty"`  // legacy single secret (backward compat)
	Secrets  map[string]string `json:"secrets,omitempty"` // multi-secret map keyed by role (e.g. {"password": "p", "username": "u"})
}

// testConnectionResponse is the wire shape for the test connection result.
type testConnectionResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (a *Admin) testConnection(w http.ResponseWriter, r *http.Request) {
	var req testConnectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Type == "" {
		writeError(w, http.StatusBadRequest, "invalid request: type is required")
		return
	}

	// Look up the connector by type from the registry.
	lookup, ok := a.deps.Conns.(connect.ConnectorLookup)
	if !ok || a.deps.Conns == nil {
		writeError(w, http.StatusInternalServerError, "connector registry unavailable")
		return
	}
	connector, found := lookup.Connector(req.Type)
	if !found {
		writeError(w, http.StatusBadRequest, "unknown connection type: "+req.Type)
		return
	}

	// Build a temporary ConnectionDef for the test.
	def := connect.ConnectionDef{
		Key:      "_test",
		Type:     req.Type,
		Settings: req.Settings,
	}

	// Inject secrets via context. Prefer Secrets map over legacy Secret string.
	ctx := r.Context()
	if len(req.Secrets) > 0 {
		// Multi-secret: convert map[string]string to map[string]connect.Secret
		secrets := make(map[string]connect.Secret, len(req.Secrets))
		for k, v := range req.Secrets {
			secrets[k] = connect.NewPlainSecret(v)
		}
		ctx = connect.WithSecrets(ctx, secrets)
		// Also set legacy WithSecret with "password" key for backward compat
		if pw, ok := secrets["password"]; ok {
			ctx = connect.WithSecret(ctx, pw)
		}
	} else if req.Secret != "" {
		// Legacy single secret: inject as both WithSecret and WithSecrets{"password": secret}
		plainSecret := connect.NewPlainSecret(req.Secret)
		ctx = connect.WithSecret(ctx, plainSecret)
		ctx = connect.WithSecrets(ctx, map[string]connect.Secret{"password": plainSecret})
	}

	// Open the connector to get a client.
	client, err := connector.Open(ctx, def)
	if err != nil {
		writeJSON(w, http.StatusOK, testConnectionResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}
	defer client.Close()

	// Run a health check (ping).
	_, err = client.Execute(ctx, connect.Operation{Kind: "ping"})
	if err != nil {
		writeJSON(w, http.StatusOK, testConnectionResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, testConnectionResponse{
		Success: true,
		Message: "Connection successful",
	})
}

// ---- GET /admin/connectors/schema -> List all connector schemas ----

// connectorSchemaEntry is the wire shape for a single connector's schema.
// Used as the value in the connectors map response.
type connectorSchemaEntry struct {
	Secrets []connect.SecretField `json:"secrets"` // nil means dynamic secrets
}

// connectorSchemaResponse is the wire shape for a single connector type lookup.
// Used by GET /admin/connectors/{type}/schema.
type connectorSchemaResponse struct {
	Type    string                `json:"type"`
	Secrets []connect.SecretField `json:"secrets"` // nil means dynamic secrets
}

// listConnectorSchemas returns the secret schema for all registered connectors.
// Used by the CMS to build dynamic secret-entry forms.
// Response: {"connectors": {"postgres": {"secrets": [...]}, "rest": {"secrets": null}, ...}}
func (a *Admin) listConnectorSchemas(w http.ResponseWriter, r *http.Request) {
	// Get the connector registry to enumerate all connectors.
	registry, ok := a.deps.Conns.(connect.ConnectorRegistry)
	if !ok || a.deps.Conns == nil {
		writeError(w, http.StatusInternalServerError, "connector registry unavailable")
		return
	}

	connectors := registry.AllConnectors()
	schemas := make(map[string]connectorSchemaEntry, len(connectors))

	for typ, conn := range connectors {
		entry := connectorSchemaEntry{}
		// Check if connector implements SecretSchemaProvider
		if provider, ok := conn.(connect.SecretSchemaProvider); ok {
			entry.Secrets = provider.SecretSchema()
		}
		schemas[typ] = entry
	}

	writeJSON(w, http.StatusOK, map[string]any{"connectors": schemas})
}

// ---- GET /admin/connectors/{type}/schema -> Get single connector schema ----

// getConnectorSchema returns the secret schema for a specific connector type.
func (a *Admin) getConnectorSchema(w http.ResponseWriter, r *http.Request) {
	connType := r.PathValue("type")
	if connType == "" {
		writeError(w, http.StatusBadRequest, "invalid request: missing connector type")
		return
	}

	// Look up the connector by type from the registry.
	lookup, ok := a.deps.Conns.(connect.ConnectorLookup)
	if !ok || a.deps.Conns == nil {
		writeError(w, http.StatusInternalServerError, "connector registry unavailable")
		return
	}
	connector, found := lookup.Connector(connType)
	if !found {
		writeError(w, http.StatusNotFound, "unknown connection type: "+connType)
		return
	}

	schema := connectorSchemaResponse{Type: connType}
	// Check if connector implements SecretSchemaProvider
	if provider, ok := connector.(connect.SecretSchemaProvider); ok {
		schema.Secrets = provider.SecretSchema()
	}

	writeJSON(w, http.StatusOK, schema)
}
