# Increment report — Slice F: Config-management Admin API (control plane) + two live-bug fixes

Branch: `feat/admin-api` · Worktree: `/home/nuzirwan/project/rule-engine-api/.worktrees/admin-api`
Build commit: `df547be` (`feat(admin): config-management /admin control plane + two live-bug fixes`)
Verdict: **APPROVED** (semantic review in `docs/.agents/tasks/admin-api/review.md`).

This increment turns the already-built, already-tested config-write store methods into a
privileged `/admin/*` HTTP control plane, adds a separate deny-by-default operator-auth mechanism,
wires the validate/dry-run author tools, and ships the two live-run bug fixes found during the live
engine run (`/readyz` false-negative; coarse all-or-nothing seeding). It is a thin HTTP +
request-shape layer over the frozen `config.Store` seam — it reimplements no store method and
leaves the pure flow/config cores intact.

## What was built — endpoint surface

All routes are code-registered Go 1.22 method-aware patterns on the same `http.ServeMux` as the
public data plane. Each is operator-guarded (deny-by-default). ServeMux longest-pattern precedence
puts `/admin/*` ahead of the public `"/"` catch-all with no special routing machinery (same as
`/livez` `/readyz` `/metrics`).

| Method + route | Store method | Purpose |
| --- | --- | --- |
| `POST /admin/flows` | `PutFlowVersion` | Create an immutable flow version (store assigns `version`; lands `validated=false`). Structural + ref validation runs before Postgres is touched. |
| `POST /admin/flows/{id}/publish` | `SetActive` | Advance the active pointer to a version. Publish-blocking: an un-validated version → 422. |
| `POST /admin/flows/{id}/rollback` | `SetActive` | Move the active pointer back. Same method, distinct control-plane intent + audit. |
| `POST /admin/flows/validate` | `GetFlowVersion` / `MarkValidated` | Structural + fixture validation. Stored mode (`flowId`+`version`) marks-validated on all-pass; candidate mode (inline `flow`) is stateless. Always 200 with an `ok` body. |
| `POST /admin/flows/dry-run` | (interpreter only) | Run a flow with writes suppressed (`observ.WithDryRun`); returns the trace + stitched response. No store write. |
| `POST /admin/jdms` | `PutJDMVersion` | Create a JDM/rule version and activate it. |
| `POST /admin/connections` | `PutConnectionVersion` | Register a connection def that points at an existing DB/REST (never provisions). `secret_ref` only. |
| `GET /admin/connections` | `Connections` | List active connection defs, settings redacted; `secretRef` pointer preserved. |
| `GET /admin/audit/{type}/{id}` | `AuditTrail` | Read the newest-first audit trail for `{flow,jdm,connection}`. |

### Status-code contract (what Strapi integrates against)
Classification is by `errors.Is`/`errors.As` against the `config.ConfigError` taxonomy — never
string-match. `statusForAdmin` dispatches in priority order so the two specific sentinels are
matched before the generic validation sentinel:

- `409` — route `(method,path)` collision (`config.ErrRouteConflict`, from SQLSTATE `23505` on
  `flows_method_path_key`). Never aliases a generic 400 or a 502.
- `422` — publish of an un-validated version (`config.ErrUnvalidated`). Never aliases a generic 400.
- `404` — `config.ErrNotFound`.
- `400` — request-shape invalid, bad flow tree, a secret value present in a connection body, or any
  other `config.ErrValidation`.
- `504` — `config.ErrTimeout` · `502` — `config.ErrUpstream` · `500` — internal / nil admin store.
- `201` — successful create (flows/jdms/connections). `200` — publish/rollback/list/audit, and
  validate/dry-run (a negative *result* is a 200 body, not a 4xx).

## Auth model

The operator plane is a **distinct trust domain** from the public JWT data plane — a separate
credential path, its own config keys, its own blast radius (it can rewrite every API). It is **not**
a role on the public JWKS token.

- **Deny-by-default, fail-closed.** `OperatorGuard.Protect`: nil authenticator ⇒ `503`
  (mount-closed, absence-of-config is denial, never a bypass); authn failure ⇒ `401`; missing RBAC
  role ⇒ `403`; otherwise the `Operator` is injected into the request context. The public chain's
  "bypass when unconfigured" convenience is deliberately **not** mirrored here.
- **Credential scheme (v1):** static hashed bearer operator tokens.
  `ADMIN_TOKENS` is a `;`-separated allow-list of `sha256hex:subject:comma,roles`; the engine stores
  only SHA-256 hashes. `StaticTokenOperatorAuth` matches constant-time (`crypto/subtle`) over the
  whole allow-list (no timing oracle, no early-exit leak) and parses the subject on the first/last
  colon so a colon-bearing subject like `op:alice` is unambiguous. The interface
  (`OperatorAuthenticator`) is the seam, so mTLS or an operator OIDC issuer can be swapped in later
  without touching a handler.
- **Fail-fast config.** `ADMIN_ENABLED=true` with a malformed entry (not three parts, non-hex/wrong-
  length hash, empty subject, duplicate subject) or zero valid tokens is a **fatal boot error** in
  `cmd/engine` — an enabled-but-empty allow-list never boots. When the plane is disabled,
  `cmd/engine` passes a nil authenticator and `/admin/*` is mount-closed.
