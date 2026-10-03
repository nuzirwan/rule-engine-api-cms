//go:build integration && cgo

// This file is the FMC demo proof: it stands up an EPHEMERAL postgres:16 holding
// the fmc_order.order_status table, seeds the committed seed.json THROUGH the
// in-memory config store, overrides ONLY the fmc-pg DSN to point at the ephemeral
// container, and serves the two FMC flows (GET /order/{order_id}, GET
// /order/msisdn/{msisdn}) end-to-end through the REAL dynamic httpapi handler +
// REAL connect.Registry + REAL ZEN decision engine. It NEVER connects to a
// user/external database — ephemeral containers only.
//
// It also carries the CRUCIAL config-driven-routing proof: a brand-new flow row
// with a brand-new path is published into the LIVE store and served on the next
// request with ZERO Go route code added, and a path with no active flow row is a
// 404. This proves the engine's premise — "adding an API = adding config, no
// code deploy".

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/connect"
	"nzr-rules-engine/internal/connect/drivers"
	"nzr-rules-engine/internal/decision"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/internal/httpapi"
	"nzr-rules-engine/internal/observ"
)

// TestFMCOrderFlows proves the two FMC demo flows are served PURELY FROM CONFIG
// (no Go route code) against an ephemeral postgres: the /order/{order_id} route
// returns proceed_fulfillment for a PAID row and await_payment for a PENDING row
// (both decided ONLY by the shared fmc-payment JDM via a decision->set node), and
// the /order/msisdn/{msisdn} route returns the LATEST row for an msisdn that has
// two rows. It also asserts /livez and /readyz are 200 (ops endpoints on the SAME
// mux, readyz gated on the real registry + store Ping against the ephemeral DB).
func TestFMCOrderFlows(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := startPostgres(t)
	defer cleanup()

	seedFMCData(t, ctx, dsn)

	// A tiny REST stub so the seed's ship-rest connection has a reachable health
	// probe (the real registry pings EVERY connection for /readyz).
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer rest.Close()

	handler := buildFMCHandler(t, ctx, dsn, rest.URL)

	t.Run("by id PAID -> proceed_fulfillment", func(t *testing.T) {
		body := doJSON(t, handler, http.MethodGet, "/order/ORD-TEST-TRK")
		if body["action"] != "proceed_fulfillment" {
			t.Fatalf("action = %v want proceed_fulfillment (body %v)", body["action"], body)
		}
		assertEchoFields(t, body, "ORD-TEST-TRK", "PAID")
	})

	t.Run("by id PENDING -> await_payment", func(t *testing.T) {
		body := doJSON(t, handler, http.MethodGet, "/order/ORD-TEST-BAD")
		if body["action"] != "await_payment" {
			t.Fatalf("action = %v want await_payment (body %v)", body["action"], body)
		}
		assertEchoFields(t, body, "ORD-TEST-BAD", "PENDING")
	})

	t.Run("by msisdn -> latest row", func(t *testing.T) {
		body := doJSON(t, handler, http.MethodGet, "/order/msisdn/628111015450")
		// Two rows share this msisdn; ORDER BY last_updated DESC LIMIT 1 must pick
		// the newer one (ORD-MSISDN-NEW), not the older (ORD-MSISDN-OLD).
		if body["order_id"] != "ORD-MSISDN-NEW" {
			t.Fatalf("order_id = %v want ORD-MSISDN-NEW (the latest row); body %v", body["order_id"], body)
		}
		if body["action"] != "proceed_fulfillment" {
			t.Fatalf("action = %v want proceed_fulfillment (latest row is PAID); body %v", body["action"], body)
		}
	})

	t.Run("ops endpoints on the same mux", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/livez = %d want 200", rec.Code)
		}
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/readyz = %d want 200 (body %s)", rec.Code, rec.Body.String())
		}
	})
}

// TestFMCConfigDrivenRouting is the CRUCIAL proof that routes come from CONFIG,
// not code: a path with no active flow row is a 404, then a NEW flow row with a
// NEW path (no DB, no Go route code) is published into the LIVE store and served
// on the very next request. The handler is also rebuilt via NewHandler to show
// even a fresh handler needs ZERO route code for the new path.
func TestFMCConfigDrivenRouting(t *testing.T) {
	ctx := context.Background()

	seedPath := filepath.Join("..", "config", "testdata", "seed.json")
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	store, err := config.SeedFromBytes(raw)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	tracer := observ.NewTracer(nil)
	logger := observ.NewLogger(nil)
	deps := httpapi.Deps{Trace: tracer, Log: logger, Store: store, Metrics: prometheus.NewRegistry()}

	handler, err := httpapi.NewHandler(store, flow.New(), deps)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	// 1. A path with no active flow row -> 404. Use a fresh top-level segment so
	// it cannot be captured by the seeded /order/{order_id} wildcard.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/ping", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("pre-publish /demo/ping = %d want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/absent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/demo/absent = %d want 404", rec.Code)
	}

	// 2. Publish a brand-new flow (trigger -> set(constant) -> response) under a
	// brand-new path, with NO Go route code added anywhere.
	pingTree := flow.Node{
		ID:   "t",
		Type: flow.TypeTrigger,
		Spec: json.RawMessage(`{"method":"GET","path":"/demo/ping","input":{}}`),
		Children: []flow.Node{{
			ID:   "set-pong",
			Type: flow.TypeSet,
			Spec: json.RawMessage(`{"targetPath":"pong","value":true}`),
			Children: []flow.Node{{
				ID:   "respond",
				Type: flow.TypeResponse,
				Spec: json.RawMessage(`{"status":200,"bodyFrom":""}`),
			}},
		}},
	}
	ver, err := store.PutFlowVersion(ctx, "", config.FlowVersion{
		FlowID: "demo-ping", Method: "GET", Path: "/demo/ping", Tree: pingTree,
	})
	if err != nil {
		t.Fatalf("PutFlowVersion: %v", err)
	}
	if err := store.SetActive(ctx, "", "demo-ping", ver); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// 3. Rebuild the handler via NewHandler (ZERO new Go route code) and serve.
	handler, err = httpapi.NewHandler(store, flow.New(), deps)
	if err != nil {
		t.Fatalf("NewHandler (rebuild): %v", err)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("post-publish /demo/ping = %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if derr := json.Unmarshal(rec.Body.Bytes(), &body); derr != nil {
		t.Fatalf("decode /demo/ping body: %v", derr)
	}
	if body["pong"] != true {
		t.Fatalf("/demo/ping body = %v want pong:true", body)
	}

	// 4. An absent flow still yields a 404 after the rebuild.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/absent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("post-publish /demo/absent = %d want 404", rec.Code)
	}
}

