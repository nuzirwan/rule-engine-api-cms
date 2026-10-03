# Implementation Plan — Config-Store Run Mode (nzr-rules-engine)

Branch `feat/config-run-mode`, worktree `/home/nuzirwan/project/rule-engine-api/.worktrees/config-run`.
All paths below are relative to that worktree root; the coder MUST use absolute paths rooted there.

## Goal

Wire the already-built, unit-tested `config.PgStore` + `config.ValkeyCache` into the runnable
`cmd/engine` binary so the engine can run against a real Postgres + Valkey as its OWN config
backing store, seeding the committed `internal/config/testdata/seed.json` into Postgres through
the store (not raw SQL). Config tables live in a dedicated Postgres schema (default `rule_engine`),
never `public`. When `CONFIG_DSN` is unset the binary keeps today's in-memory seed path byte-for-byte.

## Hard constraints (read before coding)

- **CGO_ENABLED=1 for everything that links the decision engine (ZEN).** All build/vet/test
  commands below use `CGO_ENABLED=1` except the explicit `CGO_ENABLED=0 go build ./internal/decision`
  stub check.
- **Ephemeral containers ONLY.** The new integration test and every self-verification step MUST
  spin up ephemeral `postgres:16` + `valkey` Docker containers using the PROVEN harness already in
  `internal/config/pgstore_integration_test.go` (`startPostgres`/`startValkey`/`dockerHostPort`).
  Do NOT connect to any external/user Postgres or Valkey during the build or tests. The user's
  real DB (from the parent `.env`) is never touched by the build.
- **Dependencies frozen.** stdlib `net/http` + existing deps only (`pgx/v5`, `pgxpool`,
  `valkey-go`). No new module. The `.env` loader is hand-rolled (no `godotenv`).
- **Parameterized SQL only**, mirroring `PgStore.PutFlowVersion` / `SetActive`. Never interpolate
  values. The ONE identifier that is interpolated (the schema name in `CREATE SCHEMA` /
  `SET search_path`) MUST first pass the `^[A-Za-z_][A-Za-z0-9_]*$` validation (step 2).
- **Secrets stay out of the DB.** Connection seeding writes `secret_ref` only into
  `connection_versions.settings`/`secret_ref`; never a secret value (schema has no column for one).
  Redact passwords in all startup logs.

## Key facts discovered during exploration (ground truth for the coder)

- `cmd/engine/main.go` today: `run(addr, seedPath, logger)` does ONLY `config.LoadSeed(seedPath)`
  → `*memStore`, then builds registry/decision/interp/httpapi and serves. No DSN, no migrations,
  no cache. STATE.md's "Wiring join" note overstates this (it claims a `CONFIG_DSN` PgStore-vs-seed
  switch exists — it does not). This increment actually adds that switch.
- `config.Store` seam (store.go) = `ActiveFlow / GetJDM / Connections / PutFlowVersion / SetActive /
  Ping`. Both `*memStore` and `*PgStore` satisfy it. `jdmLoader{store}` adapts to `decision.JDMLoader`.
- `PgStore` (pgstore.go) EXISTS with `NewPgStore(pools map[string]*pgxpool.Pool, opts...)`,
  options `WithCache/WithActor/WithCacheTTL`, and methods `ActiveFlow/GetJDM/Connections/
  PutFlowVersion/SetActive/MarkValidated/PromoteVersion/Ping/AuditTrail`. It has **NO public write
  method for JDMs or connections** — the integration tests insert those via raw SQL. This increment
  ADDS `PutJDMVersion` + `PutConnectionVersion` (step 3).
- `PutFlowVersion` lands a version with `validated=false`; `SetActive` REFUSES an un-validated
  version. So the Postgres seed path MUST call `MarkValidated` between `PutFlowVersion` and
  `SetActive` for any flow marked active (the in-memory `memStore.Seed` does not, because memStore's
  `SetActive` has no validated gate — this is a real behavioral difference the coder must handle).