- **RBAC per route** (`requireRole`, in the composition layer so `auth` carries no routing
  knowledge): writes (`flows`/`jdms`/`connections` POST) ⇒ `flow.write`; `publish`/`rollback` ⇒
  `flow.publish`; `validate`/`dry-run`/all GETs ⇒ `flow.read`.
- **Per-operator audit attribution.** The guard injects the operator subject into context; the admin
  edge stamps it via `config.WithAuditActor(ctx, subject)` before each write, so every `audit_log` /
  `created_by` row attributes to the operator who made it. No method signature changed — this mirrors
  the existing env-on-context / dry-run-on-context conventions.
- **Secrets never cross the wire or the logs.** The connection-create handler rejects any top-level
  secret-value field and any `settings.*` secret-value key with `400` before the store is touched;
  the connection-list response runs `settings` through the central `Redactor` as a backstop while
  preserving the non-secret `secretRef`. The operator token (plaintext or hash), any `Authorization`
  header, and any connection secret are never logged or echoed — audit lines carry only
  subject/resource/action/`error_class`.

## The two live-bug fixes

### 1. `/readyz` false-negative (`internal/httpapi/ops.go`)
The readiness probe gated on `registry.HealthCheck` over **every** data-source connection, so a
healthy engine against a reachable config store was taken out of rotation the moment any downstream
data source (e.g. the external `fmc_utility` Postgres) was unreachable. Readiness now gates on the
**config store `Ping` only** — the config store is the engine's serve-dependency; a data source is
config the engine uses per request, surfacing as a per-request `502/504`, not a readiness gate.
`503 {"notReady":"config_store"}` on store-ping failure; `200 {"status":"ready"}` otherwise; a nil
store (in-memory mode) skips the gate (200). Proven by `ops_test.go`: healthy store + failing
data-source registry ⇒ 200.

### 2. Per-object idempotent seeding (`internal/config/seed_pg.go`)
`SeedPgStore` was all-or-nothing ("if any active flow exists ⇒ skip ALL seeding"), so a new flow
added to the seed was silently not written against an already-seeded store (workaround was a
destructive `DROP SCHEMA … CASCADE`). It is rewritten to per-object create-if-absent using new
store-internal PK reads `FlowExists`/`JDMExists`/`ConnectionExists`: each absent object is written,
each present object is skipped and never version-churned (bootstrap-only). A real existence-probe
error aborts the seed rather than treating a transient outage as "absent" and double-writing.
Proven by `seed_pg_integration_test.go` (ephemeral postgres:16): seed #1 writes `alpha`; seed #2
adds only `beta` with `alpha` byte-identical (same version + checksum, no duplicate audit row); seed
#3 is a pure no-op.

Supporting the fixes and the surface, the dry-run write-suppression was wired into
`flow.actionHandler.Exec` on the live `observ` seam (`observ.IsDryRun` + `CollectorFrom`, records
`wrote:"suppressed"`, skips `client.Execute` for write ops `exec/set/del/http`); the dead
`flow/dryrun.go` shim was deleted, keeping only `isWriteOp`. Reads (`query/get/ping`) are not
suppressed.

## Verification
From `docs/.agents/tasks/admin-api/verification.md` and commit `df547be` — all GREEN, run from the
worktree root; integration suites used ephemeral `postgres:16` (+ valkey) containers only, no
external DB touched:

- `CGO_ENABLED=1 go build ./...` · `go vet ./...` · `go test ./...` — PASS (all packages).
- `go test -tags 'integration cgo' ./internal/httpapi` — PASS (ephemeral postgres:16).
- `go test -tags 'integration cgo' ./internal/config` — PASS (ephemeral postgres:16 + valkey).
- `CGO_ENABLED=0 go build ./internal/decision` — PASS (the !cgo stub still compiles).
- Targeted: `-race ./internal/flow`, `TestSeedPerObjectIdempotency`, `TestAdminDryRunSuppressesWrite` — PASS.

## Documented limitations
- **In-memory mode has no admin writes.** The admin plane is Postgres-backed; in in-memory mode
  `deps.Admin` is nil and admin writes return a classified "requires config-store mode" (500). The
  data plane still serves.
- **Single env.** The engine process serves env `""`. An admin request may carry `env` absent or
  explicitly `""`; any non-empty `env` is a `400`. A multi-env admin surface is out of scope.
- **Dry-run reads can hit live sources.** With `mocks` omitted, `/admin/flows/dry-run` (gated on
  `flow.read`) performs real read I/O against the wired data sources — faithful preview, by design
  (§2.7). Writes are always suppressed. `mocks` is the escape hatch for fully-isolated preview.
- **Dry-run trace fidelity.** v1 emits the suppressed-write record(s) + the stitched response, not a
  full per-node walk (the interpreter does not yet wrap each node in `observ.TraceNode`). Full
  per-node trace is a later increment.
- **Validate ref resolution is against the active set.** A flow referencing a JDM/connection that is
  created-but-not-yet-active shows a dangling-ref; `HasJDM` treats a transient store error as
  "present" to avoid blocking authors (publish re-checks at request time).
- **Seed is bootstrap-only.** Editing `seed.json` for an existing object does **not** update it on
  restart (skip-if-exists); live edits go through the admin API / Strapi.
- **One stale struct-doc comment** on `httpapi.Deps.Store` still describes the old
  `Conns.HealthCheck AND Store.Ping` readyz gate; the code correctly gates on `Store.Ping` only.
  Cosmetic, non-blocking, flagged for a later touch.
