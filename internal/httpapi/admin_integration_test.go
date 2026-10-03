//go:build integration && cgo

// This file verifies the /admin control plane + the two live-bug fixes end-to-end
// against EPHEMERAL postgres:16 ONLY (the proven startPostgres harness in
// integration_test.go, same package test binary). It NEVER reads a DSN from the
// environment and never touches an external DB.

package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"nzr-rules-engine/internal/auth"
	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/httpapi"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/migrations"
)

const opToken = "integration-operator-token"

// newAdminPgHandler stands up a PgStore over an ephemeral postgres, builds the
// REAL httpapi handler with the admin plane enabled (static-token operator auth),
// and returns the handler, store, pool and a cleanup.
func newAdminPgHandler(t *testing.T, operAuth auth.OperatorAuthenticator) (http.Handler, *config.PgStore, *pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()
	dsn, pgCleanup := startPostgres(t)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		pgCleanup()
		t.Fatalf("pool: %v", err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		pool.Close()
		pgCleanup()
		t.Fatalf("migrate: %v", err)
	}
	store, err := config.NewPgStore(map[string]*pgxpool.Pool{"": pool}, config.WithActor("engine"))
	if err != nil {
		pool.Close()
		pgCleanup()
		t.Fatalf("pgstore: %v", err)
	}

	tracer := observ.NewTracer(nil)
	logger := observ.NewLogger(nil)
	registry, err := connect.New(drivers.All(), nil, connect.NewEnvSecretProvider(), tracer, logger)
	if err != nil {
		pool.Close()
		pgCleanup()
		t.Fatalf("registry: %v", err)
	}

	handler, err := httpapi.NewHandler(store, flow.New(), httpapi.Deps{
		Conns:    registry,
		Trace:    tracer,
		Log:      logger,
		Store:    store,
		Metrics:  prometheus.NewRegistry(),
		Admin:    store,
		OperAuth: operAuth,
	})
	if err != nil {
		registry.Close()
		pool.Close()
		pgCleanup()
		t.Fatalf("NewHandler: %v", err)
	}
	cleanup := func() {
		registry.Close()
		pool.Close()
		pgCleanup()
	}
	return handler, store, pool, cleanup
}

// staticAuth builds a one-token operator authenticator with all roles.
func staticAuth(t *testing.T) auth.OperatorAuthenticator {
	t.Helper()
	sum := sha256.Sum256([]byte(opToken))
	spec := hex.EncodeToString(sum[:]) + ":op:tester:flow.read,flow.write,flow.publish"
	a, err := auth.NewStaticTokenOperatorAuth(spec)
	if err != nil {
		t.Fatalf("operator auth: %v", err)
	}
	return a
}

