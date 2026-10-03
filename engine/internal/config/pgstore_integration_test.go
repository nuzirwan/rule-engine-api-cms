//go:build integration && cgo

package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valkey-io/valkey-go"

	"nzr-rules-engine/internal/config"
	"nzr-rules-engine/internal/flow"
	"nzr-rules-engine/migrations"
)

// This file exercises the REAL Postgres-backed config.PgStore and the REAL
// Valkey-backed config.ValkeyCache against ephemeral containers (Docker). Each
// test maps to the Slice D acceptance criteria AC-9..AC-16 and the migrations/
// secrets rows of the §8 test plan. Integration-tagged (needs Docker + CGO).

const testEnv = "dev"

// --- AC-9: edit creates a new immutable version; prior versions unchanged ---

func TestAC9_PutFlowVersionImmutable(t *testing.T) {
	ctx := context.Background()
	store, pool, cleanup := newStore(t, nil)
	defer cleanup()

	v1Tree := flow.Node{ID: "trigger", Type: flow.TypeTrigger, Children: []flow.Node{{ID: "a", Type: flow.TypeResponse}}}
	v1, err := store.PutFlowVersion(ctx, testEnv, config.FlowVersion{
		FlowID: "orders", Method: "GET", Path: "/orders/{id}", Tree: v1Tree,
	})
	if err != nil {
		t.Fatalf("put v1: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("first version = %d; want 1", v1)
	}

	v2Tree := flow.Node{ID: "trigger", Type: flow.TypeTrigger, Children: []flow.Node{{ID: "b", Type: flow.TypeResponse}}}
	v2, err := store.PutFlowVersion(ctx, testEnv, config.FlowVersion{
		FlowID: "orders", Method: "GET", Path: "/orders/{id}", Tree: v2Tree,
	})
	if err != nil {
		t.Fatalf("put v2: %v", err)
	}
	if v2 != 2 {
		t.Fatalf("second version = %d; want 2", v2)
	}

	// v1 row is byte-identical and still present (append-only, never UPDATEd).
	var treeRaw []byte
	if err := pool.QueryRow(ctx,
		`SELECT tree FROM flow_versions WHERE flow_id='orders' AND version=1`).Scan(&treeRaw); err != nil {
		t.Fatalf("read v1: %v", err)
	}
	var got flow.Node
	if err := json.Unmarshal(treeRaw, &got); err != nil {
		t.Fatalf("decode v1 tree: %v", err)
	}
	if len(got.Children) != 1 || got.Children[0].ID != "a" {
		t.Fatalf("v1 tree mutated: %+v", got)
	}

	// Both rows coexist; a create_version audit row exists for each.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action='create_version' AND object_id='orders'`).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 2 {
		t.Fatalf("create_version audit rows = %d; want 2", count)
	}
}

// --- AC-10: publish advances the pointer; rollback moves it back; no redeploy ---

func TestAC10_PublishAndRollback(t *testing.T) {
	ctx := context.Background()
	store, pool, cleanup := newStore(t, nil)
	defer cleanup()

	v1 := putValidated(t, store, pool, "orders", "GET", "/orders/{id}", "a")
	v2 := putValidated(t, store, pool, "orders", "GET", "/orders/{id}", "b")

	// publish v1
	if err := store.SetActive(ctx, testEnv, "orders", v1); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	fv, err := store.ActiveFlow(ctx, testEnv, "GET", "/orders/{id}")
	if err != nil {
		t.Fatalf("resolve after publish v1: %v", err)
	}
	if fv.Version != v1 || fv.Tree.Children[0].ID != "a" {
		t.Fatalf("active after publish v1 = v%d/%s; want v%d/a", fv.Version, fv.Tree.Children[0].ID, v1)
	}

	// publish v2 — takes effect on the next resolve, no redeploy.
	if err := store.SetActive(ctx, testEnv, "orders", v2); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	fv, _ = store.ActiveFlow(ctx, testEnv, "GET", "/orders/{id}")
	if fv.Version != v2 || fv.Tree.Children[0].ID != "b" {
		t.Fatalf("active after publish v2 = v%d/%s; want v%d/b", fv.Version, fv.Tree.Children[0].ID, v2)
	}

	// rollback to v1.
	if err := store.SetActive(ctx, testEnv, "orders", v1); err != nil {
		t.Fatalf("rollback v1: %v", err)
	}
	fv, _ = store.ActiveFlow(ctx, testEnv, "GET", "/orders/{id}")
	if fv.Version != v1 {
		t.Fatalf("active after rollback = v%d; want v%d", fv.Version, v1)
	}

	// audit: publish rows + one rollback row.
	var publishes, rollbacks int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='publish' AND object_id='orders'`).Scan(&publishes)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='rollback' AND object_id='orders'`).Scan(&rollbacks)
	if publishes != 2 || rollbacks != 1 {
		t.Fatalf("audit publishes/rollbacks = %d/%d; want 2/1", publishes, rollbacks)
	}
}

