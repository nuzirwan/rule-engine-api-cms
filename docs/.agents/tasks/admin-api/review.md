# Config-management Admin API — final-gate semantic review

Slice F turns the already-built config-write store methods into a privileged `/admin/*` HTTP
control plane, adds a separate deny-by-default operator-auth mechanism, wires the author tools
(validate + dry-run), and ships two live-run bug fixes (`/readyz` false-negative, all-or-nothing
seeding). The implementation is a thin HTTP + request-shape layer over the frozen `config.Store`
seam — it reimplements no store method, keeps the pure flow/config cores intact, and matches the
APPROVED design (`slice-f-admin-api.md`), the plan (`plan.md`, items 1–14), and the frozen contracts
(`lld-contracts.md`). The verification evidence (`verification.md` + commit `df547be`) records the
full gate GREEN on ephemeral containers only.

Watch for: nothing blocking. One stale struct-doc comment on `httpapi.Deps.Store` (says `/readyz`
gates on `Conns.HealthCheck AND Store.Ping`; the code correctly gates on `Store.Ping` only) —
confirmed, cosmetic. One documented-and-accepted tradeoff: a `flow.read` operator running dry-run
without `mocks` triggers real read I/O against live sources — confirmed, by design (§2.7).

**Verdict**: APPROVED

## High-level view

The two live-bug fixes are correct and behaviorally proven. `/readyz` now gates on the config-store
`Ping` only (the data-source `registry.HealthCheck` gate is gone), so a healthy engine against a
reachable config store stays in rotation even when a downstream data source is dead; the unit test
pins the 200-when-datasource-down case. `SeedPgStore` is rewritten to per-object create-if-absent
using new `FlowExists`/`JDMExists`/`ConnectionExists` PK reads, so adding one flow to the seed and
restarting writes only that flow and leaves existing versions byte-identical; the integration test
pins the exact bug plus the re-seed-noop.

Dry-run write-suppression is genuinely wired, not shimmed. `flow.actionHandler.Exec` consults
`observ.IsDryRun(ctx)` and, for a write op (`exec/set/del/http`), records `wrote:"suppressed"` into
the context-carried collector and skips `client.Execute` — the correct observ seam, not the dead
`dryRunKey{}` shim, which is deleted. Reads (`query/get/ping`) are not suppressed. The admin
dry-run handler attaches both `WithDryRun` and `WithCollector`, so the suppressed-write record
reaches the response trace.

Operator auth is a distinct, deny-by-default mechanism independent of the public JWT path. The guard
fails closed: nil authenticator ⇒ 503 mount-closed, bad credential ⇒ 401, missing role ⇒ 403,
otherwise injects the `Operator` into context. `StaticTokenOperatorAuth` stores only SHA-256 hashes,
matches constant-time over the whole allow-list (no timing oracle and no early-exit leak), and its
constructor is fail-fast on malformed/empty/duplicate config. `cmd/engine` treats a bad/empty
`ADMIN_TOKENS` under `ADMIN_ENABLED=true` as a fatal boot error and passes a nil authenticator when
the plane is disabled.

Status codes are precise for the Strapi integration: a `(method,path)` collision is classified at
the store (SQLSTATE 23505 on `flows_method_path_key`) as a typed `Validation` carrying
`ErrRouteConflict`, which the admin edge maps to 409; a publish of an un-validated version carries
`ErrUnvalidated` and maps to 422. These are distinct sentinels matched by `errors.Is`, never string
text, so 409 (collision) and 422 (publish-blocking) never alias each other or a generic 400.

Secrets are `secret_ref`-only: the connection create handler rejects any top-level secret-value
field and any `settings.*` secret-value key with 400 before the store is touched, and the
connection-list response runs `settings` through the central Redactor as a backstop while preserving
the non-secret `secretRef` pointer.

Per-operator audit attribution rides the context (`config.WithAuditActor`): every `audit_log` and
`created_by` insert in `PutFlowVersion`/`SetActive`/`PutJDMVersion`/`PutConnectionVersion` now reads
`s.actorFor(ctx)`, which prefers the ctx operator subject and falls back to the construction-time
actor — no method-signature change, consistent with the env/dry-run-on-context conventions.

