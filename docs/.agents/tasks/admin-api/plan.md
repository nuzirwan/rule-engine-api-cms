# Implementation Plan — Slice F: Admin API (control plane) + two live-bug fixes

Source of truth: the APPROVED design `.worktrees/admin-api/docs/lld/slice-f-admin-api.md`
(review `docs/.agents/tasks/admin-api/design-review.md`, verdict APPROVED), the frozen
`.worktrees/admin-api/docs/lld-contracts.md`, and `.worktrees/admin-api/docs/STATE.md`.

All paths below are ABSOLUTE under `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api`.
This plan sequences the work faithfully from the approved design; it does not re-decide the
architecture. Each design decision it restates is tagged with the design section (§) it comes from.

## Tooling / standards notes
- `mcp_codegraph_codegraph_explore` IS available against the repo-root `.codegraph/` index
  (the worktree itself has no `.codegraph/`; pass `projectPath` = the repo root
  `/home/nuzirwan/project/rule-engine-api` when exploring, which indexes the same tree). The
  implementer SHOULD call it first with symbol-name queries before editing a source file, and
  again after any signature/interface change to re-check the blast radius.
- `engineering-standards` MCP wiki IS available. `detect_project` → `{language: go, arch: rest-api}`.
  There is **no `go` language page** (missing); the `architecture-rest-api-service` page and the
  universal concepts exist and are applied + cited below:
  `[[config-driven-boundaries]]`, `[[security-and-authz]]`, `[[config-and-secrets]]`,
  `[[error-classification]]`, `[[versioning-and-compatibility]]`, `[[low-level-design]]`,
  `[[caching-strategy]]`, `[[observability-and-logging]]`, `[[architecture-rest-api-service]]`.
  The codebase's own conventions (stdlib `net/http` only, classified `config.ConfigError` taxonomy,
  table-driven tests, context-carried cross-cutting flags) are the operative in-repo standards and
  are followed throughout.

## Scope correction carried from design §0 (read before starting)
`STATE.md` claims the `/admin/flows/validate` + `/admin/flows/dry-run` endpoints and the dry-run
write-suppression are already wired. **That is stale/false in this worktree** (verified:
`internal/httpapi/{server.go,ops.go,adapters.go}` contain no `/admin` route or `WithDryRun` call;
`internal/flow/handlers.go` `actionHandler.Exec` never consults a dry-run flag;
`internal/flow/dryrun.go` is a dead shim keyed on package-local `dryRunKey{}`). Validate, dry-run,
and the suppression fix are therefore BUILT FRESH here, not "integrated from existing wiring".

## Ordering rationale (dependency graph)
Items are ordered so the codebase stays buildable after each and so every consumer's dependency
lands first:
- The `internal/config` store-internal seam additions (items 2–5) are prerequisites for the
  per-object seeding fix (item 6), stored-mode validate (item 11), the route-conflict 409 (item 8),
  and per-operator audit attribution (item 13).
- The `internal/flow` dry-run suppression fix (item 7) is a prerequisite for the admin dry-run
  endpoint (item 12) honoring AC-14.
- The `internal/auth` operator-auth mechanism (item 9) is a prerequisite for mounting any guarded
  `/admin/*` route (items 10–12).
- The two live-bug fixes (item 1 `/readyz`, item 6 seeding) are independent of the admin surface
  and of each other; they are sequenced early so their value lands even if later items need rework.

---