- `ValkeyCache` (cache.go): `NewValkeyCache(client valkey.Client, opts...)` +
  `StartInvalidationConsumer(ctx)` + `Ping` + `Close`. Caller owns the `valkey.Client` lifecycle;
  `Close` stops the subscriber but does NOT close the client.
- `migrations.Apply(ctx, Execer)` runs embedded `*.up.sql` in order. `0001_init.up.sql` uses
  UNQUALIFIED `CREATE TABLE flows ...` etc., so whichever schema is first on `search_path` at
  `Apply` time receives the tables. This is the lever for the dedicated-schema requirement.
- `seedFile` schema (seed.go): `{env, flows[], jdms[], connections[]}`. `seedFlow` has `Active bool`,
  `Tree flow.Node`, `Fixtures`. `seedJDM` has `{id, version, doc json.RawMessage}`. `seedConnDef`
  has `{key, type, settings, secretRef, resilience{timeoutMs,maxAttempts}}`. The committed
  `testdata/seed.json` uses `env: ""` (empty string) — the Postgres seed will use that same env key.
- Codec helpers available in-package: `checksum(raw []byte) string` (sha256 hex),
  `withTx(ctx, pool, fn)`, `classifyPg(op, err)`, `newErr/wrapErr`, audit action consts
  (`actCreateVersion/actPublish/...`) and object-type consts (`objFlow/objJDM/objConnection`).
- End-to-end serving reference: `internal/httpapi/integration_test.go` `TestOrdersEndToEnd` builds
  the real registry+engine+handler over a seeded store and asserts `GET /orders/{id}` with an
  ephemeral postgres (`orders` data table) + an in-process `httptest` REST stub, overriding the
  seed's `orders-pg` DSN and `ship-rest` baseURL. Reuse this exact approach for assertion (d).
- Parent-workspace `.env.example` already defines the exact keys the task lists. Mirror it.

## Design decisions (made here; no separate design doc)

1. **Schema isolation via pool `AfterConnect`.** Build the config `*pgxpool.Pool` from
   `pgxpool.ParseConfig(dsn)`, then set `cfg.AfterConnect = func(ctx, conn) { conn.Exec(ctx, "SET
   search_path TO "+schema) }` so EVERY pooled connection (and every reconnect) runs in the dedicated
   schema. Before `migrations.Apply`, run `CREATE SCHEMA IF NOT EXISTS <schema>` once (on a connection
   that already has the search_path set, or explicitly qualified). Because the migration DDL is
   unqualified, all `CREATE TABLE`s then land in `<schema>.*` and `public` is untouched. Chosen over
   editing the migration SQL (keeps migrations env-agnostic and reusable by the `migrate` CLI) and
   over a DSN `search_path` option (AfterConnect is robust across reconnects and explicit).
2. **New store write methods instead of raw SQL in main.** Add `PutJDMVersion` and
   `PutConnectionVersion` to `PgStore`, transactional, parameterized, mirroring `PutFlowVersion`
   (identity upsert `ON CONFLICT DO NOTHING` → lock → compute next version → insert version →
   move active pointer → audit row). This keeps `cmd/engine` free of SQL and makes the seed load
   "through the store" as the task requires.
3. **Idempotent seeding by check-active-then-skip.** Before seeding, resolve the seed's active flow
   via `store.ActiveFlow(env, method, path)`. If it resolves (NotFound ⇒ absent), skip the whole
   seed. This is simple, correct, and avoids relying on per-object upsert races. (The new write
   methods are ALSO individually safe to re-run — identity upserts + version-bump — but the top-level
   skip is the primary idempotency gate and what the test asserts.)
4. **`.env` loader is a tiny internal package** (`internal/envfile`) so both `cmd/engine` and the
   new integration test can use it, and it is unit-testable without Docker.
5. **Run-mode selection stays inside `run(...)`** in `cmd/engine/main.go`, branching on whether
   `CONFIG_DSN` is set after the `.env` is loaded. The in-memory branch is the EXACT current code.

---

# Implementation Plan

