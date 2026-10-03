# Increment report — Wiring/Integration Join (nzr-rules-engine)

Branch: `feat/wiring-join` · worktree `/home/nuzirwan/project/rule-engine-api/.worktrees/wiring`
Go: `go1.26.2 linux/amd64` · Docker: up (postgres:16 for the tagged integration suite)

This increment performs the WIRING/INTEGRATION JOIN: it wires the four already-built
v1 packages (`internal/{config,connect,decision,observ,auth,flow}`) into the running
HTTP server and `cmd/engine`, adds the control-plane admin `validate`/`dry-run`
endpoints, and honors dry-run write-suppression in the flow action handler. It is the
one increment that edits `cmd/engine` + `internal/httpapi`, plus the permitted
cross-cutting dry-run edit in `internal/flow`.

## What changed

- **`internal/flow` (cross-cutting wiring edit + an in-scope defect fix):**
  - `handlers.go` — `actionHandler.Exec` now checks `observ.IsDryRun(ctx)`: for a write
    op it SKIPS `client.Execute`, records `wrote:"suppressed"` on the dry-run collector
    (`observ.CollectorFrom`), stores a suppressed marker under `SaveAs`, and continues the
    walk. Reads (`query`/`get`/`ping`) still execute. (AC-14)
  - `dryrun.go` — removed the stale package-local `isDryRun`/`withDryRun` shim; the real
    `observ.WithDryRun`/`IsDryRun` seam is used. `isWriteOp` kept.
  - `dryrun_test.go` (new) — proves suppress-on-dry-run (zero Execute calls + collector
    records `wrote:"suppressed"`) and execute-on-live.
  - **`validate.go` — validator reconciliation (in-scope defect fix found during
    integration).** `ValidateTree` previously classified `action`/`set`/`decision`/
    `logger` as strict LEAVES that must not have children. That contradicted the
    interpreter's own proven execution model (those handlers call `walkChildren`; the
    seed flow chains `action→condition→…→set→response` and `TestOrdersEndToEnd` passes on
    exactly that shape), so the publish gate would have rejected every realistic flow.
    Fixed precisely: trigger + the linear nodes (action/set/decision/logger) run their
    children in sequence and impose no child ceiling; the true control nodes
    (condition/switch/sequence/parallel/forEach) keep their `>=1`-child rules; `response`
    stays strictly terminal (`0` children → `leaf_has_children`). All OTHER rules
    (unknown-type, dangling conn/jdm refs, forEach `MaxItems>0`, parallel disjoint
    writes, bounded depth, unique IDs, exactly-one-terminal-Response) are intact.
    `validate_test.go` updated: the linear/deep-linear seed shape validates clean; a
    `response`-with-child still fails `leaf_has_children`; forEach-without-MaxItems and
    the other negative cases still fire.

- **`internal/config`:**
  - `open.go` (new) — `OpenPool(ctx, dsn)` (parse → pool → ping; malformed DSN =>
    Validation, open/ping failure => Upstream) and `Migrate(ctx, pool)` (delegates to the
    embedded `migrations.Apply`).
  - `testdata/seed.json` — added an `authz` JDM (ZEN decision table: `allow:false` for
    subject `"denied"`, else `allow:true`) so an integration test can turn auth ON. The
    active `orders` route and the `order` JDM are unchanged.

- **`internal/httpapi`:**
  - `auth.go` (new) — `AuthConfig` + `BuildAuthMiddleware` (returns `enabled=false` when
    unconfigured so auth is toggleable/bypassable), an `httpKeySource` that fetches +
    parses a JWKS URL to `map[string]crypto.PublicKey` (RSA + EC), a static-JWKS parser
    for inline keys, and `routeResourceAction` (route pattern → resource, method → verb).
  - `server.go` — `Deps` gained `Auth *auth.Middleware`; the flow route is wrapped
    `Authn(Authz(...))` only when `Auth != nil` (bare handler otherwise). A request root
    span opens the whole-walk trace; `observ.WithScope` stamps trace/request ids and
    `observ.EnrichScope` stamps flow_id/version/env the moment the version is pinned. The
    classified-error→HTTP-status mapping is unchanged. Admin routes registered on the same
    mux. The `Store` seam widened to include `GetJDM`/`Connections` (both satisfied by
    memStore and PgStore).
  - `admin.go` (new) — `POST /admin/flows/validate` (structural `ValidateTree` with
    store-backed ref resolution + each fixture through the interpreter with a mocked
    zero-I/O registry; publish-blocking `ok:false`, always HTTP 200) and
    `POST /admin/flows/dry-run` (`observ.WithDryRun` + collector; writes suppressed; returns
    the node trace + response + errors).
  - `admin_test.go` (new) — structural-bad-tree blocks (200/ok:false), failing-fixture
    blocks (200/ok:false + diff), good-flow passes (ok:true), dry-run suppresses the write
    (zero Execute calls) and records `wrote:"suppressed"`.
  - `integration_test.go` — added `TestWiringAuthEnforced` (401 no token / 200 valid+allowed
    / 403 valid+denied, real RS256 token + static JWKS + the seed `authz` JDM),
    `TestWiringAdminValidate` (seed flow `ok:true`; a `response`-with-child flow `ok:false`
    with `leaf_has_children`), and `TestWiringAdminDryRun` (dry-run over the orders flow:
    write recorded `wrote:"suppressed"`, ZERO REST-stub hits, read proven by the branch
    taken). Reuses the proven `startPostgres` + httptest stub harness.