// --- AC-11: a resolved snapshot is self-contained; a later SetActive does not
// mutate it, and it carries the version number Slice A pins to ---

func TestAC11_ResolvedSnapshotIsPinned(t *testing.T) {
	ctx := context.Background()
	store, pool, cleanup := newStore(t, nil)
	defer cleanup()

	v1 := putValidated(t, store, pool, "orders", "GET", "/orders/{id}", "a")
	v2 := putValidated(t, store, pool, "orders", "GET", "/orders/{id}", "b")
	if err := store.SetActive(ctx, testEnv, "orders", v1); err != nil {
		t.Fatalf("publish v1: %v", err)
	}

	// Resolve at "request start".
	pinned, err := store.ActiveFlow(ctx, testEnv, "GET", "/orders/{id}")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if pinned.Version != v1 {
		t.Fatalf("pinned version = %d; want %d", pinned.Version, v1)
	}

	// Publish v2 mid-"request".
	if err := store.SetActive(ctx, testEnv, "orders", v2); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	// The already-resolved snapshot is unchanged: it carries v1's version + tree.
	if pinned.Version != v1 || pinned.Tree.Children[0].ID != "a" {
		t.Fatalf("resolved snapshot was mutated by a later publish: %+v", pinned)
	}
}

// --- AC-12: promotion copies a specific version + fixtures to another env; the
// destination pointer is unchanged (envs can hold different active versions) ---