- [ ] 1. Add a dependency-free `.env` loader package `internal/envfile`.
      `func Load(path string) error` reads the file if it exists (a missing file is NOT an error —
      return nil), parses `KEY=VALUE` lines, trims surrounding whitespace, ignores blank lines and
      `#` comments, strips optional surrounding single/double quotes from the value, and calls
      `os.Setenv(key, val)` ONLY when `os.LookupEnv(key)` reports the key is unset (process env wins,
      so real env overrides the file). Reject malformed lines (no `=`) with a wrapped error naming the
      line number. No new imports beyond `bufio`, `os`, `strings`, `fmt`.
      Files: `internal/envfile/envfile.go`, `internal/envfile/envfile_test.go`
      Verify: `CGO_ENABLED=1 go test ./internal/envfile` — unit tests pass (cases: missing file ⇒
      nil + nothing set; comments/blanks skipped; quoted value unquoted; pre-set env var NOT
      overridden; malformed line ⇒ error).

- [ ] 2. Add a schema-identifier validator + schema-scoped pool constructor in a new file
      `internal/config/connect_pg.go` (package `config`).
      Add `func ValidateSchemaName(s string) error` enforcing `^[A-Za-z_][A-Za-z0-9_]*$` (reject
      empty / bad chars; this is the ONLY place the schema name is interpolated into SQL).
      Add `func OpenSchemaPool(ctx context.Context, dsn, schema string) (*pgxpool.Pool, error)`:
      validate `schema`; `pgxpool.ParseConfig(dsn)`; set `cfg.AfterConnect` to run
      `SET search_path TO <schema>`; `pgxpool.NewWithConfig`; then on one acquired conn run
      `CREATE SCHEMA IF NOT EXISTS <schema>` (so a brand-new DB gets the schema before migrations).
      Return the pool. Wrap failures with `wrapErr(Upstream, ...)` / `newErr(Validation, ...)` to
      match the package taxonomy.
      Files: `internal/config/connect_pg.go`, `internal/config/connect_pg_test.go`
      Verify: `CGO_ENABLED=1 go test ./internal/config` — new unit test for `ValidateSchemaName`
      passes (accepts `rule_engine`, `_x`, `A1`; rejects ``, `1x`, `a-b`, `a;b`, `public schema`).
      (OpenSchemaPool itself is exercised by the integration test in step 7, not unit tests.)