// seedFMCData creates schema fmc_order and the order_status table, then inserts a
// PAID row, a PENDING row, and two rows for one msisdn at different last_updated
// so the ORDER BY last_updated DESC LIMIT 1 picks the latest.
func seedFMCData(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx connect: %v", err)
	}
	defer conn.Close(ctx)

	stmts := []string{
		`CREATE SCHEMA fmc_order`,
		`CREATE TABLE fmc_order.order_status (
			order_id text PRIMARY KEY,
			msisdn text,
			order_status text,
			payment_status text,
			payment_amount numeric,
			product_name text,
			last_updated timestamptz
		)`,
		`INSERT INTO fmc_order.order_status
			(order_id, msisdn, order_status, payment_status, payment_amount, product_name, last_updated) VALUES
			('ORD-TEST-TRK', '628000000001', 'confirmed', 'PAID',    199.90, 'Fiber 100Mbps', '2024-01-01T10:00:00Z'),
			('ORD-TEST-BAD', '628000000002', 'pending',   'PENDING', 149.00, 'Fiber 50Mbps',  '2024-01-01T11:00:00Z'),
			('ORD-MSISDN-OLD', '628111015450', 'confirmed', 'PENDING', 99.00, 'Mobile 10GB', '2024-01-01T09:00:00Z'),
			('ORD-MSISDN-NEW', '628111015450', 'confirmed', 'PAID',    120.00, 'Mobile 20GB', '2024-02-01T09:00:00Z')`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("seed fmc data (%.40s...): %v", s, err)
		}
	}
}

// buildFMCHandler seeds the committed seed.json through the in-memory store,
// overrides ONLY the fmc-pg DSN to the ephemeral container, and builds the REAL
// registry + decision engine + dynamic handler.
func buildFMCHandler(t *testing.T, ctx context.Context, dsn, restURL string) http.Handler {
	t.Helper()

	seedPath := filepath.Join("..", "config", "testdata", "seed.json")
	raw, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	store, err := config.SeedFromBytes(raw)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}

	defs, err := store.Connections(ctx, "")
	if err != nil {
		t.Fatalf("connections: %v", err)
	}
	// Point the demo connection (fmc-pg) at the ephemeral postgres, and also the
	// kept orders-expedite connections (orders-pg -> same ephemeral postgres,
	// ship-rest -> the stub) so the registry health probe for /readyz passes.
	defs = overrideFMCEndpoint(t, defs, dsn)
	defs = overrideEndpoints(t, defs, dsn, restURL)

	tracer := observ.NewTracer(nil)
	logger := observ.NewLogger(nil)
	registry, err := connect.New(drivers.All(), defs, connect.NewEnvSecretProvider(), tracer, logger)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	engine := decision.New(jdmLoader{store: store})
	t.Cleanup(func() { engine.Close() })

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
	return handler
}

// overrideFMCEndpoint points the seeded 'fmc-pg' DSN at the ephemeral postgres,
// leaving every other connection def intact.
func overrideFMCEndpoint(t *testing.T, defs []connect.ConnectionDef, dsn string) []connect.ConnectionDef {
	t.Helper()
	for i := range defs {
		if defs[i].Key == "fmc-pg" {
			defs[i].Settings = cloneSettings(defs[i].Settings)
			defs[i].Settings["dsn"] = dsn
		}
	}
	return defs
}

// doJSON issues a request through the handler, asserts a 200, and decodes the
// JSON body.
func doJSON(t *testing.T, handler http.Handler, method, target string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s = %d want 200 (body %s)", method, target, rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s %s body: %v (raw %s)", method, target, err, rec.Body.String())
	}
	return body
}

// assertEchoFields checks the six echoed row fields are present and the two
// stable ones match the seeded row.
func assertEchoFields(t *testing.T, body map[string]any, wantOrderID, wantPayStatus string) {
	t.Helper()
	for _, f := range []string{"order_id", "msisdn", "order_status", "payment_status", "payment_amount", "product_name"} {
		if _, ok := body[f]; !ok {
			t.Fatalf("echoed field %q missing from body %v", f, body)
		}
	}
	if body["order_id"] != wantOrderID {
		t.Fatalf("order_id = %v want %v", body["order_id"], wantOrderID)
	}
	if body["payment_status"] != wantPayStatus {
		t.Fatalf("payment_status = %v want %v", body["payment_status"], wantPayStatus)
	}
}