func TestAC12_PromoteVersionAcrossEnvs(t *testing.T) {
	ctx := context.Background()
	// two env pools backed by two ephemeral databases.
	pools, cleanup := newTwoEnvPools(t)
	defer cleanup()
	store, err := config.NewPgStore(pools, config.WithActor("tester"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	// author + validate + publish v1 in dev, with a fixture that must travel.
	devPool := pools["dev"]
	v1, err := store.PutFlowVersion(ctx, "dev", config.FlowVersion{
		FlowID: "orders", Method: "GET", Path: "/orders/{id}",
		Tree:     flow.Node{ID: "trigger", Type: flow.TypeTrigger},
		Fixtures: []config.FlowFixture{{Name: "f1", Input: map[string]any{"id": float64(1)}}},
	})
	if err != nil {
		t.Fatalf("put dev v1: %v", err)
	}
	if err := store.MarkValidated(ctx, "dev", "orders", v1); err != nil {
		t.Fatalf("validate dev v1: %v", err)
	}
	if err := store.SetActive(ctx, "dev", "orders", v1); err != nil {
		t.Fatalf("publish dev v1: %v", err)
	}
	_ = devPool

	// promote dev v1 -> staging.
	if err := store.PromoteVersion(ctx, "dev", "staging", "orders", v1); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// staging has the exact version + fixture...
	stgPool := pools["staging"]
	var fxCount int
	if err := stgPool.QueryRow(ctx,
		`SELECT count(*) FROM flow_fixtures WHERE flow_id='orders' AND version=$1 AND name='f1'`, v1).Scan(&fxCount); err != nil {
		t.Fatalf("count staging fixtures: %v", err)
	}
	if fxCount != 1 {
		t.Fatalf("promoted fixture count = %d; want 1", fxCount)
	}

	// ...but the staging pointer is UNCHANGED (nothing published there yet): no
	// active_pointers row exists for the promoted flow in staging.
	var ptrRows int
	if err := stgPool.QueryRow(ctx,
		`SELECT count(*) FROM active_pointers WHERE object_type='flow' AND object_id='orders'`).Scan(&ptrRows); err != nil {
		t.Fatalf("staging pointer query: %v", err)
	}
	if ptrRows != 0 {
		t.Fatalf("staging pointer unexpectedly set; promotion must not auto-activate")
	}

	// promote audit row written in the destination.
	var promotes int
	_ = stgPool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='promote' AND object_id='orders'`).Scan(&promotes)
	if promotes != 1 {
		t.Fatalf("staging promote audit rows = %d; want 1", promotes)
	}
}

// --- AC-13: SetActive refuses an un-validated version (publish blocked) ---

func TestAC13_PublishBlockedOnUnvalidated(t *testing.T) {
	ctx := context.Background()
	store, _, cleanup := newStore(t, nil)
	defer cleanup()

	v1, err := store.PutFlowVersion(ctx, testEnv, config.FlowVersion{
		FlowID: "orders", Method: "GET", Path: "/orders/{id}",
		Tree: flow.Node{ID: "trigger", Type: flow.TypeTrigger},
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// un-validated: publish must be refused with a Validation error.
	err = store.SetActive(ctx, testEnv, "orders", v1)
	if err == nil {
		t.Fatal("expected publish to be blocked for an un-validated version")
	}
	if !errors.Is(err, config.ErrValidation) {
		t.Fatalf("publish-blocked error = %v; want Validation", err)
	}

	// after validation, publish succeeds.
	if err := store.MarkValidated(ctx, testEnv, "orders", v1); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := store.SetActive(ctx, testEnv, "orders", v1); err != nil {
		t.Fatalf("publish after validate: %v", err)
	}
}

// --- AC-15: a deliberately broken stored tree is Validation on load, no 500 ---

func TestAC15_MalformedConfigRejectedOnLoad(t *testing.T) {
	ctx := context.Background()
	store, pool, cleanup := newStore(t, nil)
	defer cleanup()

	// Hand-insert a flow + a broken version row (a JSON string, not a Node), then
	// point the active pointer at it. This bypasses PutFlowVersion to simulate a
	// bad row written by a drifting control plane (R11).
	mustExec(t, pool, `INSERT INTO flows (id, method, path, created_by) VALUES ('broken','GET','/broken','tester')`)
	mustExec(t, pool, `INSERT INTO flow_versions (flow_id, version, tree, checksum, validated, created_by)
	                   VALUES ('broken', 1, '"not a node"'::jsonb, 'x', true, 'tester')`)
	mustExec(t, pool, `INSERT INTO active_pointers (object_type, object_id, version, updated_by)
	                   VALUES ('flow','broken',1,'tester')`)

	// Resolve must classify as Validation, not panic, not a 500/Internal.
	_, err := store.ActiveFlow(ctx, testEnv, "GET", "/broken")
	if err == nil {
		t.Fatal("expected a Validation error for a malformed stored tree")
	}
	if !errors.Is(err, config.ErrValidation) {
		t.Fatalf("malformed-config error = %v; want Validation", err)
	}
	if errors.Is(err, config.ErrInternal) {
		t.Fatalf("malformed config must NOT surface as Internal/500: %v", err)
	}
}

// --- AC-16 (+R5): publish on one instance invalidates cached keys fleet-wide via
// pub/sub within one round-trip; TTL bounds any missed message ---

func TestAC16_CacheInvalidationAcrossInstances(t *testing.T) {
	ctx := context.Background()

	addr, vkCleanup := startValkey(t)
	defer vkCleanup()

	// Two independent cache instances (two engine processes) over the same Valkey,
	// each with its own client + subscription — the "one writer, many readers"
	// topology (R5).
	writerClient := newValkeyClient(t, addr)
	defer writerClient.Close()
	readerClient := newValkeyClient(t, addr)
	defer readerClient.Close()

	writerCache := config.NewValkeyCache(writerClient, config.WithTTL(30*time.Second))
	readerCache := config.NewValkeyCache(readerClient, config.WithTTL(30*time.Second))
	readerCache.StartInvalidationConsumer(ctx)
	defer readerCache.Close()
	// give the subscriber a moment to establish.
	time.Sleep(300 * time.Millisecond)

	// The reader instance has a cached flow key (as if it served a request).
	key := "cfg:v1:flow:" + testEnv + ":GET:/orders/{id}"
	if err := readerCache.Set(ctx, key, []byte(`{"flowId":"orders","version":1}`), 30*time.Second); err != nil {
		t.Fatalf("seed reader cache: %v", err)
	}
	if _, ok, _ := readerCache.Get(ctx, key); !ok {
		t.Fatal("reader cache should hold the seeded key")
	}

	// The writer instance publishes an invalidation (as SetActive does after
	// commit) for the env's flow namespace.
	if err := writerCache.Invalidate(ctx, "cfg:v1:flow:"+testEnv+":*"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	// Within one pub/sub round-trip the reader must have dropped its key.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, ok, err := readerCache.Get(ctx, key)
		if err != nil {
			t.Fatalf("reader get: %v", err)
		}
		if !ok {
			break // invalidated — success
		}
		if time.Now().After(deadline) {
			t.Fatal("reader did not drop its cached key after a cross-instance invalidate")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// --- AC-16 backstop: TTL bounds staleness even with no invalidation message ---

func TestAC16_TTLBackstop(t *testing.T) {
	ctx := context.Background()
	addr, vkCleanup := startValkey(t)
	defer vkCleanup()

	client := newValkeyClient(t, addr)
	defer client.Close()
	cache := config.NewValkeyCache(client, config.WithTTL(600*time.Millisecond), config.WithJitter(0))

	key := "cfg:v1:jdm:" + testEnv + ":order"
	if err := cache.Set(ctx, key, []byte(`{"jdm":"","version":1}`), 600*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, ok, _ := cache.Get(ctx, key); !ok {
		t.Fatal("entry should be present immediately after Set")
	}
	// no invalidation; the TTL alone must expire it.
	time.Sleep(1200 * time.Millisecond)
	if _, ok, _ := cache.Get(ctx, key); ok {
		t.Fatal("entry should have expired via TTL backstop")
	}
}

// --- store resolves via cache (cache-aside) and degrades when cache is down ---

func TestResolveCacheAsideAndDegrade(t *testing.T) {
	ctx := context.Background()
	addr, vkCleanup := startValkey(t)
	defer vkCleanup()

	client := newValkeyClient(t, addr)
	cache := config.NewValkeyCache(client, config.WithTTL(30*time.Second))

	store, pool, cleanup := newStore(t, cache)
	defer cleanup()

	v1 := putValidated(t, store, pool, "orders", "GET", "/orders/{id}", "a")
	if err := store.SetActive(ctx, testEnv, "orders", v1); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// first resolve: cache miss -> store -> populate cache.
	if _, err := store.ActiveFlow(ctx, testEnv, "GET", "/orders/{id}"); err != nil {
		t.Fatalf("resolve (miss): %v", err)
	}
	// second resolve: cache hit (the key now exists).
	cacheKey := "cfg:v1:flow:" + testEnv + ":GET:/orders/{id}"
	if _, ok, _ := cache.Get(ctx, cacheKey); !ok {
		t.Fatal("resolve should have populated the cache")
	}

	// cache down: close the client; the store must still serve from Postgres.
	client.Close()
	fv, err := store.ActiveFlow(ctx, testEnv, "GET", "/orders/{id}")
	if err != nil {
		t.Fatalf("resolve must degrade to the store when the cache is down: %v", err)
	}
	if fv.Version != v1 {
		t.Fatalf("degraded resolve version = %d; want %d", fv.Version, v1)
	}
}

// --- secrets: a connection version stores only secret_ref, never a value ---

func TestConnectionsNeverStoreSecretValue(t *testing.T) {
	ctx := context.Background()
	store, pool, cleanup := newStore(t, nil)
	defer cleanup()

	mustExec(t, pool, `INSERT INTO connections (key, type, created_by) VALUES ('orders-pg','postgres','tester')`)
	mustExec(t, pool, `INSERT INTO connection_versions (conn_key, version, settings, secret_ref, resilience, checksum, created_by)
	                   VALUES ('orders-pg', 1, '{"host":"db"}'::jsonb, 'env:ORDERS_DSN', '{}'::jsonb, 'x', 'tester')`)
	mustExec(t, pool, `INSERT INTO active_pointers (object_type, object_id, version, updated_by)
	                   VALUES ('connection','orders-pg',1,'tester')`)

	defs, err := store.Connections(ctx, testEnv)
	if err != nil {
		t.Fatalf("connections: %v", err)
	}
	if len(defs) != 1 {
		t.Fatalf("connections = %d; want 1", len(defs))
	}
	if defs[0].SecretRef != "env:ORDERS_DSN" {
		t.Fatalf("secret_ref = %q; want env:ORDERS_DSN", defs[0].SecretRef)
	}
	// the whole schema has no column that could hold a secret value: assert the
	// connection_versions row carries only settings/secret_ref/resilience.
	var cols int
	_ = pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name='connection_versions' AND column_name IN ('secret','password','secret_value')`).Scan(&cols)
	if cols != 0 {
		t.Fatalf("connection_versions has %d secret-value columns; want 0", cols)
	}
}

// --- migrations: up then down applies and tears down cleanly (R11 shape pin) ---

func TestMigrationsUpDown(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := startPostgres(t)
	defer cleanup()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply up: %v", err)
	}
	// the resolve-path tables exist with the expected shape.
	for _, tbl := range []string{"flows", "flow_versions", "flow_fixtures", "active_pointers", "audit_log", "connection_versions", "jdm_versions"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, tbl).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", tbl, err)
		}
		if !exists {
			t.Fatalf("table %s missing after migrate up", tbl)
		}
	}
	// the additive 0002 note column is present (expand migration applied).
	var hasNote bool
	_ = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='flow_versions' AND column_name='note')`).Scan(&hasNote)
	if !hasNote {
		t.Fatal("flow_versions.note missing; 0002 expand migration not applied")
	}

	if err := migrations.Rollback(ctx, pool); err != nil {
		t.Fatalf("apply down: %v", err)
	}
	var stillThere bool
	_ = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='flows')`).Scan(&stillThere)
	if stillThere {
		t.Fatal("flows table still present after migrate down")
	}
}

