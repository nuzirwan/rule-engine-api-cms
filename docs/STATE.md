# nzr-rules-engine — project state & handoff

Single source of truth for picking up work in a fresh session. Everything below is committed on
the `mainline` branch. Read this first, then the docs it points to.

Last updated: 2026-10-03 · mainline HEAD at handoff: `df1a748` (feat/strapi-cms fast-forwarded in)

## LATEST STATUS (read this first)
The **v1 engine is complete and proven LIVE**, the **config-management Admin API (control plane) is
BUILT, reviewed APPROVED, and merged**, and the **Strapi CMS (the original ask) is now BUILT,
reviewed APPROVED, and merged** — see "Strapi CMS — DONE" and "Config-management admin API — DONE"
below. The CMS integrates with the engine **ONLY** via the admin HTTP API and runs on its **own
isolated Postgres db + schema** (never `rule_engine`/`public`). **Both live-run bugs are fixed with
tests** (`/readyz` false-negative; per-object idempotent seeding). What remains is productionize
(see NEXT).

The v1 engine is proven LIVE against the user's real Postgres + Valkey. Config
store runs in `matcha` DB under the dedicated `rule_engine` schema (the user's `public` tables are
untouched); the data-source `fmc-pg` connection reads the user's real `fmc_utility` DB, schema
`fmc_order`, table `order_status`. Three config-defined flows are active, two of them verified live:
- `GET /order/{order_id}` and `GET /order/msisdn/{msisdn}` — read `fmc_order.order_status`, run a
  ZEN rule (`payment_status=='PAID' => action="proceed_fulfillment"`, else `"await_payment"`),
  return the row subset + decided `action`. CONFIRMED working live (ORD-TEST-TRK→proceed,
  ORD-TEST-BAD→await, msisdn 628111015450→latest row). These endpoints exist purely from CONFIG
  rows (zero-restart config-driven router), no business code.
