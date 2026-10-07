# LLD — Slice F: Config-management Admin API (control plane) + two live-bug fixes

Package: `internal/httpapi` (new admin surface) + `internal/auth` (operator-auth additions) +
small in-scope fixes to `internal/httpapi/ops.go`, `internal/config/seed_pg.go`, and
`internal/flow/handlers.go`. Go 1.26. Module `nzr-rules-engine`. stdlib `net/http` only — no
chi/gin (project convention, `docs/STATE.md` "Environment").

This slice turns the **already-built, already-tested** config-write store methods
(`PgStore.PutFlowVersion` / `PutJDMVersion` / `PutConnectionVersion` / `SetActive` /
`MarkValidated` / `PromoteVersion` / `AuditTrail` / `Connections`, see
`internal/config/pgstore.go`) into a privileged, code-registered `/admin/*` HTTP control plane,
plus a separate operator-auth mechanism, plus the validate/dry-run author tools, plus the two
live-run bug fixes. It is a **thin HTTP + auth + request-shape layer** over the frozen
`config.Store` seam (`docs/lld-contracts.md`); it does **not** reimplement any store method.

---

## AS-BUILT reconciliation (read first — this doc is now the integration contract)

> **Status: BUILT + reviewed APPROVED + merged (commit `df547be`, branch `feat/admin-api`).**
> This document was written as a design with open decisions and "contingent" items. Everything
> below the design hedges actually **shipped**; the AS-BUILT facts in this block override any
> design-phase hedge later in the doc. Strapi integrates against these facts.

What the design flagged as pending/contingent/requested and is now **confirmed in code** (verified
against the worktree source + `docs/.agents/tasks/admin-api/review.md`):

- **§0 scope correction is settled.** validate + dry-run were built fresh here; dry-run
  write-suppression is wired into `flow.actionHandler.Exec` on the live `observ` seam
  (`observ.IsDryRun` + `CollectorFrom`, records `wrote:"suppressed"`, skips `client.Execute` for
  write ops), and the dead `flow/dryrun.go` shim was deleted (only `isWriteOp` kept). AC-14 is met.
- **§2.3 — 409 is NOT contingent; it is live.** `classifyPg` detects SQLSTATE `23505` on
  `flows_method_path_key` and wraps it as a `Validation` carrying `config.ErrRouteConflict`;
  `statusForAdmin` maps `ErrRouteConflict` ⇒ **409**. A route collision is NOT a 502. The design's
  "until the tweak lands this reports 502" no longer applies.
- **§2.6 — `GetFlowVersion` exists** on the admin store method-set (and `*config.PgStore`); stored-
  mode validate uses it. Mode discriminator is as specified.
- **§3.5 / §8.1 — actor-on-context landed (option B).** `config.WithAuditActor(ctx, subject)` is
  implemented and read by the store's audit inserts (`actorFor(ctx)`); the admin edge stamps the
  operator subject before each write. The single-`"admin"`-actor fallback was NOT taken.
- **§8 requested seam changes are ALL done:** `FlowExists`/`JDMExists`/`ConnectionExists`,
  `GetFlowVersion`, the 23505 route-conflict classify, `WithAuditActor`, the `SeedPgStore` rewrite,
  the dry-run suppression branch, the `/readyz` gate narrowing, `auth.BearerToken` export, the
  `OperatorGuard`/`StaticTokenOperatorAuth` mechanism, the `Deps.Admin`/`Deps.OperAuth` wiring, and
  the `cmd/engine` `ADMIN_ENABLED`/`ADMIN_TOKENS` wiring (fatal on bad/empty).
- **§9 open decisions D1–D5 are resolved as the design's chosen default:** D1 static hashed bearer
  tokens; D2 validate-stored gated on `flow.read`; D3 seed is bootstrap-only; D4 actor-on-context
  (option B); D5 dry-run trace = suppressed-write records + response, not a full per-node walk.
- **AS-BUILT error sentinels:** `config.ErrUnvalidated` (⇒ 422) and `config.ErrRouteConflict`
  (⇒ 409) are distinct sentinels matched before the generic `config.ErrValidation` (⇒ 400), so 409
  and 422 never alias each other or a generic 400.
- **One known cosmetic drift (non-blocking):** the `httpapi.Deps.Store` field doc comment still
  describes the old `Conns.HealthCheck AND Store.Ping` readyz gate; the code correctly gates on
  `Store.Ping` only (§5.2). Comment lags; behavior is as this doc states.

Everything else in this document (routing/precedence, request/response JSON, the full status-code
table in §2.9, operator-auth behavior, RBAC map, the two bug fixes) reflects what shipped.

---

Grounded in the engineering-standards wiki (same vault the sibling slices cite):
- **`[[config-driven-boundaries]]`** — the admin plane manipulates config; it carries no business
  logic, and it never provisions infrastructure (a connection def only *points at* an existing
  DB/REST).
- **`[[security-and-authz]]` / deny-by-default** — the operator plane is a distinct credential/role
  path, fails closed, and is independent of the public JWT data-plane path.
- **`[[config-and-secrets]]`** — a connection version carries `secret_ref` ONLY; a secret value is
  never accepted on the wire, never stored, never logged or echoed.
- **`[[error-classification]]`** — every failure crosses the seam as a classified
  `Timeout/NotFound/Validation/Upstream/Internal` error and maps to a fixed HTTP status.
- **`[[versioning-and-compatibility]]`** — create is append-only; publish/rollback is a pointer
  move; validate gates publish.
- **`[[low-level-design]]`** — the surface is designed to the existing seams (DIP), stays
  high-cohesion/low-coupling, and keeps the pure flow/config cores untouched.

---

## 0. Scope correction vs STATE.md (read this first — it changes the plan)

`docs/STATE.md` ("Wiring join — DONE") states the admin endpoints `POST /admin/flows/validate` and
`POST /admin/flows/dry-run` **already exist and are wired**. That is **stale**. The subsequent
config-driven-routing refactor (commit `2064683`, `refactor(httpapi): zero-restart config-driven
router + ops endpoints`) replaced `internal/httpapi/server.go` with a single catch-all handler plus
code-registered ops endpoints only. Verified against the worktree:

- `internal/httpapi/{server.go,ops.go,adapters.go}` contain **no** `/admin` route, no
  `ValidateFlow`/`DryRun` handler, no `observ.WithDryRun` call. (`grep` for `admin|validate|
  dry-run|WithDryRun` across `internal/httpapi` returns nothing.)
- The dry-run **primitives** exist but are **not wired**: `observ.WithDryRun`/`IsDryRun`/
  `WithCollector`/`CollectorFrom` are present in `internal/observ/collector.go`, and
  `internal/flow/dryrun.go` has `isDryRun`/`isWriteOp` — but **`actionHandler.Exec` in
  `internal/flow/handlers.go` never calls them**, so AC-14 write-suppression is currently a no-op.
  (`grep` shows `isDryRun`/`isWriteOp` are defined only in `dryrun.go` and referenced nowhere else.)

**Consequence for this slice:** validate and dry-run are **built here, fresh**, not "integrated
from existing wiring". And dry-run's write-suppression requires a small in-scope fix to
`internal/flow/handlers.go` (§4.3). This is a correctness item, called out explicitly per the
review mandate.

---

## 1. Overview

A new `admin` sub-surface of `internal/httpapi` registers a set of **code-registered** `/admin/*`
routes on the same `http.ServeMux` the data plane uses. Because the public router is a single
`"/"` catch-all (`genericFlowHandler`), and Go 1.22+ `ServeMux` matches the **most specific**
pattern, any route registered under `/admin/` (e.g. `POST /admin/flows`) wins over `"/"` by
longest-pattern precedence — exactly as `/livez`, `/readyz`, `/metrics` already do. So the control
plane "takes precedence over the public config-driven catch-all router" with no special routing
machinery: it is just more-specific code-registered patterns.

Every `/admin/*` route is wrapped in an **operator-auth** middleware chain that is **separate from
and independent of** the public JWT data-plane authenticator. It is deny-by-default: no valid
operator credential ⇒ 401, valid-but-unauthorized ⇒ 403, and if the operator plane is not
configured at all, `/admin/*` is **mounted-closed** (every request 503/disabled) rather than open.

The handlers are thin: decode a typed request body, validate request shape, call exactly one
existing `config.Store` (or store-internal) method, map the classified result to an HTTP status +
JSON body. No handler contains flow/business logic; the heavy lifting already lives in `config`.

Two live bugs are fixed alongside: `/readyz`'s over-strict readiness criterion (§5) and
`SeedPgStore`'s all-or-nothing idempotency (§6).

---

## 2. The admin HTTP surface