// ---- harness ----

// newStore starts an ephemeral postgres, migrates it, and returns a one-env
// PgStore (env "dev") optionally fronted by cache, plus the raw pool and cleanup.
func newStore(t *testing.T, cache config.Cache) (*config.PgStore, *pgxpool.Pool, func()) {
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

	opts := []config.PgOption{config.WithActor("tester")}
	if cache != nil {
		opts = append(opts, config.WithCache(cache))
	}
	store, err := config.NewPgStore(map[string]*pgxpool.Pool{"dev": pool}, opts...)
	if err != nil {
		pool.Close()
		pgCleanup()
		t.Fatalf("store: %v", err)
	}
	cleanup := func() {
		pool.Close()
		pgCleanup()
	}
	return store, pool, cleanup
}

// newTwoEnvPools starts ONE ephemeral postgres and creates two logical databases
// (dev, staging) within it, returning a pool per env. Promotion across envs is a
// cross-database copy driven by the store.
func newTwoEnvPools(t *testing.T) (map[string]*pgxpool.Pool, func()) {
	t.Helper()
	ctx := context.Background()
	adminDSN, pgCleanup := startPostgres(t)

	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		pgCleanup()
		t.Fatalf("admin pool: %v", err)
	}
	for _, db := range []string{"env_dev", "env_staging"} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			admin.Close()
			pgCleanup()
			t.Fatalf("create db %s: %v", db, err)
		}
	}
	admin.Close()

	pools := map[string]*pgxpool.Pool{}
	for env, db := range map[string]string{"dev": "env_dev", "staging": "env_staging"} {
		dsn := swapDB(adminDSN, db)
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			pgCleanup()
			t.Fatalf("pool %s: %v", env, err)
		}
		if err := migrations.Apply(ctx, p); err != nil {
			pgCleanup()
			t.Fatalf("migrate %s: %v", env, err)
		}
		pools[env] = p
	}
	cleanup := func() {
		for _, p := range pools {
			p.Close()
		}
		pgCleanup()
	}
	return pools, cleanup
}