- `GET /orders/{id}` — legacy demo flow, ignore (its `orders` DB doesn't exist).

### How to RUN THE ENGINE LIVE (what the user was doing)
Config is driven by `.env` (gitignored). Keys the engine READS: `CONFIG_DSN` (config store, e.g.
`postgres://root:root@127.0.0.1:5432/matcha?sslmode=disable`), `CONFIG_SCHEMA` (`rule_engine`),
`VALKEY_ADDR` (`127.0.0.1:6379`), `ENGINE_ADDR` (`:8080`). The data-source DSN (`fmc_utility`) is
baked in the seed's `fmc-pg` connection (NOT read from env). The Go engine now
lives under `engine/` (its own go.mod); run from there. Build + run:
```
cd engine && CGO_ENABLED=1 go build -o bin/engine ./cmd/engine && ./bin/engine
# then: curl -s http://127.0.0.1:8080/order/ORD-TEST-TRK ; echo
#       curl -s http://127.0.0.1:8080/order/ORD-TEST-BAD ; echo
#       curl -s http://127.0.0.1:8080/order/msisdn/628111015450 ; echo
```
NOTE on `.env` cruft: `SHIP_REST_BASE_URL`/`RUN_REST_STUB`/`REST_STUB_PORT` are UNUSED leftovers
(old shipping demo) — the engine ignores them; safe to delete.

### TWO LIVE BUGS — BOTH FIXED (with tests), merged on mainline
1. **`/readyz` false-negative — FIXED.** The probe gated on `registry.HealthCheck` over every
   data-source connection, so a down downstream source (e.g. the external `fmc_utility` DB) took the
   whole engine out of rotation. Now gates on the **config-store `Ping` only** (the engine's
   serve-dependency); data-source health is per-request (`502/504`), not a readiness gate.
   Pinned by `internal/httpapi/ops_test.go` (healthy store + failing data-source registry ⇒ 200).
2. **Coarse idempotent seeding — FIXED.** `SeedPgStore` was all-or-nothing ("any active flow exists
   ⇒ skip ALL"), silently dropping new flows added to an already-seeded store (destructive
   `DROP SCHEMA … CASCADE` workaround). Rewritten to **per-object create-if-absent** via new
   `FlowExists`/`JDMExists`/`ConnectionExists` PK reads: each absent object written, each present
   one skipped (bootstrap-only, no version churn). Pinned by
   `internal/config/seed_pg_integration_test.go` (seed #1 writes alpha; seed #2 adds only beta,
   alpha byte-identical; seed #3 pure no-op).

### Endpoint planes (clarified)
- **Public (data plane):** config-defined business routes (the `/order/*` flows). Live, dynamic.
- **Ops:** `/livez` `/readyz` `/metrics` — code-registered, live (`/readyz` bug now fixed).
- **Admin (control plane):** BUILT — a privileged, operator-authed `/admin/*` HTTP surface over the
  config-write store methods (see "Config-management admin API — DONE" below). This is the clean API
  Strapi integrates against; the as-built contract is in `docs/lld/slice-f-admin-api.md`.


## What this project is
A config-driven API engine (Go, data plane) + a planned Strapi CMS (control plane, separate
build). APIs are defined as config (flow trees + GoRules ZEN decisions), not code. Full rationale
in the docs below.

## Read these to get context (in order)
- `docs/hld.md` — High-Level Design: architecture, 8 ADRs, risk register (R0–R14), acceptance
  criteria (AC-S1..S3 spike gate + AC-1..28 v1), phased scope.
- `docs/lld.md` — LLD overview: package layout, how slices compose, AC coverage map, build order.
- `docs/lld-contracts.md` — the FROZEN Go interfaces every package implements. Build against these.
- `docs/lld/slice-{a,b,c,d,e}-*.md` — detailed per-package designs.
- `docs/phase0-spike-report.md` — proven build facts (zen-go/v2 @ v2.1.2, CGO, **glibc base image
  required**, API mapping in Slice C's "zen-go v2 mapping" block).

## Environment (verified)
Go 1.26 (go.mod floor 1.22) · gcc/CGO present · **CGO_ENABLED=1 required** for ZEN · Docker up
(ephemeral postgres:16 for integration tests) · stdlib net/http only (no chi/gin).

## Repo layout (post-restructure)
Two top-level components: `engine/` (the Go rules engine, own go.mod, module
`nzr-rules-engine`) and `cms/` (the Strapi CMS, own package.json + own Postgres
db+schema). `docs/` and system docs stay at root. Run all Go commands from
`engine/`; the root `Makefile` targets cd into it for you.

## Build / test commands (run from engine/)
```
cd engine
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi   # needs Docker
CGO_ENABLED=0 go build ./internal/decision                         # stub must compile
```

## DONE (on mainline)
- Design: HLD + ADRs + 5 LLD slices + reconciled contracts.
- Phase 0 spike: GO (zen-go CGO + ZEN condition + nested flow proven). Throwaway packages removed.
- **v1 thin slice (reviewed APPROVED, merged):** real packages `internal/{flow,connect,decision,
  config,observ,httpapi}` + `cmd/engine` serve `GET /orders/{id}` end-to-end
  (Trigger→Postgres read→ZEN condition→branch REST→Set→Response) over real HTTP against real
  Postgres + REST stub, two branches. Build/vet/unit/integration all green.

### v1 packages — now BUILT (parallel fan-out merged to mainline @ 8ba7d60)
All four fan-out increments landed and merged; full gate green (build, vet, unit, and
Docker-backed integration against real postgres:16 + valkey):
- `internal/config` — **real** Postgres Store (pgxpool) + embedded migrations + config
  versioning (immutable versions, active_pointer, publish/rollback, audit) + valkey-go Cache
  (namespaced/versioned keys, TTL+jitter, pub/sub invalidation, degrade-to-store, defensive
  decode). The in-memory memStore+seed is kept behind the same seam (used by httpapi today).
- `internal/connect` — valkey connector added; full resilience envelope (timeout→breaker→retry
  with jitter, idempotent-only retry, idempotency-key dedup lock); per-instance breakers (R10).
- `internal/observ` — OTel Tracer/Span + RED Prometheus metrics + central Redactor + dry-run
  TraceCollector/WithDryRun/IsDryRun; slog Logger kept.
- `internal/auth` — JWKS Authenticator (rotation without restart) + net/http Authn/Authz
  middleware (deny-by-default) + ZEN Authorizer.
- `internal/flow` — switch/parallel/forEach/decision/logger handlers + depth/work/ctx budget +
  extended ValidateTree. (A real data race on the shared budget was found and fixed via -race.)

Dep notes from the merge: go directive is now **1.26.0**; added valkey-go v1.0.78,
sony/gobreaker/v2, golang-jwt/v5, prometheus/client_golang, go.opentelemetry.io/otel*.

### Wiring join — DONE (merged to mainline @ 74f551c)
`cmd/engine` + `internal/httpapi` now wire the REAL packages: config.Store, toggleable
AuthN→AuthZ chain (bypassed when unconfigured), OTel provider + logger + metrics, the full flow
node registry, and the admin
endpoints `POST /admin/flows/validate` (publish-blocking, always 200) + `POST /admin/flows/dry-run`
(writes suppressed via observ.WithDryRun, honored in flow.actionHandler). LIFO graceful shutdown.
Defect fixed in-scope during integration: `flow.ValidateTree` was reclassifying linear action/set
nodes (which legitimately carry a "next" child in the interpreter's model) as invalid leaves —
reconciled so the validator matches the interpreter; response stays strictly terminal; all other
rules intact. Full gate green incl. integration tests for auth (401/200/403), admin-validate
(seed ok:true / broken ok:false), and dry-run (write suppressed, zero external POSTs).

Known in-spirit limitations (documented, non-blocking): dry-run of the seed reports a Validation
in its errors array because the seed's Set consumes the suppressed write's return body (expected
consequence of suppression; trace + no-write assertions hold); the dry-run trace records only the
suppressed write node — full per-node TraceNode integration in internal/flow is a later increment.

CORRECTION (config-store run mode increment): the paragraph above previously claimed the Wiring
join made `cmd/engine` select a Postgres PgStore vs the in-memory seed behind `CONFIG_DSN`. That
was overstated — `cmd/engine` at that point only ran `config.LoadSeed(seedPath)` into a memStore;
no DSN, migrations, or cache were wired into the runnable binary. The PgStore/ValkeyCache were
built + unit/integration-tested but NOT reachable from the binary. The config-store run mode is
actually wired by the increment below.

### Config-store run mode — DONE
`cmd/engine` now selects its run mode after loading an optional `.env`:
- **CONFIG_DSN set → config-store mode.** Opens a pgxpool pinned to a dedicated Postgres schema
  (`CONFIG_SCHEMA`, default **`rule_engine`**, validated as a safe identifier) via a pool
  `AfterConnect` running `SET search_path TO <schema>`, runs `CREATE SCHEMA IF NOT EXISTS` then
  applies `migrations.Apply` so every (unqualified) `CREATE TABLE` lands in `<schema>.*` and
  `public` is left untouched. Builds a `ValkeyCache` + invalidation consumer when `VALKEY_ADDR` is
  set (else runs cache-down), a `PgStore` over `{"": pool}`, and seeds `seed.json` INTO Postgres
  idempotently THROUGH the store (new `PgStore.PutJDMVersion` / `PutConnectionVersion` write
  methods + `PutFlowVersion`→`MarkValidated`→`SetActive`; connection versions store `secret_ref`
  only, never a secret value). `migrations.Apply` is gated on a `flows`-table existence check so a
  second process start against an already-migrated schema does not re-run the bare DDL. The
  connected host/db/schema is logged with the password REDACTED. LIFO cleanup closes cache
  subscriber → valkey client → pool.
- **CONFIG_DSN unset → in-memory mode.** The EXACT original `config.LoadSeed(seedPath)` path; no
  database is touched.
- New `internal/envfile` loader (dependency-free `KEY=VALUE`, process env wins, missing file is
  fine). New `.env.example` documents `CONFIG_DSN / CONFIG_SCHEMA / VALKEY_ADDR / ENGINE_ADDR /
  ORDERS_PG_DSN / SHIP_REST_BASE_URL / RUN_REST_STUB / REST_STUB_PORT`.
- New build-tagged integration test `internal/config/configrun_integration_test.go` (ephemeral
  postgres:16 ×2 + valkey, NEVER an external DB) asserts: (a) config tables in `rule_engine`, not
  `public`; (b) seed flow/JDM/connections written + flow active; (c) a second seed is idempotent
  (no error, no duplicate version); (d) `GET /orders/{id}` served end-to-end reading config from
  the PgStore. Full gate green: build, vet, unit (incl. envfile + schema validator), the
  CGO_ENABLED=0 decision-stub build, and the config + httpapi integration suites.

### Config-management admin API — DONE (reviewed APPROVED, merged @ `df547be`)
The privileged `/admin/*` control plane is BUILT — a thin HTTP + operator-auth + request-shape layer
over the EXISTING config-write store methods (no store method reimplemented; pure flow/config cores
untouched). stdlib `net/http` only (Go 1.22 method-aware patterns, longest-pattern precedence over
the public `"/"` catch-all, same as the ops endpoints). Full build/increment report:
`docs/.increments/admin-api-report.md`. As-built contract (for Strapi integration):
`docs/lld/slice-f-admin-api.md` (reconciled to AS-BUILT).

Endpoint surface (each operator-guarded, deny-by-default):
- `POST /admin/flows` → `PutFlowVersion` (store assigns version; structural+ref validate first; bad
  tree ⇒ 400). `POST /admin/flows/{id}/publish` & `/rollback` → `SetActive` (route-derived action).
- `POST /admin/flows/validate` → stored mode (`flowId`+`version`: load via `GetFlowVersion`, validate,
  `MarkValidated` on all-pass) + candidate mode (inline `flow`, stateless); always 200 with `{ok,
  structural[], fixtures[]}`.
- `POST /admin/flows/dry-run` → writes suppressed via `observ.WithDryRun`; returns trace + response.
- `POST /admin/jdms` → `PutJDMVersion`. `POST`/`GET /admin/connections` → `PutConnectionVersion` /
  `Connections` (secret_ref ONLY; inline secret ⇒ 400; list redacted). `GET /admin/audit/{type}/{id}`
  → `AuditTrail`.

Status-code contract (by `errors.Is`, priority-ordered — the two specific sentinels before the
generic one): 409 route `(method,path)` collision (`config.ErrRouteConflict`, SQLSTATE 23505 on
`flows_method_path_key`); 422 publish of an un-validated version (`config.ErrUnvalidated`); 404
NotFound; 400 request-shape / bad tree / secret-value / generic Validation; 504 Timeout; 502
Upstream; 500 Internal / nil-store. 201 on create; 200 on publish/rollback/list/audit and
validate/dry-run.

Operator auth (`internal/auth/operator.go`) — a SEPARATE trust domain from the public JWT, NOT a
role on the JWKS token: `OperatorAuthenticator`/`Operator`/`OperatorGuard` + `StaticTokenOperatorAuth`
(sha256 allow-list, constant-time match, fail-fast on malformed/empty/duplicate config). Deny-by-
default: nil authenticator ⇒ 503 mount-closed, bad credential ⇒ 401, missing role ⇒ 403. RBAC per
route (`requireRole`): write ⇒ `flow.write`, publish/rollback ⇒ `flow.publish`, validate/dry-run/GET
⇒ `flow.read`. Per-operator audit attribution via `config.WithAuditActor(ctx, subject)` (no signature
change). `cmd/engine` reads `ADMIN_ENABLED`/`ADMIN_TOKENS` (fatal on bad/empty) and passes one
`*config.PgStore` as both the hot-path `store` and `deps.Admin`; `auth.bearerToken` exported as
`auth.BearerToken`.

Both live-run bugs fixed in the same increment (see "TWO LIVE BUGS — BOTH FIXED" above). Store seam
additions (all additive, no frozen-seam signature change): `GetFlowVersion`,
`FlowExists`/`JDMExists`/`ConnectionExists`, SQLSTATE-23505 route-conflict classify, `WithAuditActor`.
Dry-run write-suppression wired into `flow.actionHandler` (`observ.IsDryRun`+`CollectorFrom`); dead
`flow/dryrun.go` shim removed. `.env.example` documents `ADMIN_ENABLED`/`ADMIN_TOKENS`.

Verification (ACTUALLY RUN from the worktree root, all GREEN; evidence in
`docs/.agents/tasks/admin-api/verification.md`): `CGO_ENABLED=1 go build/vet/test ./...`; integration
`-tags 'integration cgo'` for `./internal/httpapi` and `./internal/config` on ephemeral postgres:16
(+ valkey); `CGO_ENABLED=0 go build ./internal/decision` (stub). Documented limitations: in-memory
mode has no admin writes; single-env (`""`); dry-run reads can hit live sources when `mocks` omitted;
dry-run trace is suppressed-write + response, not a full per-node walk; validate resolves refs
against the active set; seed is bootstrap-only; one cosmetic stale `Deps.Store` doc comment.

### Strapi CMS (the authoring UI — the original ask) — DONE (reviewed APPROVED, merged)
An isolated **Strapi 5** app under `cms/` now authors engine config with visual editors and
publishes it to the engine. Full build/increment report: `docs/.increments/strapi-cms-report.md`.
Key facts:
- **Integrates ONLY via the admin HTTP API.** Every engine interaction routes through a typed
  native-fetch `AdminClient` over the FROZEN `/admin/*` contract (`docs/lld/slice-f-admin-api.md`):
  FLAT bodies with `env` as a top-level sibling (default `""`, no `flow`/`connection` wrapper to
  satisfy `DisallowUnknownFields`), `Authorization: Bearer <token>` on EVERY call (token resolved at
  call time from `ADMIN_API_OPERATOR_TOKEN` or the per-Environment `operatorTokenRef`, never
  persisted/logged), base URL via `ADMIN_API_BASE_URL` / per-Environment `adminApiBaseUrl`. The CMS
  NEVER reads or writes the engine's Postgres config store directly.
- **Own, isolated Postgres db + schema.** `cms/config/database.ts` targets the CMS's OWN database
  (`CMS_DB_NAME`, default `strapi_cms`) and OWN schema (`CMS_DB_SCHEMA`, default `strapi_cms`) via
  `CMS_DB_*` env vars — explicitly forbidden from ever using `public` or the engine's `rule_engine`.
  Physically separate from the engine store.
- **Four content types:** Flow (flowId/method/path/`tree` on the reactflow canvas custom field/
  fixtures/env/bookkeeping, draft&publish), Jdm (jdmId/`doc` on the GoRules jdm-editor custom field,
  draft&publish), Connection (key/type/settings dynamic zone/`secretRef` only — NEVER a secret value
  or DSN/resilience, draft&publish), Environment (name/adminApiBaseUrl/operatorTokenRef/payloadEnv,
  no draft&publish).
- **Two visual editors as custom fields** (both base `type:'json'`): a **reactflow** drag-and-drop
  flow canvas and the **`@gorules/jdm-editor`** ZEN editor, plus a ValidationPanel and a raw-JSON
  fallback. The flow canvas and the server transform share the single `serialize.ts` module (no
  duplicate serializer); `spec` passes through verbatim so engine casing survives.
- **Ordered, publish-blocking pipeline** (shared by the controller and the Draft&Publish
  `beforePublish` lifecycle): listConnections → createConnection(only changed) → createJdm →
  createFlow → validateFlow(STORED) → publishFlow. Three gates abort before publish: `ok:false`, an
  unreachable validator (5xx/transport; a 404 also blocks), and a detected inline secret (aborts
  before ANY admin HTTP call via the recursive denylist). HTTP status mapping per §5.5.
- **Isolation holds:** the diff touches only `cms/` + `docs/`; `go.mod`/`go.sum`/`internal`/`cmd` are
  byte-identical to mainline. Deps are exact-pinned (Strapi 5.56.0, jdm-editor 1.52.0, reactflow
  11.11.4, pg 8.13.1, …).
- **Verification (re-run at finalize, all GREEN):** `cms/` `npm ci` / `npm run build` / `npm run
  test` (vitest + nock) exit 0; `CGO_ENABLED=1 go build ./...` exit 0; `git status --porcelain --
  go.mod go.sum internal cmd` empty.
- **Limitations (documented, non-blocking):** audit-viewer UI deferred (client has `audit()`, no UI);
  `@xyflow/react` v12 migration deferred (on reactflow 11); one cosmetic dead `.writeBack` read in
  the controller catch (no gate/bookkeeping impact).

### v1 ENGINE COMPLETE (AC-1..26). Remaining v1 documented constraints unchanged:
non-atomic cross-source writes (R3), no rate limiting (R6), per-instance breakers (R10).
`Interpreter.Run` seam deviation `(ctx, tree, ver, c, dep)` (import-cycle break) still stands.

## NEXT — v1 engine + admin control plane + Strapi CMS are done; what remains is the broader system (see arc below)
The admin API is BUILT (see "Config-management admin API — DONE"), the Strapi CMS is BUILT (see
"Strapi CMS — DONE"), and both live-run bugs are fixed. Pick the next milestone (product-priority
call):
- **Productionize** the engine + CMS (deploy; wire the CMS's own Postgres + the admin-API bearer;
  config written hand-seeded / via admin API / via Strapi), OR
- Pull a **deferred engine item** forward if a real need exists (collection filter/find nodes;
  JSON-source rule-match — both specced as next-phase below; full per-node dry-run trace;
  idempotency/rate-limit/saga), OR
- Pull a **deferred CMS item** forward if needed (audit-viewer UI; `@xyflow/react` v12 migration).

## AFTER v1 engine — remaining project arc

- **Strapi control-plane module (the CMS)** — DONE (see "Strapi CMS — DONE" above): separate Strapi 5
  build under `cms/`, four content types, `@gorules/jdm-editor` + reactflow canvas, validation +
  draft/publish pipeline, publish transform pushed to the engine via the `/admin/*` HTTP API (NOT
  the config store directly), on its own isolated Postgres db + schema.
- **Deferred engine items as needed**: idempotency for required writes (R4), rate limiting (R6),
  cross-source saga/compensation (R3).
- **De-demo-ify `internal/httpapi/adapters.go` (platform-genericness, from a business-noun audit).**
  Two demo-tuned heuristics live in the httpapi edge and should become CONFIG-DECLARED, not
  assumed from the orders demo's data shape (per `concepts/config-driven-boundaries`): (1)
  `normalizeResult` unwraps a single-row Postgres result to a map "because orders/{id} yields one
  row" — a shape assumption; the action node should declare one-row-vs-many instead of the engine
  guessing. (2) `soleStringValue`/`branchingEvaluator` treats a single-string decision output as
  "the branch" regardless of field name — tuned to the order JDM's `{"shipping":"<branch>"}`; the
  condition node should declare which decision output field is the branch. The core engine
  (`internal/flow`, `connect`, `decision`, `config`, `observ`) is clean of business logic — these
  two are the only demo-ties left after the config-driven-routing fix removes the hardcoded route.
  (The in-flight config-driven-routing increment may address these opportunistically; if not,
  they're a small follow-up increment.)
- **Collection logic over result sets (NEXT PHASE — not MVP).** The engine can return multi-row
  arrays, index into them, and iterate (`forEach`) with per-row ZEN decisions. It does NOT yet
  have first-class **`filter` / `find` / `map` / `reduce`/aggregate** nodes to select or compute a
  subset *across* rows in-engine. MVP guidance: push filtering/finding/aggregation down into the
  SQL query (that works today). Build in-engine collection nodes only when the data comes from a
  non-queryable source or must be combined across sources. Needs: collection node types with a
  ZEN predicate per element + a `Set`/append mode that builds arrays (current no-clobber `Set`
  can't build collections well). Spec into HLD/LLD with its own ACs before building.
- **JSON-file data source + rule-match (NEXT PHASE — not MVP).** Two shapes: (A) if the reference
  data is rule-shaped (conditions→outcome), author it as a **ZEN decision table** — works TODAY,
  no new code (this is the idiomatic answer to "config data + find exact match by rules"). (B) if
  it's a separate JSON *dataset* loaded then searched, needs a new **`json`/`file` connector type**
  + the collection `find`/`filter` nodes above. Prefer (A) wherever the match is rule-like; build
  (B) as a designed increment only if a genuine load-and-search-arbitrary-dataset need exists.
- **Productionize**: glibc-based container image (ADR-003/R9), the 3 per-env config stores +
  valkeys, secrets backend (Vault/SSM), CI/CD, multi-env wiring, load testing (esp. R10 breakers).

Merge-contention rule for any future parallel runs: each worktree edits only its own package;
only the final join step touches `go.mod` / `cmd/engine`.

## How to resume in a fresh session (paste this)
> Continue the nzr-rules-engine build. Read docs/STATE.md on the mainline branch of
> /home/nuzirwan/project/rule-engine-api for full context, then <pick an increment from "NEXT">.

## Workflow notes
- Substantial builds go through workflows (worktree-isolated, coder+semantic_reviewer gate,
  rebase+ff-merge onto mainline). The orchestrator removes worktrees after.
- Learnings are curated in the engineering-standards MCP wiki (ask before saving).