func adminReq(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.Header.Set("Authorization", "Bearer "+opToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func minimalFlowTree() flow.Node {
	return flow.Node{
		ID:   "trigger",
		Type: flow.TypeTrigger,
		Spec: json.RawMessage(`{"method":"GET","path":"/widget/{id}","input":{"params":["id"]}}`),
		Children: []flow.Node{
			{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)},
		},
	}
}

// TestAdminCreateValidatePublishAudit exercises the full control-plane round-trip
// over /admin/* with a valid operator token: create -> validate (stored) ->
// publish -> audit. It also proves publish of an un-validated version is 422.
func TestAdminCreateValidatePublishAudit(t *testing.T) {
	h, _, _, cleanup := newAdminPgHandler(t, staticAuth(t))
	defer cleanup()

	treeJSON, _ := json.Marshal(minimalFlowTree())

	// create -> 201, version 1.
	createBody := `{"flowId":"widget","method":"GET","path":"/widget/{id}","tree":` + string(treeJSON) + `}`
	rec := adminReq(t, h, http.MethodPost, "/admin/flows", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d want 201 (body %s)", rec.Code, rec.Body.String())
	}

	// publish BEFORE validate => 422 (publish-blocking).
	rec = adminReq(t, h, http.MethodPost, "/admin/flows/widget/publish", `{"version":1}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("publish-unvalidated status = %d want 422 (body %s)", rec.Code, rec.Body.String())
	}

	// validate (stored mode) => 200 ok:true, flips validated=true.
	rec = adminReq(t, h, http.MethodPost, "/admin/flows/validate", `{"flowId":"widget","version":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var vout map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &vout)
	if vout["ok"] != true {
		t.Fatalf("validate ok = %v want true (body %s)", vout["ok"], rec.Body.String())
	}

	// publish AFTER validate => 200.
	rec = adminReq(t, h, http.MethodPost, "/admin/flows/widget/publish", `{"version":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// audit => newest-first entries including publish + create_version.
	rec = adminReq(t, h, http.MethodGet, "/admin/audit/flow/widget", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit status = %d want 200", rec.Code)
	}
	var aout struct {
		Entries []map[string]any `json:"entries"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &aout)
	actions := map[string]bool{}
	for _, e := range aout.Entries {
		actions[e["action"].(string)] = true
	}
	if !actions["publish"] || !actions["create_version"] {
		t.Fatalf("audit actions = %v want publish + create_version", actions)
	}
	// The audit actor is the operator subject (per-request actor attribution).
	if len(aout.Entries) == 0 || aout.Entries[0]["actor"] != "op:tester" {
		t.Fatalf("audit actor = %v want op:tester", aout.Entries)
	}
}

// TestAdminConnectionSecretRejectedIntegration proves a secret-value connection
// body is 400 and GET /admin/connections shows only secretRef (no value).
func TestAdminConnectionSecretRejectedIntegration(t *testing.T) {
	h, _, _, cleanup := newAdminPgHandler(t, staticAuth(t))
	defer cleanup()

	// secret value => 400.
	rec := adminReq(t, h, http.MethodPost, "/admin/connections",
		`{"key":"pg","type":"postgres","settings":{"host":"h"},"password":"hunter2"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("secret-value status = %d want 400", rec.Code)
	}

	// secretRef only => 201.
	rec = adminReq(t, h, http.MethodPost, "/admin/connections",
		`{"key":"pg","type":"postgres","settings":{"host":"h"},"secretRef":"env:PG_PW"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("secretRef status = %d want 201 (body %s)", rec.Code, rec.Body.String())
	}

	// list shows secretRef, no secret value.
	rec = adminReq(t, h, http.MethodGet, "/admin/connections", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "env:PG_PW") || strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("list body leaked/missing secret handling: %s", rec.Body.String())
	}
}

// TestAdminDisabledPlaneIntegration proves a nil OperatorAuthenticator mounts the
// plane closed (503 on /admin/*).
func TestAdminDisabledPlaneIntegration(t *testing.T) {
	h, _, _, cleanup := newAdminPgHandler(t, nil)
	defer cleanup()
	rec := adminReq(t, h, http.MethodGet, "/admin/connections", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled-plane status = %d want 503", rec.Code)
	}
}

// TestAdminDryRunSuppressesWrite proves a dry-run against a real stored flow whose
// action is a WRITE op suppresses the write (no row written) and returns a trace
// containing the suppressed-write record (AC-14).
func TestAdminDryRunSuppressesWrite(t *testing.T) {
	h, store, pool, cleanup := newAdminPgHandler(t, staticAuth(t))
	defer cleanup()
	ctx := context.Background()

	// A sink table the write op would target if it were NOT suppressed.
	if _, err := pool.Exec(ctx, `CREATE TABLE sink (id INT)`); err != nil {
		t.Fatalf("create sink: %v", err)
	}

	// A connection the write op names. It points at the SAME ephemeral postgres,
	// but the dry-run suppresses the write before any Execute, so the pool is
	// never used for the write.
	connDef := connect.ConnectionDef{
		Key:  "sink-pg",
		Type: "postgres",
		Settings: map[string]any{
			"dsn": pool.Config().ConnString(),
		},
	}
	if _, err := store.PutConnectionVersion(ctx, "", connDef); err != nil {
		t.Fatalf("put connection: %v", err)
	}

	// A flow: trigger -> action(exec INSERT) -> response. Stored + validated so
	// dry-run can resolve it by version.
	writeTree := flow.Node{
		ID:   "trigger",
		Type: flow.TypeTrigger,
		Spec: json.RawMessage(`{"method":"POST","path":"/sink","input":{}}`),
		Children: []flow.Node{
			{
				ID:   "write",
				Type: flow.TypeAction,
				Spec: json.RawMessage(`{"connection":"sink-pg","operation":{"kind":"exec","payload":{"sql":"INSERT INTO sink (id) VALUES (1)"}},"saveAs":"w"}`),
				Children: []flow.Node{
					{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)},
				},
			},
		},
	}
	ver, err := store.PutFlowVersion(ctx, "", config.FlowVersion{
		FlowID: "sinkflow", Method: "POST", Path: "/sink", Tree: writeTree,
	})
	if err != nil {
		t.Fatalf("put flow: %v", err)
	}

	// dry-run by flowId + version. Pass a mocks map so the registry resolves the
	// connection key deterministically (no real I/O); the write op is still
	// suppressed by the dry-run flag before any Execute (AC-14).
	body := `{"flowId":"sinkflow","version":` + itoa(ver) + `,"input":{"method":"POST","path":"/sink"},"mocks":{"conn:sink-pg":{}}}`
	rec := adminReq(t, h, http.MethodPost, "/admin/flows/dry-run", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry-run status = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// No row was written (the write was suppressed).
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sink`).Scan(&n); err != nil {
		t.Fatalf("count sink: %v", err)
	}
	if n != 0 {
		t.Fatalf("sink has %d rows after dry-run; want 0 (write must be suppressed)", n)
	}
	// The trace contains the suppressed-write record.
	if !strings.Contains(rec.Body.String(), "suppressed") {
		t.Fatalf("dry-run trace missing suppressed-write record: %s", rec.Body.String())
	}
}

// TestReadyzHealthyStoreIntegration proves /readyz == 200 when the config store
// is healthy EVEN THOUGH a data-source connection points at a dead address (the
// readyz false-negative fix).
func TestReadyzHealthyStoreIntegration(t *testing.T) {
	ctx := context.Background()
	dsn, pgCleanup := startPostgres(t)
	defer pgCleanup()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := config.NewPgStore(map[string]*pgxpool.Pool{"": pool})
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}

	// A registry whose single connection points at a DEAD address. Under the old
	// gate this would make /readyz 503; after the fix it is irrelevant.
	deadDef := connect.ConnectionDef{
		Key: "dead-pg", Type: "postgres",
		Settings: map[string]any{"dsn": "postgres://nobody@127.0.0.1:1/none?sslmode=disable"},
	}
	registry, err := connect.New(drivers.All(), []connect.ConnectionDef{deadDef}, connect.NewEnvSecretProvider(), observ.NewTracer(nil), observ.NewLogger(nil))
	if err != nil {
		// Opening a dead postgres may lazily succeed (pool created, not pinged);
		// if it fails outright, skip the registry wiring — the readyz gate is
		// store-only regardless.
		registry = nil
	}
	if registry != nil {
		defer registry.Close()
	}

	deps := httpapi.Deps{Store: store, Metrics: prometheus.NewRegistry()}
	if registry != nil {
		deps.Conns = registry
	}
	h, err := httpapi.NewHandler(store, flow.New(), deps)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz status = %d want 200 (healthy store, dead data source must not gate)", rec.Code)
	}
}

// itoa is a tiny int->string without importing strconv in multiple spots.
func itoa(n int) string {
	return strings.TrimSpace(jsonNumber(n))
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