- [x] 1. Fix `/readyz` 503 false-negative: gate readiness on the config-store `Ping` ONLY; drop the
      blanket data-source `registry.HealthCheck` gate (design §5). In `Ops.readyz`, remove the
      `o.reg.HealthCheck(ctx)` block entirely (data-source health is advisory per-request / via
      `/metrics`, NOT a readiness gate — `[[config-driven-boundaries]]`: a data source is config the
      engine uses per request, not a liveness dependency). Keep the `o.store.Ping(ctx)` gate
      (`503 {"notReady":"config_store"}` on failure) and the 2s `readyzTimeout`. The `o.reg` field
      may remain on the `Ops` struct unused, or be removed if `go vet` flags it — prefer removing it
      and its `newOps` assignment to keep the struct clean, but do NOT remove `connect` imports still
      used elsewhere in the file (none are after removal — drop the now-unused `connect` import).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/ops.go`
      Verify: add/extend a unit test (item below in same file set) then run
      `CGO_ENABLED=1 go test ./internal/httpapi` from the worktree root — new readyz tests pass,
      existing ops tests still pass.

- [x] 1a. Add a `/readyz` unit test proving the fix: a `readyz` wired with a healthy `store.Ping`
      and a **failing** data-source registry returns **200** (the whole point); a failing
      `store.Ping` returns `503` with `notReady: config_store`; a nil store skips the gate (200).
      Follow the existing table-driven style in `internal/httpapi` server/ops tests, use a fake
      pinger and a fake `connect.Registry` whose `HealthCheck` returns an error.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/ops_test.go`
      (new, or extend an existing `*_test.go` in that package).
      Verify: `CGO_ENABLED=1 go test ./internal/httpapi` from the worktree root — the new readyz=200
      case passes.

- [x] 2. Add store-internal existence helpers to `internal/config` for per-object idempotent seeding
      (design §6.3): `FlowExists(ctx, env, flowID string) (bool, error)`,
      `JDMExists(ctx, env, jdmID string) (bool, error)`,
      `ConnectionExists(ctx, env, key string) (bool, error)` on `*PgStore`. Each is a single indexed
      PK read (`SELECT 1 FROM flows WHERE id=$1` / `jdms WHERE id=$1` / `connections WHERE key=$1`),
      scoped to the env's pool via `s.pool(env)`, classified per the store taxonomy via `classifyPg`
      (a no-row result ⇒ `(false, nil)`; a real DB error ⇒ surfaced as `Upstream`,
      `[[error-classification]]`). These are NOT on the frozen `Store` seam — they mirror the
      existing store-internal `MarkValidated`/`PromoteVersion`.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/config/pgstore.go`
      Verify: `CGO_ENABLED=1 go build ./internal/config && CGO_ENABLED=1 go vet ./internal/config`
      from the worktree root — compiles clean.

- [x] 3. Add store-internal `GetFlowVersion(ctx, env, flowID string, version int) (FlowVersion, error)`
      to `*PgStore` (design §2.6/§6.3) for stored-mode validate. One PK read of `flow_versions`
      (by `flow_id` + `version`) plus its `flow_fixtures`, decoding the stored tree JSON into
      `flow.Node` and the fixtures into `[]FlowFixture` exactly as `ActiveFlow` already does; a
      missing row ⇒ `NotFound` (`[[error-classification]]`). Do NOT overload `ActiveFlow` (it is
      hot-path and active-only).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/config/pgstore.go`
      Verify: `CGO_ENABLED=1 go build ./internal/config` from the worktree root — compiles clean.

- [x] 4. Add the route-conflict classify tweak to `internal/config` (design §2.3/§8.7). In
      `classifyPg`, detect `pgconn.PgError.Code == "23505"` on the `flows_method_path_key`
      constraint and wrap it as a typed `config.Validation` carrying a stable `route_conflict`
      marker (a dedicated sentinel `ErrRouteConflict` wrapped as `Validation`, or an exported
      marker the admin edge can match via `errors.Is`), instead of the current blanket `Upstream`.
      This lets the admin edge map a `(method,path)` collision deterministically to `409` rather
      than a misleading `502` (`[[versioning-and-compatibility]]`, `[[error-classification]]`).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/config/pgstore.go`
      Verify: `CGO_ENABLED=1 go build ./internal/config && CGO_ENABLED=1 go vet ./internal/config`
      from the worktree root — compiles clean.

- [x] 5. Add the actor-on-context seam to `internal/config` (design §3.5/§8.1) for per-operator
      audit attribution: a `config.WithAuditActor(ctx, subject) context.Context` helper + a
      `auditActorFrom(ctx)` reader, and change the store's `audit_log` inserts to use the
      ctx-carried actor when present, else fall back to the construction-time `s.actor`
      (`WithActor`, default `"engine"`). Additive — NO method-signature change (mirrors the existing
      env-on-context / dry-run-on-context conventions). This is the design default (option B); if
      the implementer finds the audit-insert sites too scattered to thread cleanly, the documented
      fallback (§3.5) is single-actor attribution with the operator subject captured in the
      `observ` audit log line — but attempt the context helper first.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/config/pgstore.go`
      (and a small helper file `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/config/actor.go`
      if a new file is cleaner than inlining).
      Verify: `CGO_ENABLED=1 go build ./internal/config` from the worktree root — compiles clean.