- [ ] 3. Add `PutJDMVersion` and `PutConnectionVersion` write methods to `internal/config/pgstore.go`,
      mirroring `PutFlowVersion`'s transactional style (identity upsert `ON CONFLICT (id/key) DO
      NOTHING` → `SELECT ... FOR UPDATE` lock → `max(version)+1` → insert version row with
      `checksum(...)` + `created_by=s.actor` → `INSERT ... active_pointers ON CONFLICT (object_type,
      object_id) DO UPDATE` to point at the new version → `audit_log` `create_version` then `publish`
      rows). Signatures:
      `func (s *PgStore) PutJDMVersion(ctx context.Context, env, jdmID string, doc []byte, version int) (int, error)`
      — inserts into `jdms` + `jdm_versions` (columns `jdm_id, version, jdm, checksum, created_by`)
      and points the `objJDM` pointer at it. If `version<=0`, auto-assign `max+1`.
      `func (s *PgStore) PutConnectionVersion(ctx context.Context, env string, def connect.ConnectionDef) (int, error)`
      — inserts into `connections` + `connection_versions` (columns `conn_key, version, settings,
      secret_ref, resilience, checksum, created_by`), marshalling `def.Settings` and `def.Resilience`
      to jsonb, storing `def.SecretRef` (never a secret value), and points the `objConnection` pointer
      at it. Both are additive and do NOT change existing method behavior. Add audit action reuse
      (`actCreateVersion`, `actPublish`); no new seam methods on the `Store` interface.
      Files: `internal/config/pgstore.go`
      Verify: `CGO_ENABLED=1 go build ./internal/config` exit 0; `CGO_ENABLED=1 go vet ./internal/config`
      clean. (Behavioral assertions live in step 7's integration test.)

- [ ] 4. Add a Postgres seeding helper that loads the seed document into a `*PgStore` idempotently.
      In a new file `internal/config/seed_pg.go` (package `config`, so it can reuse the private
      `seedFile`/`seedFlow`/`seedJDM`/`seedConnDef` types, `toResiliencePolicy`, and the new write
      methods), add:
      `func SeedPgStore(ctx context.Context, s *PgStore, raw []byte) (seeded bool, err error)`.
      Parse `raw` into `seedFile` (reuse existing unmarshal; a decode failure is `Validation`).
      IDEMPOTENCY GATE: for the first flow in the seed, call `s.ActiveFlow(ctx, env, method, path)`;
      if it returns nil error, return `(false, nil)` (already seeded — skip). Otherwise, for each flow:
      `PutFlowVersion` → if `f.Active`, `MarkValidated` then `SetActive` (REQUIRED because PgStore
      blocks publishing an un-validated version — differs from memStore). For each JDM: `PutJDMVersion`
      with `j.Doc` bytes + `j.Version`. For each connection: `PutConnectionVersion` with a
      `connect.ConnectionDef` built exactly as `memStore.Seed` builds it (via `toResiliencePolicy`).
      Return `(true, nil)`.
      Files: `internal/config/seed_pg.go`
      Verify: `CGO_ENABLED=1 go build ./internal/config` exit 0; full behavior asserted in step 7.

- [ ] 5. Wire run-mode selection into `cmd/engine/main.go`.
      Add flags/env: keep `-addr` but prefer `ENGINE_ADDR` when set (env overrides the flag default;
      document precedence); add `-env` flag (default `".env"`). In `main`, call
      `envfile.Load(*envPath)` BEFORE reading any config env (a missing file is fine). Read
      `CONFIG_DSN`, `CONFIG_SCHEMA` (default `"rule_engine"`), `VALKEY_ADDR`, `ENGINE_ADDR`.
      In `run(...)`:
      - **If `CONFIG_DSN` is set (config-store mode):**
        `config.OpenSchemaPool(ctx, dsn, schema)` → `migrations.Apply(ctx, pool)` → if `VALKEY_ADDR`
        set, build a `valkey.Client`, `config.NewValkeyCache(client)`, `cache.StartInvalidationConsumer(ctx)`
        (else nil cache) → `config.NewPgStore(map[string]*pgxpool.Pool{env: pool}, WithCache(cache))`
        with `env=""` to match the seed → read the seed bytes from `seedPath` →
        `config.SeedPgStore(ctx, store, raw)` → log connected host/db/schema with the password
        REDACTED and whether seeding ran → use this `store` for the rest of `run`. Defer closing the
        pool, the cache (`cache.Close()`), and the valkey client in LIFO order.
      - **Else (in-memory mode):** the EXACT current code — `config.LoadSeed(seedPath)` → memStore.
      - Both branches converge on the existing registry/decision/interp/httpapi build + serve +
        graceful shutdown. Use `addr` resolved as: `ENGINE_ADDR` if set, else the `-addr` flag.
      Keep a `redactDSN(dsn string) string` local helper (parse with `net/url`, blank the password).
      Files: `cmd/engine/main.go`
      Verify: `CGO_ENABLED=1 go build ./cmd/engine` exit 0; `CGO_ENABLED=1 go vet ./cmd/engine` clean;
      with no `CONFIG_DSN` set, `CGO_ENABLED=1 go test ./...` stays green (in-memory path unchanged).

- [ ] 6. Create a committed `.env.example` in the worktree root (do NOT create a real `.env` — it is
      gitignored and the user maintains it). Mirror the parent-workspace `.env.example` keys with the
      config-store defaults documented: `CONFIG_DSN=` (empty ⇒ in-memory), `CONFIG_SCHEMA=rule_engine`,
      `VALKEY_ADDR=127.0.0.1:6379`, `ENGINE_ADDR=:8080`, `ORDERS_PG_DSN=`, `SHIP_REST_BASE_URL=`,
      `RUN_REST_STUB=`, `REST_STUB_PORT=`. Add comments explaining each. (The engine's own config
      store is CONFIG_* ; ORDERS_PG_DSN/SHIP_REST_* override the seed's data-plane connection
      endpoints as in the existing httpapi integration test.)
      Files: `.env.example`
      Verify: file present and valid KEY=VALUE lines; `CGO_ENABLED=1 go test ./internal/envfile`
      against a copy of it parses without error (add a test case that loads the example file).

- [ ] 7. Add a NEW build-tagged integration test for the config-store run mode.
      New file `internal/config/configrun_integration_test.go` with build tag
      `//go:build integration && cgo` (package `config_test`). Reuse the PROVEN ephemeral harness from
      `pgstore_integration_test.go` (`startPostgres`, `startValkey`, `newValkeyClient`,
      `dockerHostPort`) — same package test binary, so those helpers are directly in scope. One test
      `TestConfigRunModeSeedsIntoSchema` that:
      - opens the schema-scoped pool via `config.OpenSchemaPool(ctx, dsn, "rule_engine")`, runs
        `migrations.Apply`, builds a `ValkeyCache` over an ephemeral valkey, and a `PgStore`
        (`env=""`) with the cache;
      - reads `testdata/seed.json` and calls `config.SeedPgStore` → asserts `seeded==true`;
      - **(a)** asserts config tables exist in schema `rule_engine` and NOT in `public`:
        `SELECT count(*) FROM information_schema.tables WHERE table_schema='rule_engine' AND
        table_name='flows'` == 1 AND `... table_schema='public' AND table_name='flows'` == 0;
      - **(b)** asserts the seed flow/JDM/connections are written and the flow is ACTIVE:
        `store.ActiveFlow(ctx, "", "GET", "/orders/{id}")` resolves to the seeded version;
        `store.GetJDM(ctx, "", "order")` returns bytes+version; `store.Connections(ctx, "")` returns
        the two defs (`orders-pg`, `ship-rest`) with `ship-rest` carrying no secret value;
      - **(c)** calls `config.SeedPgStore` a SECOND time → asserts `seeded==false`, no error, and no
        duplicate version (`SELECT count(*) FROM flow_versions WHERE flow_id='orders-expedite'` == 1,
        same for `jdm_versions`/`connection_versions`);
      - **(d)** serves `GET /orders/{id}` end-to-end reading config FROM the PgStore: stand up an
        ephemeral postgres `orders` DATA table (rows id=1 amount=1500 status='paid', id=2 amount=500
        status='paid') + an in-process `httptest` REST stub, fetch `store.Connections`, override the
        `orders-pg` DSN + `ship-rest` baseURL (as `httpapi/integration_test.go` does — copy
        `overrideEndpoints`/`cloneSettings`), build the real `connect.Registry` + `decision.Engine` +
        `httpapi.NewHandler(store, flow.New(), deps)`, issue `GET /orders/1` and `GET /orders/2`,
        assert 200 + `shipping` == `expedited`/`standard` and exactly the matching REST path hit.
        This proves the handler resolves the flow+JDM+connections from the PgStore, not a memStore.
      Note the ephemeral-only constraint in a file comment: this test uses TWO ephemeral postgres
      containers (one for config store, one for the orders data table) + one ephemeral valkey; it
      NEVER reads CONFIG_DSN/ORDERS_PG_DSN from the environment.
      Files: `internal/config/configrun_integration_test.go`
      Verify: `CGO_ENABLED=1 go test -tags 'integration cgo' -run TestConfigRunModeSeedsIntoSchema
      ./internal/config` passes with Docker up (coder pastes REAL output).