### 2.1 Routing, mounting, precedence

A new file `internal/httpapi/admin.go` defines an `Admin` type and a `mount(mux)` method, called
from `NewHandler` **before** the `"/"` catch-all is registered (ordering is cosmetic — ServeMux
precedence is by specificity, not registration order, but we mount ops + admin first for
readability, matching `ops.mount`).

```go
// Admin is the control-plane surface. It holds the config store (as the concrete
// *config.PgStore, because several admin ops call store-internal methods that are
// deliberately NOT on the Store seam — MarkValidated/PromoteVersion/AuditTrail),
// the interpreter + deps for validate/dry-run, and the operator-auth guard.
type Admin struct {
    store   AdminStore          // see §2.2 — the admin method-set
    interp  *flow.Interpreter   // for validate (fixture replay) and dry-run
    deps    Deps                // Conns/Decide/Trace/Log reused for dry-run reads
    guard   *auth.OperatorGuard // §3 — operator-auth middleware (deny-by-default)
    env     string              // the single env this engine process serves ("" today)
    log     observ.Logger
}

func (a *Admin) mount(mux *http.ServeMux) {
    // Each handler is wrapped by the operator guard: deny-by-default.
    h := func(fn http.HandlerFunc) http.Handler { return a.guard.Protect(fn) }
    mux.Handle("POST /admin/flows",                 h(a.createFlow))
    mux.Handle("POST /admin/flows/{id}/publish",    h(a.publishFlow))
    mux.Handle("POST /admin/flows/{id}/rollback",   h(a.rollbackFlow))
    mux.Handle("POST /admin/flows/validate",        h(a.validateFlow))
    mux.Handle("POST /admin/flows/dry-run",         h(a.dryRunFlow))
    mux.Handle("POST /admin/jdms",                  h(a.createJDM))
    mux.Handle("POST /admin/connections",           h(a.createConnection))
    mux.Handle("GET  /admin/connections",           h(a.listConnections))
    mux.Handle("GET  /admin/audit/{type}/{id}",     h(a.auditTrail))
}
```