- **`cmd/engine/main.go`** — rewired: OTel tracer provider (`observ.NewOTelTracer` over
  `sdktrace.NewTracerProvider` with a dependency-free discarding exporter so the whole-walk
  trace + trace_id exist; a prod deploy swaps an OTLP batcher through the same seam) +
  slog logger + `observ.NewMetrics`; store selection (`-config-dsn`/`CONFIG_DSN` → `OpenPool`
  + `Migrate` + `NewPgStore{env:pool}`, else the in-memory seed, with a startup log naming
  the choice); registry over `drivers.All()` (incl. valkey) + env SecretProvider; decision
  engine; toggleable auth via `httpapi.BuildAuthMiddleware` (logs ENABLED/DISABLED); the
  interpreter + `httpapi.NewServer`. Graceful shutdown order: drain → registry.Close →
  engine.Close → pgx pool.Close → ShutdownProvider (flush traces). New flags/env:
  `-config-dsn`/`CONFIG_DSN`, `-env`/`ENV`, `-auth-*`/`AUTH_*`.

- **`README.md`** — documents the store selection, the auth toggle + flags, the admin
  endpoints + contracts, the OTel/metrics wiring, and the graceful-shutdown order. The
  Makefile `test-integration` target already points at `./internal/httpapi` (unchanged).

## VERIFY — real command output

### `CGO_ENABLED=1 go build ./...` → exit 0

```
### CGO_ENABLED=1 go build ./...
exit=0
```

### `CGO_ENABLED=1 go vet ./...` → clean (exit 0)

```
### CGO_ENABLED=1 go vet ./...
exit=0
```

### `CGO_ENABLED=0 go build ./internal/decision` (stub still compiles) → exit 0

```
### CGO_ENABLED=0 go build ./internal/decision
exit=0
```

### `CGO_ENABLED=1 go test ./...` → all pass

```
?   	nzr-rules-engine/cmd/engine	[no test files]
ok  	nzr-rules-engine/internal/auth	(cached)
ok  	nzr-rules-engine/internal/config	(cached)
ok  	nzr-rules-engine/internal/connect	(cached)
?   	nzr-rules-engine/internal/connect/drivers	[no test files]
ok  	nzr-rules-engine/internal/decision	(cached)
ok  	nzr-rules-engine/internal/flow	(cached)
ok  	nzr-rules-engine/internal/httpapi	(cached)
ok  	nzr-rules-engine/internal/observ	(cached)
?   	nzr-rules-engine/migrations	[no test files]
```

### `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` → all pass (Docker)

```
=== RUN   TestOrdersEndToEnd
--- PASS: TestOrdersEndToEnd (4.77s)
    --- PASS: TestOrdersEndToEnd/paid_high-value_order_->_expedited
    --- PASS: TestOrdersEndToEnd/paid_low-value_order_->_standard
=== RUN   TestWiringAuthEnforced
--- PASS: TestWiringAuthEnforced (4.96s)     # 401 no token / 200 valid+allowed / 403 denied
=== RUN   TestWiringAdminValidate
--- PASS: TestWiringAdminValidate (4.18s)    # seed ok:true; broken flow ok:false (leaf_has_children)
=== RUN   TestWiringAdminDryRun
--- PASS: TestWiringAdminDryRun (4.12s)      # write suppressed, zero external POSTs
PASS
ok  	nzr-rules-engine/internal/httpapi	18.053s
```

(The suite also re-runs the httpapi unit tests under the tags — all PASS; elided here.)

### Race sanity (`CGO_ENABLED=1 go test -race ./internal/flow ./internal/httpapi`)

```
ok  	nzr-rules-engine/internal/flow	1.089s
ok  	nzr-rules-engine/internal/httpapi	1.053s
```

## Notes

- **Dry-run of a write-output-dependent flow.** The seed orders flow's `Set` reads
  `decision.body.shipping` — the RETURN VALUE of the write action. Under dry-run that write
  is suppressed, so its body is absent and the dependent `Set` surfaces a Validation error.
  This is the correct, expected consequence of suppressing a write a later node consumes:
  `/admin/flows/dry-run` still returns the node trace (the deliverable) and performs NO write;
  the error is reported in the response `errors` array. The AC-14 assertions
  (`wrote:"suppressed"` + zero external POSTs + reads still ran) are what the dry-run case
  proves.
- **Per-node trace scope.** The interpreter records a collector step only on the
  write-suppression path (the cross-cutting edit this task added). A full automatic per-node
  trace (TraceNode integration for every node) lives in `internal/flow` and is out of this
  wiring task's scope; the dry-run trace therefore shows the suppressed write, and the read
  executing is proven by the branch taken.
- **OTel exporter.** No OTLP/stdout exporter module is in `go.mod`; the engine wires a
  dependency-free discarding `SpanExporter` so the real tracer provider + trace_id exist. A
  production deploy swaps in a batch OTLP exporter through the same `observ.NewTracerProvider`
  seam — a startup detail, not a seam change.