- [ ] 8. Update `docs/STATE.md` to correct the overstated wiring note and record this increment.
      In the "Wiring join" section, correct the claim that `cmd/engine` already selected
      PgStore-vs-seed behind `CONFIG_DSN` — it did not; only `LoadSeed` ran. Add a new DONE subsection
      (e.g. "### Config-store run mode — DONE") stating: `cmd/engine` now selects config-store mode
      when `CONFIG_DSN` is set (dedicated `rule_engine` schema via `CONFIG_SCHEMA`, `CREATE SCHEMA`
      + `search_path` before `migrations.Apply`, `ValkeyCache` + invalidation consumer when
      `VALKEY_ADDR` set, seed loaded INTO the PgStore idempotently through new `PutJDMVersion` /
      `PutConnectionVersion` write methods + `PutFlowVersion`/`MarkValidated`/`SetActive`), and keeps
      the in-memory seed path exactly as-is when `CONFIG_DSN` is unset. Mention the new
      `internal/envfile` loader and the new build-tagged integration test (ephemeral postgres+valkey).
      Bump "Last updated".
      Files: `docs/STATE.md`
      Verify: `CGO_ENABLED=1 go build ./...` still exit 0 (doc-only change does not break build);
      re-read STATE.md to confirm the correction and new subsection are present and accurate.