Go 1.22 method-aware patterns (`"POST /admin/flows"`, `"GET /admin/audit/{type}/{id}"`) are used so
method dispatch and path-variable capture (`r.PathValue("id")`) come from the stdlib mux — no
third-party router, no hand-rolled matcher on the admin plane (the data-plane catch-all keeps its
own matcher because it must resolve against *live config*, which the static mux can't do).

Precedence note: `POST /admin/flows/validate` and `POST /admin/flows/{id}/publish` both live under
`/admin/flows/`. ServeMux resolves `/admin/flows/validate` to the **literal** pattern (more
specific than `{id}/publish`), so "validate" is never captured as an `{id}`. This is the standard
Go 1.22 precedence rule; a unit test pins it (§7).

### 2.1a Wiring: `Deps`, `NewHandler`, and the one-store-two-interfaces fact

The admin surface is wired through the existing `httpapi.Deps` + `NewHandler`, with two additive
`Deps` fields (flagged in §8):
```go
type Deps struct {
    // ...existing: Conns, Decide, Trace, Log, Store (Ping-only), Metrics...
    Admin   AdminStore                 // §2.2 — admin method-set; nil => admin writes disabled
    OperAuth auth.OperatorAuthenticator // §3 — nil => operator plane mount-closed
}
```
`NewHandler(store Store, interp *flow.Interpreter, deps Deps)` keeps its signature. Inside it, after
mounting ops, it builds `admin := newAdmin(interp, deps)` and calls `admin.mount(mux)` **before** the
`"/"` catch-all. `newAdmin` reads `deps.Admin` (the admin method-set), `deps.OperAuth` (to build the
`OperatorGuard`), and reuses `deps.Conns/Decide/Trace/Log` for dry-run reads.

**One concrete store satisfies both interfaces.** `cmd/engine` builds exactly one `*config.PgStore`
and passes the *same instance* twice: as the `store Store` argument (hot-path: `ActiveFlow`+
`ActiveRoutes`, used by `genericFlowHandler`) and as `deps.Admin` (the `AdminStore` method-set).
`*config.PgStore` structurally satisfies both (verified: it has every method of both interfaces).
No frozen-seam change — `AdminStore` is an admin-package interface satisfied by the concrete store
(DIP). In in-memory mode, `cmd/engine` passes the `*memStore` as `store` and leaves `deps.Admin ==
nil` (memStore lacks the admin method-set), so admin writes return the "requires config-store mode"
error (§2.2); the data plane still serves.

### 2.2 The admin store method-set (seam boundary)

The handlers depend on a narrow interface, not on `*config.PgStore` directly, so they stay testable
with a fake and the dependency is explicit (DIP, `[[low-level-design]]`):

```go
type AdminStore interface {
    // on the frozen Store seam:
    PutFlowVersion(ctx, env string, f config.FlowVersion) (version int, err error)
    SetActive(ctx, env, flowID string, version int) error
    Connections(ctx, env string) ([]connect.ConnectionDef, error)
    GetJDM(ctx, env, id string) (jdm []byte, version int, err error)
    ActiveFlow(ctx, env, method, path string) (config.FlowVersion, error) // for validate ref-resolution
    // store-internal (not on the Store seam; concrete PgStore exposes them):
    PutJDMVersion(ctx, env, jdmID string, doc []byte, version int) (int, error)
    PutConnectionVersion(ctx, env string, def connect.ConnectionDef) (int, error)
    MarkValidated(ctx, env, flowID string, version int) error
    AuditTrail(ctx, env, objectType, objectID string) ([]config.AuditEntry, error)
}
```

`*config.PgStore` already satisfies all of these (verified in `pgstore.go`). The in-memory
`memStore` does **not** implement the admin set; admin routes are therefore only meaningfully
functional in config-store mode. In in-memory mode the admin surface mounts but every write returns
a classified `Internal`/`Validation` "admin requires config-store mode" — see §2.9. (Rationale:
admin is a Postgres-backed control plane; the seed-only in-memory mode is a dev convenience.)

### 2.3 `POST /admin/flows` → `PutFlowVersion`

Create a new immutable flow version (lands `validated=false`). The version number is assigned by
the store (`max+1`), **not** by the client, so the request's `version` field is ignored/rejected.

Request:
```json
{
  "flowId": "orders",
  "method": "GET",
  "path": "/orders/{id}",
  "tree": { "...Node tree (flow.Node JSON)..." },
  "fixtures": [ { "name": "...", "input": {}, "mocks": {}, "expect": {} } ],
  "note": "optional author why"
}
```
Validation (request-shape, before touching the store): `flowId`, `method`, `path`, `tree` required;
`method` ∈ a known HTTP-method set; `tree` is decoded into `flow.Node` and run through
`flow.ValidateTree` with a `RefResolver` backed by the store (structural + dangling-ref check, §4.1)
— a malformed tree is a `400` with the collected issues, so a bad flow never reaches Postgres.

Response `201 Created`:
```json
{ "flowId": "orders", "version": 7, "validated": false }
```
Status map: `201` on success; `400` on bad request shape or tree validation; `409` on a route
`(method,path)` already owned by a *different* flowId — **but this requires a store-internal fix**,
see the next paragraph; `502/504` on store `Upstream/Timeout`; `500` on `Internal`.

**Route-collision 409 — store fix required (verified against source).** The `flows` table has
`UNIQUE (method, path)` (`slice-d` schema), but `PutFlowVersion`'s `INSERT INTO flows … ON CONFLICT
(id) DO NOTHING` only guards the `id` conflict; a different flowId claiming an owned `(method,path)`
raises a Postgres unique violation (SQLSTATE `23505`). `classifyPg` (`pgstore.go`) wraps any
non-`42xxx` pg error as **`Upstream`** — so today a route collision is **indistinguishable at the
HTTP layer from a real outage** (it would report `502`). The review's hedged "409 or fall back to
502" is therefore wrong to keep. **Decision:** add a store-internal tweak so the conflict surfaces
as a typed `config.Validation` (or a dedicated `ErrRouteConflict` sentinel): in `classifyPg`, detect
`pgconn.PgError.Code == "23505"` and the `flows_method_path_key` constraint name and wrap it as
`Validation` with a stable "route_conflict" marker. The admin edge then maps that marker
deterministically to `409`. This is a `internal/config` change flagged in §8. Until it lands, a
route collision reports `502` (honest "store rejected the write" — not silently wrong, just coarse);
the 409 is contingent on the store tweak and the design does not pretend otherwise.

Note: creating a version does **not** publish it. The author then calls validate (§2.6) + publish
(§2.4). `created_by`/actor is taken from the authenticated operator principal (§3), not from the
body — the body never sets the audit actor.

### 2.4 `POST /admin/flows/{id}/publish` → `SetActive`

Advance the active pointer for flow `{id}` to a specific version. `SetActive` refuses an
un-validated version (publish-blocking, AC-13), so publishing a never-validated version returns
`422 Unprocessable Entity` (mapped from the store's `Validation` "cannot publish un-validated"
case — see §2.9 for why this one maps to 422 not 400).

Request:
```json
{ "version": 7, "reason": "optional" }
```
`version` required, positive int. Response `200`:
```json
{ "flowId": "orders", "activeVersion": 7, "action": "publish" }
```
Calls `store.SetActive(ctx, env, id, version)`. The store writes the audit row (recording its own
publish-vs-rollback determination) and fires cache invalidation after commit (all existing
behavior). `{id}` comes from `r.PathValue("id")`.

**`action` is derived by the handler, not returned by the store (verified).** `SetActive(ctx, env,
flowID, version) error` returns **only an error** (`pgstore.go`) — the publish/rollback
classification is computed internally and written to `audit_log`, never returned. So the handler
cannot "echo the store's action". The route itself already conveys intent (`/publish` vs
`/rollback`), so **the handler sets `action` from the route** (`"publish"` on this route). Optionally,
to report the *effective* action truthfully, the handler reads the current active version first via
`store.AuditTrail`/a pointer read and compares — but that is a nice-to-have; **the chosen v1 behavior
is route-derived `action`** (simplest, no extra read, no new seam). The `action` field is thus a
restatement of which route was called, documented as such.

### 2.5 `POST /admin/flows/{id}/rollback` → `SetActive`

Semantically "move the pointer to an earlier version". It calls the **same** `SetActive` method —
the store classifies it internally as `rollback` when `version < current` (`pgstore.go`) and records
it in the audit log. We keep a distinct route (not just "publish an older version") because the
control-plane vocabulary and the audit intent differ, and Strapi/operators think in
publish-vs-rollback terms. Request/response identical to publish; the response `action` reads
`"rollback"` because this is the rollback route (**route-derived**, per §2.4 — the store does not
return the action).

Edge case: rollback to a version `>=` current. The store records it internally as `publish` (pointer
moves forward or stays) in the audit log, even though the operator hit `/rollback`. The response
`action` still reads `"rollback"` (route-derived); the **audit log** is the source of truth for what
actually happened. We **do not** reject this at the edge. If precise effective-action reporting is
wanted, the optional current-pointer read noted in §2.4 overrides the route-derived label —
deferred. Documented, not an error.

### 2.6 `POST /admin/flows/validate` → structural + fixtures (publish-blocking, always 200)

Validate a candidate flow version **without persisting it active**. This is stateless w.r.t. the
active pointer but MAY persist: see the two modes below. Always returns HTTP `200` with an `ok`
boolean body — a failing validation is a *successful* validation call returning a negative result,
never a 4xx/5xx (per `slice-d` §7 and `[[error-classification]]`). Only transport/engine faults are
non-200.

**Mode discriminator (precise rule).** The two modes are selected by exactly these fields, with
`version` as the tie-breaker so there is no ambiguity when both are present:
- **Stored mode** — `flowId` present AND `version` a **positive int**. The handler loads that stored
  version's tree (requires a store read — see below), validates it, and on all-pass calls
  `MarkValidated(flowId, version)`. An inline `flow` object, if also present, is **ignored** in this
  mode (the stored version wins; `version` is the explicit discriminator). A `version` that resolves
  to **no stored version** is a `404` (this is the one non-200 validate case: a request to validate a
  thing that doesn't exist is a bad request, not a negative result).
- **Candidate mode** — otherwise (`flow` object present, `version` absent/zero). Validate the inline
  candidate statelessly; **never** `MarkValidated`.
- Neither a usable `flow` object nor a `flowId`+`version` ⇒ `400`.

Loading a stored version's tree needs a read the frozen `Store` seam does not expose by version
number (`ActiveFlow` resolves only the *active* version). **Decision:** add a store-internal
`GetFlowVersion(ctx, env, flowID string, version int) (FlowVersion, error)` (one PK read of
`flow_versions` + its fixtures; `NotFound` when absent) — flagged in §8. Stored-mode validate and any
future "inspect version N" admin read use it. (Alternative rejected: overloading `ActiveFlow` — it is
hot-path + active-only.)

Request (candidate mode):
```json
{
  "env": "dev",
  "flow": { "flowId": "orders", "method": "GET", "path": "/orders/{id}",
            "tree": { "...flow.Node..." }, "fixtures": [ { "...AdminFixture (see §4.2)..." } ] }
}
```
Request (stored mode):
```json
{ "env": "dev", "flowId": "orders", "version": 7 }
```

Behavior:
1. **Structural validation** via `flow.ValidateTree(fv.Tree, refResolver)` (`fv.Tree` is the
   `flow.Node` root; `ValidateTree(root Node, refs RefResolver)`) where `refResolver` answers
   `HasConnection(key)` / `HasJDM(id)` against the store (§4.1). Collect-all, not fail-fast.
2. **Fixture replay** (§4.2): run each fixture through `interp.Run` in a mocked-source harness
   (zero real I/O), asserting the fixture's declared `expect`.
3. `ok = (no structural issues) && (every fixture passed)`. In stored mode, `ok==true` ⇒
   `MarkValidated`; `ok==false` ⇒ no state change (publish stays blocked).

Response `200`:
```json
{
  "ok": false,
  "structural": [ { "nodeId": "action.x", "code": "dangling_connection", "message": "connection \"foo\" not found" } ],
  "fixtures": [
    { "name": "paid order", "passed": true },
    { "name": "unpaid is 402", "passed": false, "diff": { "field": "...", "expected": "...", "actual": "..." } }
  ]
}
```
`structural[].code` is the stable `flow.ValidationIssue.Code` (machine key for Strapi UI).

### 2.7 `POST /admin/flows/dry-run` → full trace, writes suppressed (AC-14)

Run a flow and return the node-by-node trace with **writes suppressed**. Request:
```json
{
  "env": "dev",
  "flowId": "orders",
  "version": 7,
  "input": { "method": "GET", "path": "/orders/42", "params": { "id": "42" }, "body": {}, "headers": {} },
  "mocks": { "conn:fmc-pg": { "query:...": { "...": "..." } } }
}
```
`version` optional (defaults to the active version via `ActiveFlow`). `mocks` optional — omitted
means **reads hit real sources** (via the wired `deps.Conns`), writes are always suppressed.

Behavior: build `ctx` with `observ.WithDryRun(ctx)` **and** `observ.WithCollector(ctx, collector)`,
build a `flow.Ctx` from `input.params` (same lifting the data-plane edge does), run `interp.Run`.
The action handler, with the §4.3 fix, skips write ops and records `wrote:"suppressed"`. No pointer
change, no audit row, no cache write (dry-run calls no store write method).

**Trace fidelity — honest scope (verified).** The collector only receives a node record where
something calls `collector.Record` (directly or via `observ.TraceNode`). **Verified: the interpreter
(`internal/flow/interpreter.go`) does NOT currently wrap node execution in `observ.TraceNode`**, so
the collector gets **no automatic per-node records** today. Two options:
- **v1 (chosen, minimal):** the §4.3 fix explicitly records the **suppressed-write** node(s). So the
  v1 dry-run `trace` reliably contains the suppressed write(s) (the AC-14-critical evidence) and the
  final `response`; it does **not** yet contain a full per-node walk. This matches the known
  STATE.md limitation ("the dry-run trace records only the suppressed write node — full per-node
  TraceNode integration is a later increment"). The sample below is therefore trimmed to what v1
  actually emits.
- **optional stretch:** wrap each `NodeHandler.Exec` call in the interpreter with `observ.TraceNode`
  (feeding `NodeOutcome{Branch, Err}`), which populates a full per-node trace for free. This is a
  clean `internal/flow` change but larger than the AC-14 fix; **deferred** unless the reviewer wants
  full-trace dry-run in this slice. Called out as a decision (D5).

Response `200` (v1 fidelity — suppressed writes + response; not yet a full per-node walk):
```json
{
  "trace": [
    { "nodeId": "action.write", "type": "action", "wrote": "suppressed", "durationMs": 0 }
  ],
  "response": { "...stitched Ctx.Response..." },
  "errors": []
}
```
If the flow run returns a classified error, dry-run still returns `200` with that error summarized
in `errors` (dry-run is a debugger; a flow error is a *result* to show the author, not a transport
fault). A bad request shape (missing flowId AND no resolvable active flow) is a `400`.

### 2.8 `POST /admin/jdms` → `PutJDMVersion`; `POST`/`GET /admin/connections`; `GET /admin/audit/{type}/{id}`

**`POST /admin/jdms`** — create a JDM version and point the active JDM pointer at it (the store's
`PutJDMVersion` both inserts and activates; JDMs have no separate validate gate). Request:
```json
{ "jdmId": "order-decision", "doc": { "...GoRules JDM graph..." }, "version": 0, "note": "..." }
```
`doc` is accepted as raw JSON and passed as bytes to `PutJDMVersion`; `version<=0` ⇒ auto-assign.
A `doc` that is empty/absent is a `400`. Response `201`: `{ "jdmId": "...", "version": 3 }`.

**`POST /admin/connections`** — register/replace a connection def (a new immutable version +
activate). The def **points at an existing DB/REST**; the engine never provisions. Request:
```json
{
  "key": "fmc-pg",
  "type": "postgres",
  "settings": { "host": "...", "port": "5432", "database": "fmc_utility", "sslmode": "disable",
                "pool": { "maxConns": 8 } },
  "secretRef": "env:FMC_PG_PASSWORD",
  "resilience": { "timeoutMs": 2000, "retry": {...}, "breaker": {...} }
}
```
**Secret rule (hard):** the request carries `secretRef` ONLY. A request body containing any
secret-value-looking field (`password`, `secret`, `token`, `apiKey`, `settings.password`, …) is
**rejected `400`** before the store is touched — the admin edge enforces "`secret_ref` only, never
a value" (`[[config-and-secrets]]`, AC-20). `settings`/`resilience` are passed through to
`PutConnectionVersion` as-is; the store already stores `secret_ref` only. Response `201`:
`{ "key": "fmc-pg", "version": 2 }`.

**`GET /admin/connections`** — list active connection defs via `store.Connections(env)`. The
response **must redact**: `secretRef` is returned (it is a non-secret pointer), but the handler runs
the whole response object through `observ`'s central `Redactor` as a backstop so no accidental
secret field leaks. Response `200`:
```json
{ "connections": [ { "key": "fmc-pg", "type": "postgres", "settings": {...}, "secretRef": "env:FMC_PG_PASSWORD", "resilience": {...} } ] }
```

**`GET /admin/audit/{type}/{id}`** — read the audit trail via `store.AuditTrail(env, type, id)`.
`{type}` ∈ `{flow, jdm, connection}` (validated against the known set; anything else is `400`).
Response `200`:
```json
{
  "objectType": "flow", "objectId": "orders",
  "entries": [
    { "action": "publish", "fromVersion": 6, "toVersion": 7, "actor": "op:alice", "at": "2026-10-03T...Z", "reason": "" },
    { "action": "create_version", "toVersion": 7, "actor": "op:alice", "at": "...", "reason": "" }
  ]
}
```
Newest-first (store already orders `at DESC, id DESC`). `fromVersion`/`toVersion` are nullable
(encode as JSON `null` when the store's `*int` is nil).

### 2.9 Error → HTTP status mapping (admin plane)

Classification is by `errors.Is`/`errors.As` against the `config.ConfigError` taxonomy, never
string-match (consistent with `statusForConfig`/`statusForFlow` already in `server.go`). A new
`statusForAdmin(err) (int, string)` in `admin.go`:

| Store/edge class | HTTP | Client body `error` | Notes |
| --- | --- | --- | --- |
| request-shape invalid (edge) | 400 | "invalid request: <field>" | before store touched |
| tree structural invalid (edge, create) | 400 | "flow tree invalid" + `issues[]` | from `ValidateTree` |
| secret value present in connection body | 400 | "secret value not allowed; use secretRef" | AC-20 guard |
| `config.Validation` "publish un-validated" | 422 | "flow version not validated" | publish-blocking (AC-13) |
| other `config.Validation` | 400 | "invalid request" | |
| `config.NotFound` | 404 | "not found" | missing flow/version/object |
| route `(method,path)` conflict | 409 | "route already owned by another flow" | **contingent on the §2.3 store fix** that surfaces SQLSTATE `23505` on `flows_method_path_key` as a typed `Validation`/route_conflict marker; until then this reports `502` (see §2.3) |
| `config.Timeout` | 504 | "store timeout" | |
| `config.Upstream` | 502 | "store unavailable" | Postgres down |
| `config.Internal` / unclassified | 500 | "internal error" | only true faults |

validate (§2.6) and dry-run (§2.7) are the exceptions: a negative *result* is `200` with a body,
not a 4xx. Only a malformed request or a transport fault makes them non-200. The client body is
deliberately coarse (no internal detail); the full classified error is logged at the seam
(`observ.Logger`), never the token or any secret (§3, `[[config-and-secrets]]`).

**Request `env` rule (exact).** The engine process serves a single env — the empty string `""`
(`httpapi.defaultEnv`), ADR-006. For any admin request carrying an `env` field: **absent ⇒ treated
as `""` (ok); `env == ""` explicitly ⇒ ok; any non-empty `env` ⇒ `400`** ("engine serves env \"\"
only"). This removes the ambiguity of "equal to the served env" — an explicit empty string is
accepted, a non-empty mismatch is rejected before the store is touched.

---

## 3. Privileged operator auth — separate from the public JWT path, deny-by-default

### 3.1 Why a separate mechanism (not a role on the public JWT)

The public data plane authenticates end-user API callers against a rotating **JWKS** (`auth.
jwksVerifier`, issuer/audience/roles claim). The control plane authenticates **operators / the
Strapi service account** — a different trust domain, a different credential lifecycle, and a
different blast radius (it can rewrite every API). Overloading the public JWT with an `admin` role
would couple the two: a compromise or misconfiguration of the public issuer would reach the control
plane, and the public issuer's audience/claims model would dictate operator access. Per
`[[security-and-authz]]` (least privilege, separation of trust domains) the operator plane gets its
**own** credential path, wired from its **own** config keys, and is **off unless explicitly
configured**.

### 3.2 Mechanism: `auth.OperatorGuard`

A new, small, self-contained guard in `internal/auth/operator.go`, independent of
`jwksVerifier`/`Middleware`:

```go
// OperatorAuthenticator validates an operator credential and yields an operator
// identity. It is a DISTINCT seam from Authenticator (the public JWT path).
type OperatorAuthenticator interface {
    AuthenticateOperator(ctx context.Context, r *http.Request) (Operator, error)
}
type Operator struct { Subject string; Roles []string } // e.g. {"op:alice", ["flow.publish","flow.write"]}

// OperatorGuard is the deny-by-default middleware wrapping every /admin route.
type OperatorGuard struct {
    authn   OperatorAuthenticator // nil => plane disabled (mount-closed)
    require func(r *http.Request) (role string) // route -> required role (RBAC)
    log     observ.Logger
}
func (g *OperatorGuard) Protect(next http.HandlerFunc) http.Handler
```

The disabled-plane check is `g.authn == nil` — a nil interface, decided in `cmd/engine` from
`ADMIN_ENABLED`/`ADMIN_TOKENS`. **No `Count()` method on the interface is needed** (the review's
earlier `operatorAuth.Count()==0` guard is removed): `cmd/engine` constructs
`*StaticTokenOperatorAuth` only when `ADMIN_ENABLED=true` AND at least one valid token parsed; if
`ADMIN_ENABLED=true` but zero valid tokens, that is a **fatal startup error** (§3.3 malformed-config
rule — "enabled but empty allow-list" never boots). When the plane is off, `cmd/engine` passes a
`nil` `OperatorAuthenticator`, and `OperatorGuard.Protect` fails closed:

1. If `g.authn == nil` → the operator plane is **not configured** → every `/admin/*` request is
   `503 {"error":"admin plane disabled"}`. **Mount-closed, never mount-open.** (Deny-by-default
   taken literally: absence of config is denial, not a bypass. This is the opposite of the public
   chain's "bypassed when unconfigured" convenience, and the difference is deliberate — the control
   plane must never be reachable unauthenticated.)
2. `authn.AuthenticateOperator(ctx, r)` fails → `401`, generic body, detail logged (never the
   credential). 
3. RBAC: `role := g.require(r)`; if the operator's `Roles` does not include `role` (and `role != ""`)
   → `403`, logged as a security event with subject + resource + action, never the credential.
4. Otherwise inject the `Operator` into the request context (so handlers read the audit actor from
   it) and call `next`.

The injected `Operator.Subject` is threaded to the store as the **audit actor** via a per-request
`PgStore` actor. The store's actor is currently a struct field set once at construction
(`WithActor`). To attribute each admin write to its operator without reworking the store seam, the
admin handler sets the actor on a **per-request basis** — see §3.5.

### 3.3 Credential scheme (v1) — static operator tokens, pluggable

v1 operator credential is a **bearer operator token** matched against a configured allow-list
(hashed), each token mapped to a subject + role set. This is intentionally simple and dependency-
free (no new dep, no second JWKS), and it is exactly what a server-to-server caller (Strapi service
account, `curl`, Postman) needs now. The `OperatorAuthenticator` interface is the seam, so a later
increment can swap in mTLS client-cert auth or a separate operator OIDC issuer without touching any
handler.

Config keys (read in `cmd/engine`, new, operator-plane-only, never the public JWT keys):
```
ADMIN_ENABLED=true                 # explicit opt-in; unset/false => plane disabled (mount-closed)
ADMIN_TOKENS=<sha256hex>:op:alice:flow.write,flow.publish;<sha256hex>:op:strapi:flow.write,...
```
`ADMIN_TOKENS` is a `;`-separated list of `tokenSHA256 : subject : comma-roles`. The engine stores
only the **SHA-256 hash** of each token; the plaintext token is never stored, never logged
(`[[config-and-secrets]]`). A request presents `Authorization: Bearer <operator-token>`. The admin
middleware extracts the bearer by calling a **newly-exported** `auth.BearerToken(r *http.Request)
(string, bool)` — today `auth.bearerToken` is unexported and unreachable from `internal/httpapi`
(verified), so this slice **exports it** (rename `bearerToken`→`BearerToken` in
`internal/auth/middleware.go`, flagged in §8) to keep a single RFC-7235 scheme parser rather than
duplicating one. The header space is shared with the public plane, but the two planes mount on
disjoint routes (`/admin/*` vs the catch-all), so there is no ambiguity: an operator token presented
to a data-plane route is just an invalid data-plane JWT and vice-versa.

**Malformed-config is fail-fast at startup (deny-by-default posture).** `ADMIN_ENABLED=true` with an
`ADMIN_TOKENS` entry that is malformed (not three `:`-separated parts, a non-hex / wrong-length hash,
an empty subject, or a duplicate subject) makes `cmd/engine` **exit non-zero at startup** — a bad
operator-auth config must never silently drop an entry and come up with a weaker allow-list. The
parse happens in the `StaticTokenOperatorAuth` constructor (`internal/auth`), which returns an error
that `cmd/engine` surfaces as a fatal boot error.

Constant-time compare (`crypto/subtle` on the fixed-width SHA-256 hashes) on the credential match to
avoid a timing oracle (`[[security-and-authz]]`).

### 3.4 RBAC map (`require`)

A tiny route→role function supplied by the composition root, so `auth` carries no routing
knowledge (same pattern as `ResourceActionFunc`):

| Route | Required role |
| --- | --- |
| `POST /admin/flows`, `POST /admin/jdms`, `POST /admin/connections` | `flow.write` |
| `POST /admin/flows/{id}/publish`, `/rollback` | `flow.publish` |
| `POST /admin/flows/validate`, `/dry-run` | `flow.read` (validate/dry-run are side-effect-free* ) |
| `GET /admin/connections`, `GET /admin/audit/...` | `flow.read` |

*validate in stored mode calls `MarkValidated` (a write to the `validated` flag). We still gate it
on `flow.read` because it cannot change what *runs* (only `publish` moves the pointer); marking
validated is a prerequisite the author owns. If an operator shop wants stricter separation, the map
is one line to change — called out as a tunable, not a hidden decision.

### 3.5 Audit actor attribution (per-request)

The store records `created_by`/`actor` from `PgStore.actor` (set at construction via `WithActor`,
default `"engine"`). To attribute each admin mutation to the operator who made it, the admin handler
needs a store whose actor is the request's operator subject. Two options:

- **(A)** Add an actor argument to each write method — rejected: churns the frozen seam and every
  caller for a cross-cutting concern.
- **(B)** Give `PgStore` a lightweight `WithActorContext` seam: store reads actor from `ctx` when
  present, else falls back to the construction-time actor. **Chosen.** It is additive (a new
  `config.WithActor(ctx, subject)` context helper + a one-line read in the store's audit inserts),
  does not touch any method signature, and mirrors the existing `env-on-context` / `dry-run-on-
  context` conventions the codebase already uses. The admin handler calls
  `ctx = config.WithAuditActor(ctx, operator.Subject)` before invoking the store method.

This is a **requested seam addition** (see §8) scoped to `internal/config` (actor-on-context) — the
admin slice owns the httpapi/auth code; the one-line store change is flagged for the stitch/owner of
`internal/config`. If the owner prefers to keep the construction-time actor for v1, the fallback is
acceptable: all admin writes attribute to a single `"admin"` actor and the operator subject is
captured in the `observ` audit log line instead (degraded, but not wrong). Design default is (B);
the degraded path is the documented fallback.

### 3.6 What is never logged or echoed

The operator token (plaintext or hash), any `Authorization` header value, any connection secret
value, and the public JWT are **never** logged or returned. Audit log lines carry subject, resource,
action, outcome, and `error_class` only — reusing the existing redacting `observ.Logger` and the
`deny401`/`deny403` patterns from `middleware.go`. The connection-list response is passed through
the central `Redactor` as a backstop (§2.8).

---

## 4. Validate & dry-run internals

### 4.1 RefResolver backed by the store

`flow.ValidateTree(root Node, refs RefResolver)` (the actual signature; `root` is `fv.Tree`, a
`flow.Node`) needs `HasConnection(key)`/`HasJDM(id)`. A new adapter in `admin.go`:

```go
type storeRefs struct { ctx context.Context; store AdminStore; env string }
func (s storeRefs) HasConnection(key string) bool { /* Connections(env) contains key */ }
func (s storeRefs) HasJDM(id string) bool         { /* GetJDM(env,id) != NotFound */ }
```
`HasConnection` reads `store.Connections(env)` once per validate call and checks membership (small
list; cache-fronted). `HasJDM` calls `store.GetJDM(env,id)` and treats a `NotFound` as absent, any
other error as "assume present" (don't fail validation on a transient store blip — structural
checks are the point; a dangling-ref false-negative under store failure is less harmful than
blocking the author). Note: refs are resolved against the **active** JDM/connection set; a flow that
references a JDM created-but-not-yet-active would show a dangling-ref. Documented limitation —
validate-with-pending-refs is a Strapi-orchestration concern (create JDM+activate, then validate the
flow), not a v1 engine feature.

### 4.2 Fixture type — the stored `config.FlowFixture` is too thin; use an admin-local richer type

**Verified gap:** the persisted `config.FlowFixture` is `{ Name string; Input map[string]any; Want
map[string]any }` (`internal/config/store.go`) — it has **no `Mocks` field and no structured
`Expect`**. The mocked-source replay the design needs cannot be driven by that type as-is.

**Decision (option b from the review): keep `config.FlowFixture` frozen; define an admin-request-
local fixture type in `internal/httpapi`** used for the validate/dry-run request shapes:

```go
// AdminFixture is the richer fixture the admin plane accepts on the wire. It is
// NOT the persisted config.FlowFixture (which stays {Name,Input,Want}).
type AdminFixture struct {
    Name   string                       `json:"name"`
    Input  map[string]any               `json:"input"`
    Mocks  map[string]map[string]any    `json:"mocks"`  // "conn:{key}" -> "op:{name}" -> result
    Expect struct {
        Output     map[string]any `json:"output,omitempty"`
        BranchPath []string       `json:"branchPath,omitempty"`
        Errors     []string       `json:"errors,omitempty"`   // classified error labels
    } `json:"expect"`
}
```

Rationale for option (b) over (a) widening `config.FlowFixture`: the frozen `config.FlowVersion`/
`FlowFixture` is the **persisted** contract and the schema's `flow_fixtures.body` already serializes
`{Name,Input,Want}`; widening it is a schema+contract change for a surface that is mostly an
**in-flight validation payload**, not persisted state. The admin plane validates with the richer
type at request time; when a candidate flow is persisted via `PutFlowVersion`, its `fixtures` are
down-mapped to `config.FlowFixture` (name+input preserved; the structured `Expect.Output` maps to
`Want`; `Mocks` is validation-time-only and not persisted). This keeps the persisted contract
unchanged (gate criterion 7) while giving validate the inputs it needs.

Replay (per `AdminFixture`):
1. Build a `flow.Ctx` from `fixture.Input` (params/body/headers), as the data-plane edge does
   (`flow.NewCtx` + lift params).
2. Build a **mock registry**: an admin-package type implementing the frozen `connect.Registry` seam
   whose `Client(key)` returns a `connect.Client` answering from
   `fixture.Mocks["conn:"+key]["op:"+opName]` with **no real I/O** (deterministic). It implements the
   public `connect.Registry`/`connect.Client` interfaces only — it does not reach into the real
   `connect` package internals.
3. Attach a `TraceCollector` (`observ.NewTraceCollector` + `observ.WithCollector`) so the branch
   sequence is recorded, then `interp.Run(ctx, &fv.Tree, ver, c, depsWithMockRegistry)`.
4. Assert the declared `Expect`: `Output` (deep-equal vs `c.Response`), `BranchPath` (vs the
   collector's recorded branch sequence), `Errors` (classified error labels present). Each unmet
   expectation becomes a `diff` entry. All `Expect` sub-fields are optional; an empty `Expect`
   asserts only "runs without a classified error".

### 4.3 Dry-run write-suppression fix (in-scope correctness fix to `internal/flow/handlers.go`)

**Root cause:** `actionHandler.Exec` never consults the dry-run flag, so an action's write op runs
for real even under `observ.WithDryRun`. AC-14 is currently unmet. The dead `flow/dryrun.go` helpers
(`isDryRun`, `isWriteOp`) were a shim for a seam that has since landed in `observ`.

**Fix:** in `actionHandler.Exec`, before `client.Execute(ctx, op)`:
```go
if observ.IsDryRun(ctx) && isWriteOp(op.Kind) {
    // Record a suppressed write into the trace collector (if attached) and skip the I/O.
    if tc, ok := observ.CollectorFrom(ctx); ok {
        tc.Record(n.ID, string(n.Type), "", map[string]any{"wrote": "suppressed"})
    }
    return walkChildren(ctx, n.Children, c, dep, w) // linear continue, no SaveAs
}
```
Changes:
- `internal/flow/dryrun.go`: delete the package-local `isDryRun`/`withDryRun` (the dead shim); keep
  `isWriteOp(kind string) bool` (still the single source of "is this op a write"). Replace the stale
  TODO comment.
- `internal/flow/handlers.go`: add the suppression branch above; import `internal/observ` (already a
  transitive dep via `Deps`, no cycle — `observ` does not import `flow`).
- A write op under dry-run writes nothing to `c.Data` under `SaveAs` (there is no result). A
  downstream node that reads that `SaveAs` sees it absent — this is the known "dry-run of a flow
  whose Set consumes a suppressed write's body reports a Validation" caveat already documented in
  STATE.md; unchanged, acceptable.

`isWriteOp` kinds: reads = `query/get/ping`; writes = `exec/set/del/http` (the existing definition).
`http` is treated as a write (a POST/PUT has side effects); a pure GET through the `http` kind would
be over-suppressed, but the thin slice's write-vs-read is op-kind-based and the data plane uses
`query` for reads, so this is correct for current flows. A finer read/write split on the `http` op
(by HTTP method) is a documented follow-up, not needed here.

This is a `flow`-package edit; per the merge-contention rule each worktree edits its own packages,
and this slice legitimately owns the dry-run completion since it ships the only consumer. Flagged in
§8 so the stitch step is aware.

---

## 5. Bug fix #1 — `/readyz` 503 false-negative

### 5.1 Root-cause hypothesis

`Ops.readyz` (`internal/httpapi/ops.go`) gates readiness on **`registry.HealthCheck(ctx)`** AND
`store.Ping(ctx)`. `registry.HealthCheck` (`internal/connect/registry.go`) fans out a `ping` op to
**every live connection**, including the **data-source** connections — e.g. `fmc-pg`, which points
at the user's **external** `fmc_utility` Postgres. The readiness contract is "can this engine
**serve**?" The engine serves config-driven flows out of its **own** config store (+ cache); it does
**not** require every arbitrary downstream data source to be reachable at probe time to be "ready" —
a down data source should fail the specific request that needs it (and surface as `502/504`), not
take the whole instance out of rotation.

So a healthy engine against a reachable config store returns `503` the moment any data-source probe
fails: the external `fmc_utility` DB is briefly unreachable, or — more subtly — the REST `ping`
does a `GET {baseURL}/` and treats any `>=500` as unhealthy (and a connection error as Upstream),
so a data-source REST endpoint without a healthy root path fails readiness even though no flow
needs its root path. That is the false-negative.

### 5.2 Corrected readiness criterion

**`/readyz` is 200 when the engine can serve: the config store is reachable (`store.Ping`) and the
cache, if configured, is reachable.** Data-source connection health is **removed** from the
readiness gate.

Concretely, change `Ops.readyz`:
- **Keep:** `store.Ping(ctx)` — the config store is the thing the engine *must* reach to resolve any
  route. Unreachable ⇒ genuinely not ready ⇒ `503 {"notReady":"config_store"}`.
- **Remove:** the blanket `registry.HealthCheck(ctx)` gate over *all* connections.
- **Optionally add (narrow):** a cache reachability probe when a cache is wired, since a cache-down
  engine still serves (degrade-to-store) — so cache-down is **ready** (maybe `200` with a
  `"degraded":"cache"` note), **not** `503`. The cache is an optimization, never the readiness
  source of truth (`[[caching-strategy]]` defined cache-down behavior). v1: do **not** gate on
  cache at all; readiness == config store reachable. (A degraded-but-serving note is a nice-to-have,
  deferred.)

Rationale grounded in standards: readiness reflects "can serve", and the engine's serve-dependency
is its config store, not its data sources (`[[config-driven-boundaries]]` — a data source is config
the engine *uses per request*, not a liveness dependency of the control/resolve path). Liveness
(`/livez`) stays unconditional 200.

### 5.3 What about data-source health?

Data-source reachability is still observable — but as **per-request** behavior (a flow hitting a
down source returns a classified `Upstream/Timeout` → `502/504`, already implemented) and via
`/metrics` (RED metrics on the connection). If an operator *wants* a deep health view, a **separate**
`GET /admin/health/connections` (operator-gated) can expose `registry.HealthCheck` per-connection
results as an informational, non-gating diagnostic. Proposed as an optional admin diagnostic, not
part of the readiness gate. (Decision: readiness != dependency-dashboard.)

### 5.4 Edge cases

- Config store briefly slow: `readyz` already runs under a 2s bounded timeout (`readyzTimeout`); a
  store `Ping` that exceeds it ⇒ `503` (correct — the engine can't resolve routes). Unchanged.
- In-memory mode: `deps.Store` is a `*memStore` wrapped so `Ping` returns nil (in-memory is always
  reachable) ⇒ `readyz` is `200`. Verify the in-memory store exposes a nil-returning `Ping` (it does
  via the `Deps.Store` interface which only requires `Ping`); if not, the gate is skipped when
  `store==nil`. Confirmed by `newOps`: a nil store skips the gate.

---

## 6. Bug fix #2 — per-flow / per-object idempotent seeding

### 6.1 Root-cause hypothesis

`SeedPgStore` (`internal/config/seed_pg.go`) uses an **all-or-nothing** idempotency gate: it
resolves `ActiveFlow(firstFlow.method, firstFlow.path)`; if that one flow is already active, it
returns `(false, nil)` and writes **nothing** — so a brand-new flow added to `seed.json` is silently
skipped on restart against an already-seeded store. The documented workaround is `DROP SCHEMA … 
CASCADE` + reseed, which is destructive and loses version history/audit.

### 6.2 Corrected design — per-object idempotency

Replace the single top-level gate with **per-object** existence checks, writing only the objects
that are missing, idempotently, without duplicating existing versions.

For each **flow** in the seed:
- Resolve `ActiveFlow(env, f.method, f.path)` (or a lighter existence check). 
  - **Absent (`NotFound`)** → `PutFlowVersion` → (if `f.active`) `MarkValidated` → `SetActive`.
  - **Present** → **skip** (do not create a duplicate version). The seed is a *bootstrap*, not a
    migration tool: once a flow exists, further changes go through the admin API / Strapi, not by
    re-running the seed. (This keeps seeding a pure "write what's missing" op and avoids
    re-publishing or version-churning an operator-managed flow.)
- A finer check than `ActiveFlow`: "does flow identity `f.flowId` exist at all?" via a cheap
  existence query. **Decision:** gate on **flow identity existence** (`flowId`), not on the active
  route — because a created-but-not-yet-published flow has no active route yet, and we must not
  re-create its version on restart. A new store-internal helper `FlowExists(ctx, env, flowId) 
  (bool, error)` (one indexed PK read of `flows`) is the cleanest check. (Fallback if we avoid a new
  helper: catch the `(method,path)` unique-violation / `(flow_id,version)` conflict from
  `PutFlowVersion` and treat it as "already present" — but that relies on error-shape matching and
  can still bump a version; the explicit existence check is preferred.)

For each **JDM** / **connection** in the seed: same per-object pattern.
- JDM: gate on JDM identity existence (`jdms.id`); absent → `PutJDMVersion`; present → skip.
- Connection: gate on connection identity existence (`connections.key`); absent →
  `PutConnectionVersion`; present → skip.
- (Both `PutJDMVersion`/`PutConnectionVersion` currently *always* insert a new version + re-point;
  the per-object skip in the seeder is what makes a restart idempotent for them, matching the flow
  path.)

### 6.3 New store-internal existence helpers (requested seam addition, `internal/config`)

```go
func (s *PgStore) FlowExists(ctx, env, flowID string) (bool, error)       // SELECT 1 FROM flows WHERE id=$1
func (s *PgStore) JDMExists(ctx, env, jdmID string) (bool, error)         // SELECT 1 FROM jdms WHERE id=$1
func (s *PgStore) ConnectionExists(ctx, env, key string) (bool, error)    // SELECT 1 FROM connections WHERE key=$1
```
Each is a single indexed PK read, classified per the store taxonomy (a real DB error ⇒ `Upstream`,
surfaced; `NotFound`/no-row ⇒ `(false, nil)`). These are store-internal (not on the `Store` seam),
mirroring `MarkValidated`/`PromoteVersion`. **Flagged in §8** as a `internal/config` change this
slice drives (the seeder and admin validate both benefit).

### 6.4 Rewritten `SeedPgStore` shape

```
for each flow:   if !FlowExists -> PutFlowVersion [+ MarkValidated + SetActive if active]
for each jdm:    if !JDMExists -> PutJDMVersion
for each conn:   if !ConnectionExists -> PutConnectionVersion
return seeded = (anything was written)
```
Idempotency properties:
- Re-running the seed with **no new objects** writes nothing (every object exists) → no duplicate
  versions, no pointer churn, no audit spam. (Replaces the old all-or-nothing gate with a per-object
  no-op.)
- Adding **one new flow** to `seed.json` and restarting writes **only** that flow (its version +
  validate + activate) and leaves every existing object byte-identical. ✅ (The exact bug.)
- A partially-seeded store (e.g. a prior crash mid-seed) **self-heals**: each missing object is
  written on the next start; each present one is skipped. The old gate could not recover a partial
  seed except by DROP SCHEMA.

### 6.5 Edge cases / invariants

- **Concurrency:** two engine instances starting against the same fresh store could both see
  `!FlowExists` and both `PutFlowVersion`. `PutFlowVersion` is already transactional with
  `SELECT … FOR UPDATE` on the parent identity + `INSERT … ON CONFLICT (id) DO NOTHING` on `flows`,
  so the second instance's version insert serializes behind the lock; worst case it creates a `v2`
  of an identical tree (benign, append-only) and both try to activate. `SetActive` is an idempotent
  pointer upsert. Acceptable: seeding is a bootstrap, typically single-instance at first boot; the
  per-object skip plus the store's existing locking make a double-seed benign (an extra identical
  version), never corrupt. A stricter "seed under an advisory lock" is a possible hardening,
  documented as optional, not required for the bug fix.
- **`f.active=false` flows:** created but not published; on restart `FlowExists` is true → skipped.
  Correct (we don't re-create or auto-publish).
- **Changed tree for an existing flowId in the seed:** **not** re-applied (skip-if-exists). The seed
  is bootstrap-only; editing a live flow is an admin-API/Strapi operation. Documented so no one
  expects "edit seed.json + restart" to update a flow (it won't — by design).

---

## 7. Testability

- **Admin handlers (unit):** table-driven tests against a **fake `AdminStore`** (returns canned
  values / classified errors) assert the request→method mapping, status codes, the secret-value
  rejection, and the error→status map (`statusForAdmin`). No DB. Table-driven per the project's
  default test style.
- **Operator guard (unit):** `OperatorGuard.Protect` with a fake `OperatorAuthenticator`:
  disabled-plane ⇒ 503; bad credential ⇒ 401; wrong role ⇒ 403; valid ⇒ next called + operator in
  ctx. Assert nothing sensitive is logged (inspect the fake logger's fields).
- **Routing precedence (unit):** a `ServeMux` wired with the admin routes resolves
  `/admin/flows/validate` to the validate handler, not the `{id}/publish` handler; `GET` vs `POST`
  dispatch; `{type}`/`{id}` capture.
- **Validate / dry-run (unit + integration):** unit with mocked sources (fixtures) asserts
  structural issues and fixture diffs; the dry-run suppression fix gets a `flow`-package unit test
  (an action with a write op under `observ.WithDryRun` records `wrote:"suppressed"` and performs no
  `Execute` write — assert via a spy client). Integration (Docker postgres:16) asserts an end-to-end
  create→validate→publish→audit round-trip and a dry-run against a real stored flow with writes
  suppressed (no row written, trace present) — extends the existing `httpapi` integration suite.
- **`/readyz` fix (unit + integration):** unit — a `readyz` with a healthy `store.Ping` and a
  **failing** data-source registry returns `200` (the whole point); a failing `store.Ping` returns
  `503 config_store`. Integration — bring up the config store, point a data-source connection at a
  dead address, assert `/readyz==200`.
- **Per-object seeding (integration):** seed once; add a second flow to the seed doc; re-seed;
  assert the new flow is written+active and the first flow's version row is byte-identical and not
  duplicated; re-seed again with no changes ⇒ `seeded==false`, zero new versions/audit rows. This
  directly pins the bug.

Hard-to-test smell check: the handlers are thin and the heavy logic is in already-tested store
methods + the pure validator, so the admin plane is almost entirely unit-testable with fakes; only
the two integration round-trips need Docker. That is the right shape.

---

## 8. Requested seam changes (for the stitch/owner step)

All additive, non-conflicting; flagged per the merge-contention rule (each worktree edits its own
packages; cross-package seam asks go here):

1. **`internal/config` — actor-on-context** (§3.5): add `config.WithAuditActor(ctx, subject) 
   context.Context` + a one-line read in the store's audit inserts (actor := ctx-actor else
   construction actor). Lets admin writes attribute to the operator without a signature change.
   *Fallback if declined:* attribute all admin writes to a single `"admin"` actor; capture the
   operator subject in the `observ` audit line. Design default is the context helper.
2. **`internal/config` — existence helpers** (§6.3): `FlowExists`/`JDMExists`/`ConnectionExists`
   (store-internal, single PK reads). Required for the per-object idempotent seeding fix.
3. **`internal/config/seed_pg.go` — rewrite `SeedPgStore`** to the per-object shape (§6.4). Owned
   by `internal/config`; this slice drives it as part of bug fix #2.
4. **`internal/flow/handlers.go` + `dryrun.go` — wire dry-run write-suppression** (§4.3). Owned by
   `internal/flow`; this slice drives it as the only consumer. Delete the dead shim, add the
   suppression branch, import `internal/observ`.
5. **`internal/httpapi/ops.go` — drop the data-source `registry.HealthCheck` gate from `/readyz`**
   (§5.2); readiness == config-store reachable. Owned by `internal/httpapi` (this slice).
6. **`internal/config` — `GetFlowVersion(ctx, env, flowID, version) (FlowVersion, error)`** (§2.6):
   store-internal PK read of a specific flow version + its fixtures (`NotFound` when absent).
   Required for stored-mode validate (and any future "inspect version N" admin read).
7. **`internal/config` — route-conflict classify tweak** (§2.3): in `classifyPg`, detect pg
   SQLSTATE `23505` on the `flows_method_path_key` constraint and surface it as a typed
   `Validation`/route_conflict marker so the admin edge maps it to `409` (not a misleading `502`).
8. **`internal/auth/middleware.go` — export `bearerToken` → `BearerToken(r) (string, bool)`** (§3.3)
   so `internal/httpapi`'s admin middleware reuses the single RFC-7235 parser.
9. **`internal/auth` — new `OperatorAuthenticator`/`Operator`/`OperatorGuard` +
   `StaticTokenOperatorAuth`** (§3.2/§3.3): the operator-auth mechanism. In-package to `auth` (owned
   here with the httpapi admin surface).
10. **`internal/httpapi/server.go` — add `Admin AdminStore` + `OperAuth auth.OperatorAuthenticator`
    to `Deps`** and build+mount the admin surface in `NewHandler` (§2.1a). In-package (this slice).
11. **`cmd/engine` — read `ADMIN_ENABLED`/`ADMIN_TOKENS`, construct `StaticTokenOperatorAuth` (fatal
    on malformed config / enabled-but-empty), pass the same `*config.PgStore` as both `store` and
    `deps.Admin`** (§2.1a/§3.3). The composition root; touched only at the join per the
    merge-contention rule.

Items 1–7 touch `internal/config`/`internal/flow` and are flagged for their owners / the join step;
items 8–11 are the `auth`/`httpapi`/`cmd` surface this slice owns directly. The design is written so
every non-`httpapi`/`auth` item is small, isolated, and individually justifiable.

---

## 9. Risks, assumptions, open decisions

**Assumptions**
- The engine process serves a single env (`""` today); the admin plane targets that same env. A
  multi-env admin surface (choose env per request) is out of scope. The request `env` field follows
  the exact rule in §2.9 (absent/`""` ⇒ ok; any non-empty ⇒ `400`), since the engine's own store is
  single-pool for `""`.
- `*config.PgStore` is the admin store; **in-memory mode does not support admin writes** (§2.2).
  Operators run the engine in config-store mode. Documented, not a silent failure (admin writes in
  in-memory mode return a classified error).
- The operator token allow-list (`ADMIN_TOKENS`) is delivered via env/secret manager like every
  other secret; the engine only ever sees hashes it computes at load. No plaintext token at rest.

**Risks**
- **R-adm-1 (control-plane exposure).** The admin plane can rewrite every API. Mitigation: separate
  credential path, deny-by-default, **mount-closed when unconfigured**, RBAC per route, constant-
  time credential compare, nothing sensitive logged. Highest-severity property of this slice;
  explicitly reviewed.
- **R-adm-2 (precedence regression).** A future `/admin/...` pattern could accidentally shadow or be
  shadowed by the `"/"` catch-all or a `{id}` segment. Mitigation: the precedence unit test (§7)
  pins `validate` vs `{id}/publish` and `/admin/*` vs `/`.
- **R-adm-3 (double-seed under concurrent first boot).** Benign extra identical version at worst
  (§6.5); optional advisory-lock hardening noted.
- **R-adm-4 (validate ref false-negative under store blip).** `HasJDM` treats non-NotFound errors as
  "present" to avoid blocking authors on a transient store error (§4.1); the trade-off is a dangling
  ref could slip past validate during a store blip and surface at publish/runtime instead.
  Acceptable (publish still re-checks via the real resolve path at request time).
- **R-adm-5 (dry-run reads hit real sources).** With `mocks` omitted, dry-run performs real reads
  (writes always suppressed). An operator dry-running against production data sources incurs real
  read load. Documented; `mocks` is the escape hatch for fully-isolated preview.

**Open decisions for the reviewer**
- **D1:** operator-auth v1 = static hashed bearer tokens (§3.3). Confirm this is acceptable for now
  vs requiring mTLS or a separate operator OIDC issuer immediately. (Interface is swappable either
  way; the question is only the v1 implementation.)
- **D2:** validate-stored-mode gated on `flow.read` while it flips `validated` (§3.4). Confirm, or
  tighten to a `flow.write`/`flow.validate` role.
- **D3:** seed is bootstrap-only — editing `seed.json` for an existing object does **not** update it
  on restart (§6.5). Confirm this is the intended contract (vs "seed re-applies changes"), since it
  changes operator expectations.
- **D4:** actor-on-context seam addition to `internal/config` (§3.5 / §8.1). Confirm the store owner
  accepts the one-line additive change, else fall back to single-actor attribution.
- **D5:** dry-run trace fidelity (§2.7). v1 emits suppressed-write records + the final response, not
  a full per-node walk (matches the current collector/interpreter wiring). Confirm this is enough,
  or pull the "wrap each node in `observ.TraceNode`" stretch into this slice for a full per-node
  trace.

---

## 10. Review responses

This revision answers `docs/.agents/tasks/admin-api/design-review.json` (verdict
`CHANGES_REQUESTED`, 9 blocking + 3 nits). Each finding is addressed, backlogged, or justified,
aligned to the original requirements (admin HTTP surface over the existing store methods; operator
auth separate from the public JWT path, deny-by-default; the two live-bug fixes).

- **F1 (HIGH) — dry-run suppression doesn't exist in the worktree.** ADDRESSED. Independently caught
  in §0 and fixed in §4.3: the slice now takes `internal/flow` in scope, adds the `observ.IsDryRun`
  branch to `actionHandler.Exec` (records `wrote:"suppressed"`, skips the write I/O), and deletes the
  dead `flow/dryrun.go` shim keeping only `isWriteOp`. No reliance on a pre-existing suppression.
- **F2 (HIGH) — worktree behind the frozen AC-14 obligation; don't cite STATE.md.** ADDRESSED. §0
  explicitly states STATE.md's "validate/dry-run already wired" and "suppression honored + tested
  green" claims are **stale/false in this worktree** (verified by grep + reading `handlers.go`), and
  the slice brings `flow.actionHandler` into compliance (§4.3) rather than assuming it. The design
  verifies against worktree source, not STATE.md.
- **F3 (HIGH) — `SetActive` returns no action.** ADDRESSED. §2.4/§2.5 rewritten: `SetActive(...)
  error` returns only an error; the handler sets the response `action` from the **route**
  (`/publish` vs `/rollback`), with the audit log as the source of truth for the effective action.
  The "the store returns rollback" wording is removed. Option (a)-lite (optional pointer read for
  effective-action) is noted as deferred.
- **F4 (MEDIUM) — fixture shape not on `config.FlowFixture`.** ADDRESSED. §4.2 rewritten: keep the
  frozen `config.FlowFixture {Name,Input,Want}` and define an **admin-request-local `AdminFixture`**
  (with `Mocks` + structured `Expect`) in `internal/httpapi`, with an explicit down-map to
  `FlowFixture` on persist. No contract-type widening.
- **F5 (MEDIUM) — `auth.bearerToken` unexported.** ADDRESSED. §3.3 now specifies exporting it as
  `auth.BearerToken(r) (string, bool)` (seam item §8.8) and calling that single parser from httpapi;
  the "reuse" claim now names a reachable symbol.
- **F6 (MEDIUM) — `Count()` referenced but not on the interface; malformed-config behavior.**
  ADDRESSED. The disabled-plane check is `g.authn == nil` (no `Count()` on the interface at all,
  §3.2). Malformed `ADMIN_TOKENS` or `ADMIN_ENABLED=true` with zero valid tokens is a **fatal
  startup error** (§3.3), never a silent drop or an enabled-but-empty allow-list.
- **F7 (MEDIUM) — Deps/AdminStore wiring under-specified.** ADDRESSED. New §2.1a states one
  `*config.PgStore` is passed as both the hot-path `store` and `deps.Admin`, adds `Admin`/`OperAuth`
  to `Deps`, and specifies `newAdmin(deps)` reads them. Seam items §8.10/§8.11.
- **F8 (MEDIUM) — route-collision 409-vs-502 is a guess.** ADDRESSED. Verified `classifyPg` wraps a
  `23505` unique violation as `Upstream` (indistinguishable → would be `502`). §2.3 now requires a
  **store-internal classify tweak** (SQLSTATE `23505` + constraint name → typed route_conflict) to
  map deterministically to `409`; the ambiguous "or 502 fallback" is removed (until the tweak lands,
  the honest coarse `502` applies, stated explicitly). Seam item §8.7.
- **F9 (MEDIUM) — validate two-mode discriminator under-specified.** ADDRESSED. §2.6 defines the
  exact rule: `flowId` + positive `version` ⇒ stored mode (load via new `GetFlowVersion`, validate,
  `MarkValidated` on all-pass; inline `flow` ignored; no stored match ⇒ `404`); else candidate mode
  (inline, stateless, no `MarkValidated`); neither ⇒ `400`. Seam item §8.6.
- **F10 (NIT) — `ValidateTree(tree,refs)` signature.** ADDRESSED. §2.6/§4.1 reworded to
  `flow.ValidateTree(fv.Tree, refs)` with the real signature `ValidateTree(root Node, refs
  RefResolver)`.
- **F11 (NIT) — dry-run trace fidelity overstated.** ADDRESSED. §2.7 now states (verified) the
  interpreter does not wrap nodes in `observ.TraceNode`, so v1 emits suppressed-write records + the
  response only — not a full per-node walk; the sample is trimmed and a full-trace stretch is D5.
- **F12 (NIT) — env comparison ambiguity.** ADDRESSED. §2.9 states the exact rule: `env` absent ⇒
  `""` (ok); explicit `""` ⇒ ok; any non-empty ⇒ `400`.

Net: all three HIGH and all six MEDIUM findings are resolved in the design; the three NITs are
folded in. The gate criteria the review marked NOT MET (5 and 7, resting on the dry-run suppression
gap) are now MET by taking the AC-14 fix in scope (§4.3) and reconciling the stale STATE.md claim
(§0).