<details>
<summary>Issues (2)</summary>

1. **Stale Deps.Store doc comment** — the `httpapi.Deps.Store` field comment claims `/readyz` gates
   on `Conns.HealthCheck AND Store.Ping`; the implemented `ops.go` gates on `Store.Ping` only. Code
   is correct; update the comment on a later touch. Non-blocking.
2. **dry-run reads hit live sources under flow.read** — with `mocks` omitted, `/admin/flows/dry-run`
   (gated on `flow.read`) performs real read I/O against the wired data sources. Explicitly accepted
   in design §2.7 (faithful preview; writes always suppressed). Non-blocking; noted for awareness.

</details>

<details>
<summary>Details</summary>

### `/readyz` gate narrowed to the config store (bug fix #1)

`Ops` now holds only a `pinger` (the config store), a gatherer, and a logger — the `connect.Registry`
field and its `HealthCheck` gate are gone. `readyz` runs `store.Ping` under the 2s `readyzTimeout`
and returns `503 {"notReady":"config_store"}` on failure, `200 {"status":"ready"}` otherwise; a nil
store (in-memory mode) skips the gate and returns 200. This is the designed criterion (§5.2): the
engine's serve-dependency is its config store, not every downstream data source. The unit test
proves the whole point — healthy `store.Ping` + a failing data-source registry ⇒ 200.

The `Deps.Store` struct comment is stale here (it still describes the old `Conns.HealthCheck AND
Store.Ping` gate). The behavior is correct; only the comment lags.

### Per-object idempotent seeding (bug fix #2)

`SeedPgStore` replaces the all-or-nothing top-level gate with per-object existence checks:
`FlowExists`/`JDMExists`/`ConnectionExists` (single indexed PK reads, `pgx.ErrNoRows ⇒ (false,nil)`,
any real driver error surfaced via `classifyPg` as `Upstream`). Each absent object is written; each
present object is skipped and never version-churned (bootstrap-only, D3). A real existence-probe
error aborts the seed rather than silently treating a transient outage as "absent" and double-writing
— the right failure mode. `seeded` is the OR of any write. The integration test proves seed-#1 writes
`alpha`, seed-#2 adds only `beta` with `alpha` byte-identical (same version + checksum, no duplicate
audit row), and seed-#3 is a pure no-op.

### Dry-run write-suppression on the observ seam (AC-14)

`actionHandler.Exec` gained exactly the designed branch before `client.Execute`:

```go
if observ.IsDryRun(ctx) && isWriteOp(op.Kind) {
    if tc, ok := observ.CollectorFrom(ctx); ok {
        tc.Record(n.ID, string(n.Type), "", map[string]any{"wrote": "suppressed"})
    }
    return walkChildren(ctx, n.Children, c, dep, w)
}
```

`dryrun.go` is reduced to just `isWriteOp` (reads = `query/get/ping`; everything else a write) — the
dead `dryRunKey`/`withDryRun`/`isDryRun` shim keyed on a package-local type is deleted, so there is
no longer a second, unreachable dry-run flag competing with the live `observ` one. `observ` does not
import `flow`, so importing `observ` here introduces no cycle. The admin dry-run handler builds
`observ.WithCollector(observ.WithDryRun(ctx), collector)` and reads `collector.Result(...).Steps`
into the response `trace`, so the suppressed-write record is the AC-14 evidence the author sees.

### Operator auth: separate, deny-by-default, no credential leak

`OperatorGuard.Protect` implements the four-step fail-closed chain exactly: nil `authn` ⇒ 503
(mount-closed, absence-is-denial), authn error ⇒ 401, `require(r)` role not held ⇒ 403 (security
event logged with subject/resource/action), else inject `Operator` into ctx and call next. Audit
lines carry only `error_class`/subject/resource/action — never the token, hash, or Authorization
header. `StaticTokenOperatorAuth`:

- Parses `sha256hex:subject:comma,roles` splitting on the FIRST and LAST colon, so a colon-bearing
  subject like `op:alice` is unambiguous — a nice correctness detail.
- Constructor is fail-fast: a non-three-part entry, a non-hex / wrong-length hash, an empty subject,
  a duplicate subject, or zero valid tokens (`ErrNoOperatorTokens`) all return an error. An
  enabled-but-empty allow-list never boots.
- `AuthenticateOperator` hashes the presented bearer and scans the WHOLE allow-list with
  `subtle.ConstantTimeCompare`, taking the last match — the match time does not depend on which (or
  whether an) entry matched. No timing oracle.

`cmd/engine.buildOperatorAuth` returns `(nil, nil)` when `ADMIN_ENABLED != true` (plane mount-closed)
and surfaces a constructor error as a fatal boot error otherwise. The RBAC `require` map lives in the
composition layer (`requireRole`), so `auth` carries no routing knowledge.

### Status-code precision: 409 vs 422 vs 400

`classifyPg` now detects `pgconn.PgError.Code == "23505"` on constraint `flows_method_path_key` and
wraps it as `Validation` + `ErrRouteConflict`; everything else from the driver stays `Upstream`.
`SetActive`'s un-validated-publish path wraps `Validation` + `ErrUnvalidated`. `statusForAdmin`
dispatches by `errors.Is` in priority order: `ErrRouteConflict ⇒ 409`, `ErrUnvalidated ⇒ 422`,
`ErrNotFound ⇒ 404`, `ErrValidation ⇒ 400`, `ErrTimeout ⇒ 504`, `ErrUpstream ⇒ 502`, else 500. The
two specific `Validation` sentinels are matched before the generic `ErrValidation`, so a route
collision never falls through to 400 and a publish-block never falls through to 400 — the exact
contract the Strapi CMS integrates against.

### Admin surface: thin layer, correct mapping, secret_ref only

`AdminStore` is a narrow DIP interface satisfied structurally by `*config.PgStore` (compile-time
asserted), so the handlers test against a fake and `cmd/engine` passes one concrete store as both the
hot-path `store` and `deps.Admin`. Handlers decode strictly (`DisallowUnknownFields`), enforce the
single-env rule (absent/`"" ` ok; any non-empty ⇒ 400), validate request shape, call exactly one
store method, and map the classified result. `createFlow` runs `flow.ValidateTree` before touching
Postgres (bad tree ⇒ 400 with issues). `publish`/`rollback` derive `action` from the route (SetActive
returns only error) as designed. `createConnection` rejects secret-value fields via a
`rejectIfPresent` JSON type plus a nested `settings` scan ⇒ 400; `listConnections` scrubs `settings`
through the Redactor while keeping `secretRef`. Audit actor is stamped via
`config.WithAuditActor(ctx, operator.Subject)` before each write.

### Validate: stored vs candidate modes, publish-blocking, always-200

Mode discriminator matches §2.6 exactly: `flowId` + positive `version` ⇒ stored (load via
`GetFlowVersion`, 404 on no match, `MarkValidated` on all-pass); else inline `flow` ⇒ candidate
(stateless, never `MarkValidated`); neither ⇒ 400. Structural validation uses the real
`flow.ValidateTree(root, refs)` with a `storeRefs` adapter (`HasConnection` via `Connections`
membership; `HasJDM` treating NotFound as absent and any other error as present, per §4.1). Fixture
replay runs the interpreter over a mock `connect.Registry` with a trace collector and deep-equals
`Expect.Output` vs `Ctx.Response`. The response is always 200 with `{ok, structural[], fixtures[]}`;
only a malformed request or stored-mode 404 is non-200. `GetFlowVersion` is a dedicated PK read of
`flow_versions` + fixtures (NotFound when absent), not an overload of the hot-path `ActiveFlow`.

### Wiring and seam fidelity

