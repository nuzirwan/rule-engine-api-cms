# nzr-rules-engine — project state & handoff

Single source of truth for picking up work in a fresh session. Everything below is committed on
the `mainline` branch. Read this first, then the docs it points to.

Last updated: 2026-10-03 · mainline HEAD at handoff: `4fd28a2`

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

## Build / test commands
```
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

### v1 ENGINE COMPLETE (AC-1..26). Remaining v1 documented constraints unchanged:
non-atomic cross-source writes (R3), no rate limiting (R6), per-instance breakers (R10).
`Interpreter.Run` seam deviation `(ctx, tree, ver, c, dep)` (import-cycle break) still stands.

## NEXT — the v1 ENGINE is done; what remains is the broader system (see arc below)
Pick the next milestone (product-priority call):
- Build the **Strapi CMS** (the authoring UI — the original ask), OR
- **Productionize** the engine first (deploy with hand-seeded/Strapi-written config), OR
- Pull a **deferred engine item** forward if a real need exists (collection filter/find nodes;
  JSON-source rule-match — both specced as next-phase below; full per-node dry-run trace;
  idempotency/rate-limit/saga).

## AFTER v1 engine — remaining project arc
- **Strapi control-plane module (the CMS)** — separate Node/React build: content types,
  `@gorules/jdm-editor` + React Flow drag-and-drop canvas, validation hooks, draft/publish,
  promotion, the publish transform into the engine config store. (This is the original ask.)
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
