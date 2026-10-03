//go:build integration && cgo

// This file verifies the PER-OBJECT idempotent seeding fix (slice-f-admin-api.md
// §6) against an EPHEMERAL postgres:16 container ONLY — it NEVER reads a DSN from
// the environment and never touches an external DB. It uses the proven container
// harness (startPostgres) shared with the sibling config integration tests.

package config_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/migrations"
)

// TestSeedPerObjectIdempotency proves the fix for the live bug: adding ONE new
// flow to the seed doc and re-seeding writes ONLY the new flow (active) and
// leaves every existing object byte-identical and not duplicated; a third,
// no-change re-seed is a pure no-op (seeded==false, zero new rows). This is the
// exact scenario the old all-or-nothing gate silently skipped.
func TestSeedPerObjectIdempotency(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := startPostgres(t)
	defer cleanup()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store, err := config.NewPgStore(map[string]*pgxpool.Pool{"": pool}, config.WithActor("engine"))
	if err != nil {
		t.Fatalf("new pgstore: %v", err)
	}

	// --- seed #1: a single active flow "alpha". ---
	seedV1 := seedDocWithFlows(t, flowSeed{"alpha", "GET", "/alpha/{id}", true})
	seeded, err := config.SeedPgStore(ctx, store, seedV1)
	if err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if !seeded {
		t.Fatal("seed v1 should report seeded==true")
	}
	assertSeedCount(t, ctx, pool, `SELECT count(*) FROM flow_versions WHERE flow_id='alpha'`, 1)
	var alphaChecksumV1 string
	if err := pool.QueryRow(ctx,
		`SELECT checksum FROM flow_versions WHERE flow_id='alpha' AND version=1`).Scan(&alphaChecksumV1); err != nil {
		t.Fatalf("read alpha checksum: %v", err)
	}

	// --- seed #2: the SAME alpha PLUS a new active flow "beta". Only beta is
	// written; alpha stays byte-identical (same single version, same checksum). ---
	seedV2 := seedDocWithFlows(t,
		flowSeed{"alpha", "GET", "/alpha/{id}", true},
		flowSeed{"beta", "GET", "/beta/{id}", true},
	)
	seeded, err = config.SeedPgStore(ctx, store, seedV2)
	if err != nil {
		t.Fatalf("seed v2: %v", err)
	}
	if !seeded {
		t.Fatal("seed v2 should report seeded==true (beta was written)")
	}
	// alpha: still exactly one version, checksum unchanged (NOT re-created).
	assertSeedCount(t, ctx, pool, `SELECT count(*) FROM flow_versions WHERE flow_id='alpha'`, 1)
	var alphaChecksumV2 string
	if err := pool.QueryRow(ctx,
		`SELECT checksum FROM flow_versions WHERE flow_id='alpha' AND version=1`).Scan(&alphaChecksumV2); err != nil {
		t.Fatalf("read alpha checksum after v2: %v", err)
	}
	if alphaChecksumV1 != alphaChecksumV2 {
		t.Fatalf("alpha checksum changed on re-seed: %q -> %q (must be byte-identical)", alphaChecksumV1, alphaChecksumV2)
	}
	// beta: written and active.
	assertSeedCount(t, ctx, pool, `SELECT count(*) FROM flow_versions WHERE flow_id='beta'`, 1)
	if _, err := store.ActiveFlow(ctx, "", "GET", "/beta/{id}"); err != nil {
		t.Fatalf("beta not active after re-seed: %v", err)
	}
	// alpha is still active and un-churned.
	if _, err := store.ActiveFlow(ctx, "", "GET", "/alpha/{id}"); err != nil {
		t.Fatalf("alpha no longer active after re-seed: %v", err)
	}

	// create_version audit rows: exactly one per flow (no duplicate alpha create).
	assertSeedCount(t, ctx, pool, `SELECT count(*) FROM audit_log WHERE action='create_version' AND object_id='alpha'`, 1)
	assertSeedCount(t, ctx, pool, `SELECT count(*) FROM audit_log WHERE action='create_version' AND object_id='beta'`, 1)

	// --- seed #3: no changes => pure no-op. ---
	var auditBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&auditBefore); err != nil {
		t.Fatalf("count audit before noop: %v", err)
	}
	seeded, err = config.SeedPgStore(ctx, store, seedV2)
	if err != nil {
		t.Fatalf("seed v3 (noop): %v", err)
	}
	if seeded {
		t.Fatal("seed v3 should report seeded==false (no new objects)")
	}
	assertSeedCount(t, ctx, pool, `SELECT count(*) FROM flow_versions WHERE flow_id='alpha'`, 1)
	assertSeedCount(t, ctx, pool, `SELECT count(*) FROM flow_versions WHERE flow_id='beta'`, 1)
	var auditAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&auditAfter); err != nil {
		t.Fatalf("count audit after noop: %v", err)
	}
	if auditAfter != auditBefore {
		t.Fatalf("no-op re-seed wrote %d audit rows; want 0", auditAfter-auditBefore)
	}
}

// flowSeed is a compact description of one flow for the seed-doc builder.
type flowSeed struct {
	id, method, path string
	active           bool
}

// seedDocWithFlows builds a minimal seed JSON (env "") with the given flows and
// no JDMs/connections. Each flow is a valid trigger->response tree.
func seedDocWithFlows(t *testing.T, flows ...flowSeed) []byte {
	t.Helper()
	type sf struct {
		Env   string           `json:"env"`
		Flows []map[string]any `json:"flows"`
	}
	doc := sf{Env: ""}
	for _, f := range flows {
		tree := flow.Node{
			ID:   "trigger",
			Type: flow.TypeTrigger,
			Spec: json.RawMessage(`{"method":"` + f.method + `","path":"` + f.path + `","input":{}}`),
			Children: []flow.Node{
				{ID: "resp", Type: flow.TypeResponse, Spec: json.RawMessage(`{"status":200}`)},
			},
		}
		doc.Flows = append(doc.Flows, map[string]any{
			"flowId": f.id, "version": 1, "method": f.method, "path": f.path,
			"active": f.active, "tree": tree,
		})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal seed doc: %v", err)
	}
	return raw
}

// assertSeedCount asserts a scalar count query equals want.
func assertSeedCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, want int) {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	if n != want {
		t.Fatalf("count %q = %d; want %d", sql, n, want)
	}
}