The two additive `Deps` fields (`Admin`, `OperAuth`) and the `admin.mount(mux)` call before the `"/"`
catch-all are the only `httpapi` surface changes; `NewHandler`'s signature is unchanged. The admin
routes use Go 1.22 method-aware patterns, so `/admin/flows/validate` resolves to the literal pattern
over `{id}/publish` by ServeMux specificity. The `AdminFixture` wire type stays out of the frozen
`config.FlowFixture` (down-mapped on persist, up-mapped on stored-read), so the persisted contract is
unchanged. All referenced symbols (`flow.NewCtx`, `flow.Version`, `flow.ValidateTree`,
`observ.NewTraceCollector`/`Result`/`Steps`, `observ.NewRedactor`/`Scrub`, `config.AuditEntry`
`FromVersion`/`ToVersion`) exist with matching signatures.

### Verification gate (read from evidence, not re-run)

`verification.md` records every gate command GREEN: `go build ./...`, `go vet ./...`,
`go test ./...` (all packages), `go test -tags 'integration cgo' ./internal/httpapi` (ephemeral
postgres:16), `go test -tags 'integration cgo' ./internal/config` (ephemeral postgres:16 + valkey),
and `CGO_ENABLED=0 go build ./internal/decision` (stub compiles). Targeted runs include
`-race ./internal/flow` and the two named integration tests. The evidence is specific (durations,
package names, the exact assertions each test proves) and the integration suites are documented as
ephemeral-container-only. No articulable doubt remained that would warrant a spot-check re-run.

</details>

<details>
<summary>File map</summary>

- `internal/httpapi/ops.go` — `/readyz` gates on `Store.Ping` only; registry health gate removed.
- `internal/httpapi/ops_test.go` — proves 200 when data source down, 503 on store ping fail, 200 nil store.
- `internal/config/seed_pg.go` — per-object create-if-absent seeding.
- `internal/config/pgstore_admin.go` — `FlowExists`/`JDMExists`/`ConnectionExists`, `GetFlowVersion`.
- `internal/config/pgstore.go` — audit inserts use `actorFor(ctx)`; `classifyPg` 23505 route-conflict.
- `internal/config/errors.go` — `ErrUnvalidated`, `ErrRouteConflict` sentinels.
- `internal/config/actor.go` — `WithAuditActor` / `actorFor` actor-on-context seam.
- `internal/config/seed_pg_integration_test.go` — per-object idempotency integration proof.
- `internal/flow/handlers.go` — dry-run write-suppression branch in `actionHandler.Exec`.
- `internal/flow/dryrun.go` — reduced to `isWriteOp`; dead shim deleted.
- `internal/flow/handlers_dryrun_test.go` — suppression unit test (spy client).
- `internal/auth/operator.go` — `OperatorGuard`, `StaticTokenOperatorAuth`, deny-by-default.
- `internal/auth/operator_test.go` — guard + constructor fail-fast tests.
- `internal/auth/middleware.go` — `bearerToken` exported as `BearerToken`.
- `internal/httpapi/admin.go` — `AdminStore`, `newAdmin`, mount, `statusForAdmin`, env/actor helpers.
- `internal/httpapi/admin_handlers.go` — create/publish/rollback/jdm/connections/audit handlers.
- `internal/httpapi/admin_validate.go` — validate (stored + candidate), mock registry.
- `internal/httpapi/admin_dryrun.go` — dry-run handler (writes suppressed, trace + response).
- `internal/httpapi/admin_types.go` — `AdminFixture`, down/up-map, secret-value rejection helpers.
- `internal/httpapi/server.go` — `Deps.Admin`/`Deps.OperAuth`; mount admin before catch-all.
- `internal/httpapi/admin_test.go` — handler/routing/status unit tests.
- `internal/httpapi/admin_integration_test.go` — end-to-end admin + readyz + dry-run integration.
- `cmd/engine/main.go` — `buildOperatorAuth` (fatal on bad config), same store as `store` + `Admin`.
- `.env.example` — `ADMIN_ENABLED` / `ADMIN_TOKENS` documented.

Full diff: `git -C /home/nuzirwan/project/rule-engine-api/.worktrees/admin-api diff mainline`

</details>
