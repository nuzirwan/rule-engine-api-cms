# Verification — Slice F: Admin API (control plane) + two live-bug fixes

Iteration: FIRST (no `review.json` present — implemented full scope from scratch).
Worktree: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api`, branch `feat/admin-api`.

All commands were ACTUALLY RUN from the worktree root and were GREEN before this
was written. Integration suites used EPHEMERAL `postgres:16` (+ valkey) containers
only — no external DB was ever touched.

## Verification gate — commands run and results

| Command | Result |
| --- | --- |
| `CGO_ENABLED=1 go build ./...` | PASS (no output) |
| `CGO_ENABLED=1 go vet ./...` | PASS (no output) |
| `CGO_ENABLED=1 go test ./...` | PASS — all packages ok (auth, config, connect, decision, envfile, flow, httpapi, observ) |
| `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` | PASS — `ok nzr-rules-engine/internal/httpapi 55.233s` (ephemeral postgres:16) |
| `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/config` | PASS — `ok nzr-rules-engine/internal/config 104.508s` (ephemeral postgres:16 + valkey) |
| `CGO_ENABLED=0 go build ./internal/decision` | PASS — the !cgo decision stub still compiles |

Additional targeted runs:
- `CGO_ENABLED=1 go test -race ./internal/flow` — PASS (dry-run suppression, no data race).
- `CGO_ENABLED=1 go test -tags 'integration cgo' -run TestSeedPerObjectIdempotency ./internal/config` — PASS (14.2s).
- `CGO_ENABLED=1 go test -tags 'integration cgo' -run TestAdminDryRunSuppressesWrite ./internal/httpapi` — PASS (4.5s).

## What each verification proves

### Unit (no DB)
- `internal/httpapi/ops_test.go` — `/readyz` gates on the config-store Ping ONLY;
  a healthy store + a FAILING data-source registry returns 200 (the fix); a
  failing store Ping is 503 `config_store`; a nil store (in-memory) is 200.
- `internal/flow/handlers_dryrun_test.go` — a write op (`exec`/`set`/`del`/`http`)
  under `observ.WithDryRun` performs NO `Execute` and records `wrote:"suppressed"`;
  a read op (`query`/`get`) is NOT suppressed; without dry-run the real write runs.
- `internal/auth/operator_test.go` — the operator guard is deny-by-default
  (nil authn => 503, bad credential => 401, wrong role => 403, valid => next with
  the Operator in ctx) and logs nothing sensitive; `NewStaticTokenOperatorAuth`
  rejects malformed/empty/duplicate config and matches constant-time.
- `internal/httpapi/admin_test.go` — each admin request→method→status mapping, the
  secret-value rejection (400, top-level + nested `settings.password`), the full
  `statusForAdmin` map (409/422/404/400/504/502/500), route precedence
  (`/admin/flows/validate` resolves to validate, not `{id}/publish`), GET vs POST
  dispatch, `{type}/{id}` capture, disabled-plane (503) and nil-Admin (500).

### Integration (ephemeral postgres:16 (+ valkey) ONLY)
- `internal/config/seed_pg_integration_test.go` — per-object idempotent seeding:
  seed #1 writes `alpha`; seed #2 adds `beta` and writes ONLY `beta` while `alpha`
  stays byte-identical (same version, same checksum) and no duplicate
  `create_version` audit row; seed #3 (no change) is a pure no-op
  (`seeded==false`, zero new audit rows). This is the exact live bug.
- `internal/config/configrun_integration_test.go` (existing) — still green with the
  rewritten `SeedPgStore` (first seed `seeded==true`, second seed `seeded==false`,
  no duplicate versions).
- `internal/httpapi/admin_integration_test.go`:
  - create → validate (stored) → publish → audit round-trip over `/admin/*` with a
    valid operator token; publish BEFORE validate => 422; the audit actor is the
    operator subject `op:tester` (per-request actor attribution).
  - a secret-value connection body => 400; `GET /admin/connections` shows
    `secretRef` only (no secret value).
  - a disabled operator plane (nil `OperAuth`) => 503 on `/admin/*`.
  - a dry-run against a REAL stored flow whose action is a WRITE op suppresses the
    write (zero rows in the sink table) and returns a trace containing the
    suppressed-write record.
  - `/readyz == 200` with a healthy config store even though a data-source
    connection points at a dead address.

## Scope implemented (plan items 1–14)

1. `/readyz` false-negative fixed (gate on `store.Ping` only; dropped the
   data-source `registry.HealthCheck` gate). `internal/httpapi/ops.go` (+test).
2. Store-internal existence helpers `FlowExists`/`JDMExists`/`ConnectionExists`.
   `internal/config/pgstore_admin.go`.
3. Store-internal `GetFlowVersion(ctx,env,flowID,version)`. Same file.
4. Route-conflict classify tweak: SQLSTATE `23505` on `flows_method_path_key` =>
   typed `Validation`/`ErrRouteConflict` (admin maps to 409, not 502).
   `internal/config/{errors.go,pgstore.go}`.
5. Actor-on-context seam `config.WithAuditActor`; store audit inserts use the
   ctx-carried operator actor else the construction actor.
   `internal/config/{actor.go,pgstore.go}`.
6. `SeedPgStore` rewritten to per-object create-if-absent (bootstrap-only).
   `internal/config/seed_pg.go` (+integration test, item 6a).
7. Dry-run write-suppression wired into `flow.actionHandler` via
   `observ.IsDryRun`/`CollectorFrom`; dead `flow/dryrun.go` shim deleted (kept
   only `isWriteOp`). `internal/flow/{handlers.go,dryrun.go}` (+test, item 7a).
8. Operator-auth mechanism: `OperatorAuthenticator`/`Operator`/`OperatorGuard` +
   `StaticTokenOperatorAuth` (sha256 allow-list, constant-time, fail-fast config),
   deny-by-default. `internal/auth/operator.go` (+test, item 8b).
8a. `auth.bearerToken` exported as `auth.BearerToken`. `internal/auth/middleware.go`.
9. `Deps.Admin` + `Deps.OperAuth`; `NewHandler` builds + mounts the admin surface
   before the catch-all. `internal/httpapi/server.go`.
10. Admin HTTP handlers (create/publish/rollback/jdm/connections POST+GET/audit) +
    `statusForAdmin`. `internal/httpapi/{admin.go,admin_handlers.go,admin_types.go}`.
11. `POST /admin/flows/validate` (stored + candidate modes, publish-blocking,
    always-200-on-result) + `AdminFixture` + `storeRefs`.
    `internal/httpapi/admin_validate.go`.
12. `POST /admin/flows/dry-run` (writes suppressed, trace + response).
    `internal/httpapi/admin_dryrun.go`.
13. Composition root: `ADMIN_ENABLED`/`ADMIN_TOKENS` wiring (fatal on
    malformed/empty), same `*config.PgStore` passed as both `store` and
    `deps.Admin`. `cmd/engine/main.go` + `.env.example`.
14. Admin + fixes integration coverage. `internal/httpapi/admin_integration_test.go`.

## Status-code precision (for the downstream Strapi CMS)
- 422 for publish-blocking validation failures (`config.ErrUnvalidated`).
- 409 for version/route-unique collisions (`classifyPg` SQLSTATE `23505` on
  `flows_method_path_key` => `config.ErrRouteConflict`).
- 400 request-shape / secret-value; 404 NotFound; 504 Timeout; 502 Upstream;
  500 Internal / nil-Admin.

## Constraints honoured
- stdlib `net/http` only (Go 1.22 method-aware patterns; no chi/gin).
- Connection writes accept `secret_ref` only; inline secret values rejected 400;
  the connection-list response redacts the free-form settings map.
- CGO_ENABLED=1 build/vet/test green; the CGO_ENABLED=0 decision stub still compiles.
- Integration tests used ephemeral containers only; no external DB touched.

## MCP tooling notes
- `codegraph` (`.codegraph/` at the repo root) was used to survey the httpapi
  server/ops/interpreter symbols before editing.
- `engineering-standards`: `detect_project` => `{go, rest-api}` (no `go` language
  page; `architecture-rest-api-service` + universal concepts available). Applied +
  cited the plan's standards (`[[security-and-authz]]` deny-by-default,
  `[[config-and-secrets]]` secret_ref-only, `[[error-classification]]`,
  `[[config-driven-boundaries]]` for the readyz gate).