- [ ] 9. Full self-verification gate (ephemeral containers only) — paste REAL output for each.
      Run from the worktree root:
      - `CGO_ENABLED=1 go build ./...` → exit 0
      - `CGO_ENABLED=1 go vet ./...` → clean
      - `CGO_ENABLED=1 go test ./...` → all unit tests pass (in-memory path + envfile + validator)
      - `CGO_ENABLED=0 go build ./internal/decision` → exit 0 (stub compiles)
      - `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/config` → all integration tests
        pass, including the existing AC-9..AC-16 tests AND the new `TestConfigRunModeSeedsIntoSchema`
        (assertions a–d)
      - `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` → existing end-to-end test
        stays green (confirms the in-memory seed path is untouched)
      Files: none (verification only)
      Verify: every command above produces the stated outcome; the coder records the real console
      output in the FEAT/commit notes. Any failure is fixed before claiming done.

## Idempotency summary (what makes re-runs safe)

- Top-level gate in `SeedPgStore`: if the seed's active flow already resolves, skip entirely
  (asserted by test (c), `seeded==false`).
- Each write method is independently safe: identity rows use `ON CONFLICT DO NOTHING`; version
  numbers are computed under a `FOR UPDATE` lock; the active pointer uses `ON CONFLICT ... DO UPDATE`.
- `CREATE SCHEMA IF NOT EXISTS` and the `migrate` applier are safe to re-run against an existing
  schema (migrations are not re-tracked here, but `CREATE TABLE` on an already-seeded DB is avoided
  because the seed-skip path short-circuits before any DDL re-run is attempted on a fresh process;
  NOTE for coder: `migrations.Apply` runs unconditionally in config-store mode and `0001` uses bare
  `CREATE TABLE` — if a second PROCESS start against an already-migrated schema must not error, make
  the migration idempotent-tolerant by catching "already exists" OR gate `migrations.Apply` behind a
  table-existence check. Prefer the existence check: before `Apply`, query
  `information_schema.tables` for `flows` in `<schema>`; skip `Apply` if present. Implement this in
  step 5's config-store branch and cover it with the second-run assertion in step 7(c).)

## Open assumptions (made to avoid stalling)

- The config-store seed uses `env=""` (matching the committed `testdata/seed.json`), not `"dev"`.
  The one-env pool map is `{"": pool}`.
- `ORDERS_PG_DSN` / `SHIP_REST_BASE_URL` / `RUN_REST_STUB` / `REST_STUB_PORT` are loaded by the
  `.env` loader and documented, and remain runtime overrides of the seed's data-plane connection
  endpoints (the config store itself only needs CONFIG_DSN/CONFIG_SCHEMA/VALKEY_ADDR/ENGINE_ADDR).
  Full data-plane auto-stub wiring in `cmd/engine` is out of scope for this increment unless trivial;
  the integration test proves end-to-end serving with explicit overrides.
```