// putValidated puts a flow version with a one-child response tree, marks it
// validated, and returns its version number.
func putValidated(t *testing.T, store *config.PgStore, pool *pgxpool.Pool, flowID, method, path, childID string) int {
	t.Helper()
	ctx := context.Background()
	v, err := store.PutFlowVersion(ctx, testEnv, config.FlowVersion{
		FlowID: flowID, Method: method, Path: path,
		Tree: flow.Node{ID: "trigger", Type: flow.TypeTrigger,
			Children: []flow.Node{{ID: childID, Type: flow.TypeResponse}}},
	})
	if err != nil {
		t.Fatalf("put %s/%s: %v", flowID, childID, err)
	}
	if err := store.MarkValidated(ctx, testEnv, flowID, v); err != nil {
		t.Fatalf("validate %s v%d: %v", flowID, v, err)
	}
	return v
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// swapDB replaces the database path segment in a postgres DSN.
func swapDB(dsn, db string) string {
	// dsn like postgres://user:pass@host:port/olddb?opts
	q := ""
	if i := strings.Index(dsn, "?"); i >= 0 {
		q = dsn[i:]
		dsn = dsn[:i]
	}
	slash := strings.LastIndex(dsn, "/")
	return dsn[:slash+1] + db + q
}

// newValkeyClient builds a valkey-go client for addr.
func newValkeyClient(t *testing.T, addr string) valkey.Client {
	t.Helper()
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true})
	if err != nil {
		t.Fatalf("valkey client: %v", err)
	}
	return c
}

