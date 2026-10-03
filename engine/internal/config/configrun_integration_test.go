//go:build integration && cgo

// This file verifies the CONFIG-STORE RUN MODE end to end against EPHEMERAL
// containers ONLY: it stands up TWO ephemeral postgres:16 containers (one as the
// engine's config store, one holding the orders DATA table) plus one ephemeral
// valkey. It NEVER reads CONFIG_DSN / ORDERS_PG_DSN from the environment and
// never connects to any external/user database. The container harness
// (startPostgres / startValkey / newValkeyClient / dockerHostPort) is the proven
// recipe shared with pgstore_integration_test.go (same package test binary).

package config_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/httpapi"
	"nzr-rules-engine/internal/observ"
	"nzr-rules-engine/migrations"
)

const configSchema = "rule_engine"

// TestConfigRunModeSeedsIntoSchema proves the config-store run mode:
//
//	(a) config tables are created in schema rule_engine and NOT in public;
//	(b) the seed flow/JDM/connections are written and the flow is ACTIVE;
//	(c) running the seed a SECOND time is idempotent (no error, no duplicate);
//	(d) GET /orders/{id} is served end-to-end reading config FROM the PgStore.
func TestConfigRunModeSeedsIntoSchema(t *testing.T) {
	ctx := context.Background()

	// --- ephemeral config-store postgres, schema-scoped pool, migrations ---
	cfgDSN, cfgCleanup := startPostgres(t)
	defer cfgCleanup()

	pool, err := config.OpenSchemaPool(ctx, cfgDSN, configSchema)
	if err != nil {
		t.Fatalf("open schema pool: %v", err)
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// --- ephemeral valkey + cache (exercises the cached resolve path too) ---
	vkAddr, vkCleanup := startValkey(t)
	defer vkCleanup()
	vkClient := newValkeyClient(t, vkAddr)
	defer vkClient.Close()
	cache := config.NewValkeyCache(vkClient)
	cache.StartInvalidationConsumer(ctx)
	defer cache.Close()

	store, err := config.NewPgStore(
		map[string]*pgxpool.Pool{"": pool},
		config.WithCache(cache),
		config.WithActor("engine"),
	)
	if err != nil {
		t.Fatalf("new pgstore: %v", err)
	}

	// --- seed from the committed seed.json THROUGH the store ---
	seedPath := filepath.Join("testdata", "seed.json")
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	seeded, err := config.SeedPgStore(ctx, store, raw)
	if err != nil {
		t.Fatalf("seed pg store: %v", err)
	}
	if !seeded {
		t.Fatal("first seed should report seeded==true")
	}

	// (a) tables landed in rule_engine, NOT public.
	assertTableSchema(t, ctx, pool, configSchema, "flows", 1)
	assertTableSchema(t, ctx, pool, "public", "flows", 0)
	// spot-check the other config tables are in the dedicated schema.
	for _, tbl := range []string{"flow_versions", "jdm_versions", "connection_versions", "active_pointers", "audit_log"} {
		assertTableSchema(t, ctx, pool, configSchema, tbl, 1)
		assertTableSchema(t, ctx, pool, "public", tbl, 0)
	}

	// (b) flow active, JDM present, both connections present (ship-rest no secret value).
	fv, err := store.ActiveFlow(ctx, "", "GET", "/orders/{id}")
	if err != nil {
		t.Fatalf("active flow: %v", err)
	}
	if fv.FlowID != "orders-expedite" || fv.Version != 1 {
		t.Fatalf("active flow = %s v%d; want orders-expedite v1", fv.FlowID, fv.Version)
	}
	jdm, jver, err := store.GetJDM(ctx, "", "order")
	if err != nil {
		t.Fatalf("get jdm: %v", err)
	}
	if len(jdm) == 0 || jver != 1 {
		t.Fatalf("jdm bytes=%d version=%d; want non-empty v1", len(jdm), jver)
	}
	defs, err := store.Connections(ctx, "")
	if err != nil {
		t.Fatalf("connections: %v", err)
	}
	byKey := map[string]connect.ConnectionDef{}
	for _, d := range defs {
		byKey[d.Key] = d
	}
	if _, ok := byKey["orders-pg"]; !ok {
		t.Fatalf("missing orders-pg connection; got %v", defs)
	}
	ship, ok := byKey["ship-rest"]
	if !ok {
		t.Fatalf("missing ship-rest connection; got %v", defs)
	}
	// secret_ref is empty in the seed and no secret VALUE is ever stored.
	if ship.SecretRef != "" {
		t.Fatalf("ship-rest secret_ref = %q; want empty (no secret value stored)", ship.SecretRef)
	}
	assertNoSecretValueColumn(t, ctx, pool)

	// (c) second seed is a no-op (idempotent) and no duplicate versions exist.
	seeded2, err := config.SeedPgStore(ctx, store, raw)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if seeded2 {
		t.Fatal("second seed should report seeded==false (idempotent skip)")
	}
	assertCount(t, ctx, pool, `SELECT count(*) FROM flow_versions WHERE flow_id='orders-expedite'`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM jdm_versions WHERE jdm_id='order'`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM connection_versions WHERE conn_key='orders-pg'`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM connection_versions WHERE conn_key='ship-rest'`, 1)

	// (d) serve GET /orders/{id} end-to-end, config resolved FROM the PgStore.
	serveEndToEndFromPgStore(t, ctx, store)
}

// serveEndToEndFromPgStore stands up an ephemeral orders DATA postgres + an
// in-process REST stub, overrides the seed's data-plane endpoints to point at
// them, builds the REAL registry + decision engine + httpapi handler over the
// PgStore, and asserts both branches (expedited / standard) are served.
func serveEndToEndFromPgStore(t *testing.T, ctx context.Context, store config.Store) {
	t.Helper()

	// ephemeral orders DATA postgres (distinct from the config store).
	dataDSN, dataCleanup := startPostgres(t)
	defer dataCleanup()
	conn, err := pgx.Connect(ctx, dataDSN)
	if err != nil {
		t.Fatalf("data pg connect: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE TABLE orders (id INT PRIMARY KEY, amount INT NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatalf("create orders table: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO orders (id, amount, status) VALUES (1, 1500, 'paid'), (2, 500, 'paid')`); err != nil {
		t.Fatalf("seed orders rows: %v", err)
	}

	// in-process REST stub records hits and returns {shipping} derived from path.
	var hits []string
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		shipping := "standard"
		if r.URL.Path == "/expedite" {
			shipping = "expedited"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "shipping": shipping})
	}))
	defer rest.Close()

	// read connection defs FROM the PgStore, then override endpoints at runtime.
	defs, err := store.Connections(ctx, "")
	if err != nil {
		t.Fatalf("connections from store: %v", err)
	}
	defs = overrideEndpoints(t, defs, dataDSN, rest.URL)

	tracer := observ.NewTracer(nil)
	logger := observ.NewLogger(nil)
	registry, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), tracer, logger)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	defer registry.Close()

	engine := decision.New(cfgJDMLoader{store: store})
	defer engine.Close()

	handler, err := httpapi.NewHandler(store, flow.New(), httpapi.Deps{
		Conns:   registry,
		Decide:  engine,
		Trace:   tracer,
		Log:     logger,
		Store:   store,
		Metrics: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	cases := []struct {
		id, wantShipping, wantPath string
	}{
		{"1", "expedited", "/expedite"},
		{"2", "standard", "/standard"},
	}
	for _, tc := range cases {
		hits = nil
		req := httptest.NewRequest(http.MethodGet, "/orders/"+tc.id, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /orders/%s status = %d; want 200 (body: %s)", tc.id, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if derr := json.Unmarshal(rec.Body.Bytes(), &body); derr != nil {
			t.Fatalf("decode response: %v (raw: %s)", derr, rec.Body.String())
		}
		if got := body["shipping"]; got != tc.wantShipping {
			t.Errorf("order %s shipping = %v; want %v (full: %v)", tc.id, got, tc.wantShipping, body)
		}
		if len(hits) != 1 || hits[0] != tc.wantPath {
			t.Errorf("order %s REST hits = %v; want exactly [%s]", tc.id, hits, tc.wantPath)
		}
	}
}

// cfgJDMLoader adapts config.Store to decision.JDMLoader (same adapter cmd/engine
// wires) so the real engine resolves the seeded order JDM FROM the PgStore.
type cfgJDMLoader struct{ store config.Store }

func (l cfgJDMLoader) LoadJDM(ctx context.Context, env, jdmID string) ([]byte, int, error) {
	return l.store.GetJDM(ctx, env, jdmID)
}

// overrideEndpoints points the seeded 'orders-pg' DSN at the ephemeral data
// postgres and 'ship-rest' baseURL at the httptest stub, leaving the rest intact.
func overrideEndpoints(t *testing.T, defs []connect.ConnectionDef, dsn, restURL string) []connect.ConnectionDef {
	t.Helper()
	for i := range defs {
		switch defs[i].Key {
		case "orders-pg":
			defs[i].Settings = cloneSettings(defs[i].Settings)
			defs[i].Settings["dsn"] = dsn
		case "ship-rest":
			defs[i].Settings = cloneSettings(defs[i].Settings)
			defs[i].Settings["baseURL"] = restURL
		}
	}
	return defs
}

func cloneSettings(s map[string]any) map[string]any {
	out := make(map[string]any, len(s)+1)
	for k, v := range s {
		out[k] = v
	}
	return out
}

// --- assertion helpers ---

func assertTableSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema, table string, want int) {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2`,
		schema, table).Scan(&n); err != nil {
		t.Fatalf("count %s.%s: %v", schema, table, err)
	}
	if n != want {
		t.Fatalf("table %s.%s count = %d; want %d", schema, table, n, want)
	}
}

func assertCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, want int) {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", sql, err)
	}
	if n != want {
		t.Fatalf("count %q = %d; want %d", sql, n, want)
	}
}

func assertNoSecretValueColumn(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var cols int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name='connection_versions' AND column_name IN ('secret','password','secret_value')`).Scan(&cols); err != nil {
		t.Fatalf("count secret-value columns: %v", err)
	}
	if cols != 0 {
		t.Fatalf("connection_versions has %d secret-value columns; want 0", cols)
	}
}