- [x] 6. Fix per-flow/per-object idempotent seeding in `SeedPgStore` (design §6). Replace the
      all-or-nothing top-level gate (`ActiveFlow(sf.Flows[0]...)` ⇒ skip ALL) with per-object
      create-if-absent using the item-2 helpers: for each flow, `if !FlowExists → PutFlowVersion
      [+ MarkValidated + SetActive if f.Active]`, else skip; for each JDM, `if !JDMExists →
      PutJDMVersion`, else skip; for each connection, `if !ConnectionExists → PutConnectionVersion`,
      else skip. Return `seeded = (anything was written)`. Seed is bootstrap-only: an existing object
      is skipped, never version-churned (design §6.5, decision D3). Keep connection writes
      `secret_ref`-only (unchanged — the seed path already passes `SecretRef`, never a value,
      `[[config-and-secrets]]`).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/config/seed_pg.go`
      Verify: `CGO_ENABLED=1 go build ./internal/config` from the worktree root — compiles clean;
      full proof is item 6a's integration test.

- [x] 6a. Add a config integration test proving the seeding fix (design §7). Seed once into an
      ephemeral `postgres:16`; add a SECOND, ADDITIONAL flow to the seed doc; re-seed; assert the
      new flow is written + active AND the first flow's existing version row is byte-identical and
      NOT duplicated (same max version, same checksum); re-seed again with no changes ⇒
      `seeded==false`, zero new versions / audit rows. Follow the existing build-tagged pattern in
      `internal/config/configrun_integration_test.go` (ephemeral containers only — NEVER an
      external DB).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/config/seed_pg_integration_test.go`
      (new; `//go:build integration && cgo`), or extend `configrun_integration_test.go`.
      Verify: `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/config` from the worktree
      root (Docker: ephemeral postgres:16) — the additional-flow and re-seed-noop assertions pass.

- [x] 7. Wire dry-run write-suppression into `flow.actionHandler` and delete the dead shim
      (design §4.3, lld-contracts.md AC-14). In `actionHandler.Exec`, BEFORE
      `client.Execute(ctx, op)`, add: `if observ.IsDryRun(ctx) && isWriteOp(op.Kind) { if tc, ok :=
      observ.CollectorFrom(ctx); ok { tc.Record(n.ID, string(n.Type), "", map[string]any{"wrote":
      "suppressed"}) }; return walkChildren(ctx, n.Children, c, dep, w) }`. Import `internal/observ`
      (no cycle — `observ` does not import `flow`). In `dryrun.go`, delete the dead
      `dryRunKey`/`withDryRun`/`isDryRun` shim (and its TODO), KEEP `isWriteOp(kind string) bool`
      as the single source of write-vs-read. Confirm `op.Kind` is the field name on
      `connect.Operation` (it is: `Operation.Kind`).
      Files:
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/flow/handlers.go`,
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/flow/dryrun.go`
      Verify: `CGO_ENABLED=1 go build ./internal/flow && CGO_ENABLED=1 go vet ./internal/flow`
      from the worktree root — compiles clean; behavior proven in item 7a.