// --- container harnesses (proven patterns from the sibling slices) ---

var dockerMu sync.Mutex

func startPostgres(t *testing.T) (dsn string, cleanup func()) {
	t.Helper()
	const (
		image = "postgres:16"
		pass  = "cfgpw"
		db    = "cfgdb"
	)
	dockerMu.Lock()
	out, err := exec.Command("docker", "run", "-d",
		"-e", "POSTGRES_PASSWORD="+pass,
		"-e", "POSTGRES_DB="+db,
		"-P", image,
	).CombinedOutput()
	dockerMu.Unlock()
	if err != nil {
		t.Fatalf("docker run postgres: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	cleanup = func() { _ = exec.Command("docker", "rm", "-f", id).Run() }

	hostPort := dockerHostPort(t, id, "5432/tcp", cleanup)
	dsn = fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/%s?sslmode=disable", pass, hostPort, db)

	ctx := context.Background()
	deadline := time.Now().Add(60 * time.Second)
	for {
		pool, perr := pgxpool.New(ctx, dsn)
		if perr == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				pool.Close()
				return dsn, cleanup
			}
			pool.Close()
		}
		if time.Now().After(deadline) {
			cleanup()
			t.Fatalf("postgres not ready within deadline: %v", perr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func startValkey(t *testing.T) (addr string, cleanup func()) {
	t.Helper()
	const image = "valkey/valkey:8-alpine"

	dockerMu.Lock()
	out, err := exec.Command("docker", "run", "-d", "-P", image).CombinedOutput()
	dockerMu.Unlock()
	if err != nil {
		t.Fatalf("docker run valkey: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	cleanup = func() { _ = exec.Command("docker", "rm", "-f", id).Run() }

	hostPort := dockerHostPort(t, id, "6379/tcp", cleanup)
	addr = fmt.Sprintf("127.0.0.1:%s", hostPort)

	deadline := time.Now().Add(30 * time.Second)
	for {
		c, cerr := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true})
		if cerr == nil {
			perr := c.Do(context.Background(), c.B().Ping().Build()).Error()
			c.Close()
			if perr == nil {
				return addr, cleanup
			}
		}
		if time.Now().After(deadline) {
			cleanup()
			t.Fatalf("valkey not ready within deadline: %v", cerr)
		}
		time.Sleep(400 * time.Millisecond)
	}
}

// dockerHostPort resolves the mapped host port for containerPort, polling briefly
// because a -P binding can lag container start.
func dockerHostPort(t *testing.T, id, containerPort string, cleanup func()) string {
	t.Helper()
	for i := 0; i < 40; i++ {
		insp, ierr := exec.Command("docker", "inspect",
			"-f", `{{(index (index .NetworkSettings.Ports "`+containerPort+`") 0).HostPort}}`, id,
		).CombinedOutput()
		p := strings.TrimSpace(string(insp))
		if ierr == nil && p != "" && !strings.Contains(p, "no value") {
			return p
		}
		time.Sleep(250 * time.Millisecond)
	}
	cleanup()
	t.Fatalf("could not resolve mapped host port for %s", containerPort)
	return ""
}