- [x] 7a. Add a `flow`-package unit test for the suppression fix (design §7): an `action` node with a
      write op (`exec`/`set`/`del`/`http`) run under `observ.WithDryRun(ctx)` +
      `observ.WithCollector(ctx, collector)` performs NO `Execute` write (assert via a spy
      `connect.Client` whose write methods fail the test if called) and records a trace node with
      `wrote: "suppressed"`; the same op WITHOUT dry-run performs the real `Execute`. Also assert a
      read op (`query`/`get`) is NOT suppressed under dry-run. Table-driven, matching the package's
      existing test style.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/flow/handlers_dryrun_test.go`
      (new, or extend an existing `internal/flow/*_test.go`).
      Verify: `CGO_ENABLED=1 go test ./internal/flow` from the worktree root — new suppression tests
      pass; run `CGO_ENABLED=1 go test -race ./internal/flow` if the budget node races were a prior
      concern (STATE.md notes a fixed data race in this package).

- [x] 8. Build the operator-auth mechanism in `internal/auth`, separate from the public JWT path,
      deny-by-default (design §3). Create `internal/auth/operator.go` defining:
      `OperatorAuthenticator` interface (`AuthenticateOperator(ctx, r) (Operator, error)`),
      `Operator{Subject string; Roles []string}`, `OperatorGuard{authn, require, log}` with
      `Protect(next http.HandlerFunc) http.Handler`, and `StaticTokenOperatorAuth` (v1 hashed
      bearer-token allow-list, decision D1). `Protect` is deny-by-default (`[[security-and-authz]]`):
      `authn == nil` ⇒ `503 {"error":"admin plane disabled"}` (mount-closed, never mount-open);
      `AuthenticateOperator` error ⇒ `401` (detail logged, never the credential); RBAC
      `require(r)` role not in `Operator.Roles` ⇒ `403` (security event logged: subject + resource +
      action, never the credential); otherwise inject `Operator` into request context and call
      `next`. `StaticTokenOperatorAuth` stores only SHA-256 hashes, matches with constant-time
      `crypto/subtle` compare (no timing oracle), and its constructor returns an error on malformed
      config (an entry not three `:`-parts, non-hex/wrong-length hash, empty or duplicate subject) or
      zero valid tokens — so `cmd/engine` can treat `ADMIN_ENABLED=true` + bad/empty tokens as a
      fatal boot error (design §3.3). No `Count()` method on the interface. The token/hash/header is
      NEVER logged (`[[config-and-secrets]]`, design §3.6).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/auth/operator.go`
      Verify: `CGO_ENABLED=1 go build ./internal/auth && CGO_ENABLED=1 go vet ./internal/auth`
      from the worktree root — compiles clean.

- [x] 8a. Export the RFC-7235 bearer parser as `auth.BearerToken(r *http.Request) (string, bool)`
      (design §3.3/§8.8). Rename the unexported `bearerToken` in `internal/auth/middleware.go` to
      `BearerToken` and update its in-package callers so the admin middleware reuses the single
      parser rather than duplicating one.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/auth/middleware.go`
      (plus any in-package references).
      Verify: `CGO_ENABLED=1 go build ./internal/auth` from the worktree root — compiles clean.

- [x] 8b. Add `internal/auth` unit tests for the operator guard (design §7): disabled-plane
      (`authn == nil`) ⇒ 503; bad credential ⇒ 401; wrong role ⇒ 403; valid ⇒ `next` called with
      the `Operator` in context; and `StaticTokenOperatorAuth` constructor rejects malformed /
      empty token config (fatal error) and accepts a well-formed allow-list with constant-time
      match. Assert the fake logger captured NOTHING sensitive (no token/hash/header value).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/auth/operator_test.go`
      Verify: `CGO_ENABLED=1 go test ./internal/auth` from the worktree root — new tests pass.

- [x] 9. Add the admin wiring fields to `httpapi.Deps` and build+mount the admin surface in
      `NewHandler` (design §2.1a/§8.10). Add to `Deps`: `Admin AdminStore` (nil ⇒ admin writes
      return a classified "requires config-store mode" error) and
      `OperAuth auth.OperatorAuthenticator` (nil ⇒ operator plane mount-closed). Keep
      `NewHandler(store Store, interp, deps)`'s signature; inside it, after `ops.mount(mux)` and
      BEFORE the `"/"` catch-all, build `admin := newAdmin(interp, deps)` and call
      `admin.mount(mux)`. Define `AdminStore` (design §2.2) as the narrow interface the handlers
      depend on (DIP, `[[low-level-design]]`): `PutFlowVersion`, `SetActive`, `Connections`,
      `GetJDM`, `ActiveFlow`, `PutJDMVersion`, `PutConnectionVersion`, `MarkValidated`, `AuditTrail`,
      plus the new `GetFlowVersion` (item 3) — all satisfied structurally by `*config.PgStore`.
      Files:
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/server.go`,
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/admin.go` (new —
      may hold `AdminStore`, `newAdmin`, the `Admin` type and `mount`; handlers can live here or in
      sibling files per item 10).
      Verify: `CGO_ENABLED=1 go build ./internal/httpapi` from the worktree root — compiles clean
      (handlers may be stubs returning 501 until item 10 fills them, as long as it builds).

- [x] 10. Implement the admin HTTP handlers — the thin HTTP + request-shape layer over the existing
      store methods (design §2.3–§2.5, §2.8). In `internal/httpapi/admin.go` (and/or
      `admin_handlers.go`), register via `a.mount` with Go 1.22 method-aware patterns, each wrapped
      by `a.guard.Protect`:
      `POST /admin/flows` (→ `PutFlowVersion`, 201, store-assigned version, request `version`
      ignored; request-shape + `flow.ValidateTree` structural check first ⇒ 400 on bad tree;
      `(method,path)` collision ⇒ 409 via item-4 marker, else 502 pre-tweak); `POST
      /admin/flows/{id}/publish` and `/rollback` (→ `SetActive`, response `action` DERIVED FROM THE
      ROUTE per §2.4/§2.5 since `SetActive` returns only `error`; publish of an un-validated version
      ⇒ 422); `POST /admin/jdms` (→ `PutJDMVersion`, 201; empty `doc` ⇒ 400); `POST
      /admin/connections` (→ `PutConnectionVersion`, 201; REJECT any secret-value field —
      `password`/`secret`/`token`/`apiKey`/`settings.password` — with 400, accept `secretRef` only,
      `[[config-and-secrets]]`, AC-20); `GET /admin/connections` (→ `Connections`, 200, run the
      response through the central `observ` Redactor as a backstop); `GET /admin/audit/{type}/{id}`
      (→ `AuditTrail`, `{type}` ∈ {flow,jdm,connection} else 400, newest-first, nullable
      `fromVersion`/`toVersion` encode as JSON null). Set the audit actor via
      `config.WithAuditActor(ctx, operator.Subject)` (item 5) before each store write. Supply the
      route→role `require` map (design §3.4) and the `env` rule (§2.9: absent/`""` ⇒ ok; any
      non-empty ⇒ 400) from the composition layer. Add `statusForAdmin(err) (int, string)` mapping
      the `config.ConfigError` taxonomy to HTTP status per the §2.9 table (classify via
      `errors.Is`/`errors.As`, never string-match; client body coarse, full error logged at the
      seam, never a secret). When `deps.Admin == nil`, writes return the classified "requires
      config-store mode" error.
      Files:
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/admin.go`,
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/admin_handlers.go`
      (optional split).
      Verify: `CGO_ENABLED=1 go build ./internal/httpapi && CGO_ENABLED=1 go vet ./internal/httpapi`
      from the worktree root — compiles clean; behavior proven in items 10a + 13.

- [x] 10a. Add admin handler + routing-precedence unit tests (design §7) against a FAKE `AdminStore`
      (canned values / classified errors) and a fake `OperatorAuthenticator`: assert each
      request→method mapping and status code, the secret-value rejection (400), the full
      `statusForAdmin` error→status map (400/404/409/422/502/504/500), and that a `ServeMux` wired
      with the admin routes resolves `/admin/flows/validate` to the validate handler (NOT
      `{id}/publish`), dispatches `GET` vs `POST`, and captures `{type}`/`{id}`. No DB.
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/admin_test.go`
      Verify: `CGO_ENABLED=1 go test ./internal/httpapi` from the worktree root — new admin tests
      pass.

- [x] 11. Implement `POST /admin/flows/validate` (design §2.6, publish-blocking, always 200 on a
      result). Define the admin-request-local `AdminFixture` type (design §4.2 —
      `{Name, Input, Mocks, Expect{Output,BranchPath,Errors}}`) in `internal/httpapi`; do NOT widen
      the frozen `config.FlowFixture {Name,Input,Want}`. Mode discriminator (§2.6): `flowId` + a
      POSITIVE `version` ⇒ stored mode (load via `GetFlowVersion` item 3, validate, `MarkValidated`
      on all-pass; inline `flow` ignored; no stored match ⇒ 404); else candidate mode (inline flow,
      stateless, never `MarkValidated`); neither usable `flow` nor `flowId`+`version` ⇒ 400.
      Validate = `flow.ValidateTree(fv.Tree, refs)` (real signature `ValidateTree(root Node, refs
      RefResolver)`) with a `storeRefs` adapter (`HasConnection` via `Connections(env)` membership;
      `HasJDM` via `GetJDM`, NotFound ⇒ absent, other error ⇒ assume present per §4.1) PLUS fixture
      replay against a mock `connect.Registry` (§4.2) with a `TraceCollector` attached, asserting
      each fixture's `Expect`. Response is always 200 with `{ok, structural[], fixtures[]}`; only a
      malformed request or a stored-mode 404 is non-200. On persist of a candidate flow (via item
      10's create path) down-map `AdminFixture` → `config.FlowFixture` (name+input preserved,
      `Expect.Output`→`Want`, `Mocks` validation-time-only, not persisted).
      Files:
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/admin.go`
      (handler + `storeRefs` + `AdminFixture` + down-map; may split a `validate.go`).
      Verify: `CGO_ENABLED=1 go test ./internal/httpapi` from the worktree root — a unit test with
      mocked sources asserts structural issues and fixture pass/fail diffs, stored vs candidate mode
      selection, and the 404 stored-no-match case.

- [x] 12. Implement `POST /admin/flows/dry-run` (design §2.7, writes suppressed, AC-14). Build
      `ctx` with `observ.WithDryRun(ctx)` AND `observ.WithCollector(ctx, collector)`, build a
      `flow.Ctx` from `input.params` (same lifting the data-plane edge does), resolve the flow
      (`version` optional ⇒ default to the active via `ActiveFlow`; else `GetFlowVersion`), and run
      `interp.Run`. With the item-7 fix, write ops record `wrote:"suppressed"` and perform no I/O.
      `mocks` optional (omitted ⇒ reads hit real sources via `deps.Conns`, writes always
      suppressed). Response always 200 with `{trace[], response, errors[]}` — v1 fidelity is the
      suppressed-write record(s) + the final response (NOT a full per-node walk; decision D5 defers
      the per-node `observ.TraceNode` stretch). A flow error is summarized in `errors[]` at 200
      (dry-run is a debugger); a bad request shape (no flowId AND no resolvable active flow) ⇒ 400.
      Files:
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/admin.go`
      (handler; may split a `dryrun.go`).
      Verify: `CGO_ENABLED=1 go test ./internal/httpapi` from the worktree root — a unit test asserts
      a dry-run over a flow with a write op returns 200, no real write occurred, and the trace
      contains the suppressed-write record.

- [x] 13. Wire the composition root in `cmd/engine` (design §2.1a/§3.3/§8.11) and extend
      `.env.example`. Read `ADMIN_ENABLED` and `ADMIN_TOKENS` (operator-plane-only keys, never the
      public JWT keys). When `ADMIN_ENABLED=true`, construct `auth.StaticTokenOperatorAuth` from
      `ADMIN_TOKENS` and treat a constructor error (malformed / zero valid tokens) as a FATAL boot
      error (non-zero exit); when disabled/unset, pass a nil `OperatorAuthenticator` (plane
      mount-closed). In config-store mode, pass the SAME `*config.PgStore` instance both as the
      `store` argument and as `deps.Admin`; in in-memory mode pass the `*memStore` as `store` and
      leave `deps.Admin == nil` (admin writes return the classified "requires config-store mode"
      error; data plane still serves). Add `ADMIN_ENABLED` / `ADMIN_TOKENS` to `.env.example` with a
      note that `ADMIN_TOKENS` holds `sha256hex:subject:comma,roles` entries (`;`-separated) and the
      plaintext token is never stored. Per the merge-contention rule, `cmd/engine` is touched only
      here at the join.
      Files:
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/cmd/engine/main.go`
      (and any `cmd/engine` wiring helpers it uses),
      `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/.env.example`
      Verify: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go vet ./...` from the worktree root —
      compiles and vets clean.

- [x] 14. Add the admin + fixes integration coverage to the httpapi suite (design §7). Against
      ephemeral `postgres:16` + valkey, assert: an end-to-end create → validate → publish → audit
      round-trip over `/admin/*` with a valid operator token; publish of an un-validated version ⇒
      422; a secret-value connection body ⇒ 400 and `GET /admin/connections` shows `secretRef` only
      (no secret value); a disabled operator plane ⇒ 503 on `/admin/*`; a dry-run against a real
      stored flow with a write op suppresses the write (no row written) and returns a trace; and
      `/readyz == 200` when the config store is healthy but a data-source connection points at a
      dead address. Extend the existing build-tagged `internal/httpapi/integration_test.go`
      (ephemeral containers only — NEVER an external DB).
      Files: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/integration_test.go`
      (extend) or `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/internal/httpapi/admin_integration_test.go`
      (new; `//go:build integration && cgo`).
      Verify: `CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi` from the worktree
      root (Docker: ephemeral postgres:16 + valkey) — all new admin, readyz, and dry-run integration
      assertions pass.

---

## Final verification gate (implementer AND final reviewer run ALL of these from the worktree root `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api`)

```
CGO_ENABLED=1 go build ./...
CGO_ENABLED=1 go vet ./...
CGO_ENABLED=1 go test ./...
CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/httpapi   # Docker: ephemeral postgres:16 + valkey
CGO_ENABLED=1 go test -tags 'integration cgo' ./internal/config    # config-run integration
CGO_ENABLED=0 go build ./internal/decision                         # stub must compile
```

Hard constraint: integration tests MUST NEVER touch an external DB — ephemeral `postgres:16` /
valkey containers only. The final reviewer writes its verdict to
`/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api/docs/.agents/tasks/admin-api/review.json`
field `verdict` (value `APPROVED` to stop the build loop).

## Seam-change checklist (design §8 — all additive, non-conflicting)
- `internal/config`: existence helpers (item 2), `GetFlowVersion` (item 3), route-conflict classify
  tweak (item 4), actor-on-context (item 5), `SeedPgStore` rewrite (item 6).
- `internal/flow`: dry-run write-suppression + delete dead shim (item 7).
- `internal/httpapi`: `/readyz` gate fix (item 1), `Deps.Admin`/`Deps.OperAuth` + mount (item 9),
  admin handlers + validate + dry-run (items 10–12).
- `internal/auth`: operator-auth mechanism (item 8), export `BearerToken` (item 8a).
- `cmd/engine`: operator-auth wiring + same-store-twice (item 13) — touched only at the join.

## Deferred (documented in design, NOT in scope for this slice)
Full per-node dry-run trace via `observ.TraceNode` (D5); effective-action pointer-read refinement
for publish/rollback (§2.4); optional `GET /admin/health/connections` diagnostic (§5.3); advisory
lock hardening for concurrent first-boot seeding (§6.5); finer HTTP read/write split on the `http`
op kind (§4.3).
