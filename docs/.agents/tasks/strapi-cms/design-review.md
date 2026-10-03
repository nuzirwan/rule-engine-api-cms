# Design Review — Strapi CMS control-plane module (round 5, resume)

Reviewed doc: `docs/.agents/tasks/strapi-cms/design.md` (status: "revised after design-review
(round 4) — reconciled to the FROZEN as-built admin API").
Reviewer: fresh design-review subagent, no authoring context.
Date: 2026-10-03.

This is a **resume review**. The design was already APPROVED on the big architectural calls; the
round-4 review left **0 HIGH, 2 MEDIUM, 3 NIT** findings. The job here is to verify those five
findings are now correctly closed against the FROZEN, AS-BUILT admin contract — not to re-litigate
settled architecture. Wire-level details were checked against `slice-f-admin-api.md` **and** the
real Go source in the worktree's `internal/`, not against the design's prose.

Sources read and cross-checked (as-built):
- `docs/lld/slice-f-admin-api.md` — the AS-BUILT admin-API contract (merged @ `df547be`), incl. the
  "AS-BUILT reconciliation" block, §2.3/§2.4/§2.6/§2.8/§2.9 status map, and §3.3/§3.4 RBAC.
- `internal/connect/connect.go` — the real `connect.ConnectionDef` and `connect.ResiliencePolicy`
  struct definitions (JSON-tag check).
- `internal/httpapi/admin_handlers.go` — the real `createFlowRequest` / `setActiveRequest` /
  `createJDMRequest` / `createConnectionRequest` structs, `setActive`, and the `listConnections`
  response-building map.
- `internal/httpapi/admin.go` — `statusForAdmin` error→status map, `decodeJSON`
  (`DisallowUnknownFields`), `knownMethods`.
- `internal/httpapi/admin_validate.go` — `validateFlowRequest` / `candidateFlowBody`, stored-vs-
  candidate discriminator, stored-mode 404 path.
- `internal/httpapi/admin_test.go` — `TestStatusForAdmin`, `TestAdminPublishUnvalidated`,
  `TestAdminListConnectionsRedacts` (pin the as-built status codes + list keys).

Verdict basis: the five round-4 findings are each verified **closed and correct against source**.
No new HIGH/MEDIUM arose. Count of HIGH+MEDIUM drives the verdict.

---

## Status of the five round-4 findings

### Finding 1 (MED, round 4) — Publish-blocking status is 422, not 409 — CLOSED ✓

Verified against source:
- `statusForAdmin` (`internal/httpapi/admin.go`) maps `config.ErrUnvalidated → 422`
  (`http.StatusUnprocessableEntity`) and `config.ErrRouteConflict → 409` (`http.StatusConflict`) as
  two **distinct** sentinels matched before the generic `config.ErrValidation → 400`. `slice-f` §2.4/
  §2.9 and the AS-BUILT block agree.
- `setActive` (`admin_handlers.go`) routes a `SetActive` error through `a.fail → statusForAdmin`, so
  a publish of an un-validated version surfaces as **422**. `TestAdminPublishUnvalidated` and
  `TestStatusForAdmin` pin this.

Design closure is correct and complete:
- §5.1 methods table: `publishFlow` row says "**422 if the version is not validated**
  (`config.ErrUnvalidated`)".
- §5.3 step 4/closing line: "a stray `publishFlow` would **422** (publish-blocking,
  `config.ErrUnvalidated` — not 409)".
- §5.4 step 6: "if **422** 'not validated': abort".
- §5.5 error table: `publishFlow`/`rollbackFlow` → "**422 un-validated version**
  (`config.ErrUnvalidated`)".
- §6.3 test: "**Publish 422 (un-validated)**… interceptor is keyed to **422**… a 409 interceptor
  here would never match".

`409` is kept **only** for the create-time route `(method,path)` collision, and crucially the
round-3 "502 until a store fix lands" hedge is **removed** — correct, because the AS-BUILT block
confirms the fix landed: `classifyPg` detects SQLSTATE `23505` on `flows_method_path_key` and wraps
it as `config.ErrRouteConflict ⇒ 409`. This is a live code path, not contingent. §5.5/§5.6/§6.3 all
treat the collision as an author-fixable live `409`.

### Finding 2 (MED, round 4) — Operator RBAC understated — CLOSED ✓

Verified against `slice-f` §3.4 RBAC map: creates → `flow.write`; publish/rollback → `flow.publish`;
validate/dry-run and `GET /admin/connections` + audit → `flow.read`. The full publish sequence
(reconcile via `listConnections` → creates → stored-mode validate → publish) therefore touches all
three roles, exactly as the design now states.

Design closure is correct and complete:
- §5.1 "Operator token RBAC roles — all three (MED finding 2)": states the engine-side **subject**
  of the CMS operator token must be granted `flow.read` + `flow.write` + `flow.publish` in the
  engine's `ADMIN_TOKENS` (e.g. `…:op:strapi:flow.read,flow.write,flow.publish`), configured
  engine-side, not in the CMS; explains the single-role token 403s mid-sequence.
- §3.4 Environment `operatorTokenRef` note and §5.6 "Operator-token provisioning" repeat the
  three-role requirement as an engine-side provisioning requirement the CMS documents but cannot
  enforce.
- §5.5 adds the `any admin op → 403 (authenticated but role not granted)` row: not retry-recoverable,
  surfaces "operator token lacks the required engine role", token redacted.
- §6.3 happy-path provisions a token subject with all three roles, and adds an "Under-privileged
  operator 403" case forcing `publishFlow → 403`.

### Finding 3 (NIT, round 4) — Stored-mode validate 404 unhandled — CLOSED ✓

Verified against `admin_validate.go`: stored mode (`stored := req.FlowID != "" && req.Version > 0`)
calls `a.store.GetFlowVersion(...)`; a `NotFound` is routed through `a.fail → statusForAdmin
(config.ErrNotFound → 404)`. `slice-f` §2.6 confirms the 404 is the one non-200 validate case.

Design closure: §5.1 methods table (stored-mode `version` that resolves to no stored version ⇒ 404),
§5.1.1, and the §5.5 row `validateFlow → 404 (version not found)` — not retry-recoverable, "created
version not found; re-create and retry", `lastPublishStatus=failed`. §6.3 adds a "Validate 404" test
asserting publish is blocked and `publishFlow` never called.

### Finding 4 (NIT, round 4) — Create-time route collision reconciled — CLOSED ✓

Verified: `config.ErrRouteConflict ⇒ 409` is live (AS-BUILT block + `statusForAdmin` +
`TestStatusForAdmin`). The round-3 "502 until a store fix lands" hedge is removed everywhere.

Design closure: §5.5 `createFlow` row maps a 409 collision to a non-misleading author-fixable message
("route `<method> <path>` is already owned by another flow — change the route or edit the existing
flow"), explicitly **not** a "retry, engine unavailable" copy; it also notes that a `502` from
`createFlow` now means a genuine store-upstream fault. §5.6 adds a client-side route-uniqueness
pre-check against existing active Flow entries before `createFlow`. §6.3 adds a "Create route
collision 409" test. UX copy is non-misleading.

### Finding 5 (NIT, round 4) — `GET /admin/connections` key casing — CLOSED ✓ (and the casing claim is exactly right)

This is the finding most prone to a wrong fix, so it was verified byte-for-byte against source:

- `internal/connect/connect.go`: **`ConnectionDef` has NO JSON tags** (`Key, Type string; Settings
  map[string]any; SecretRef string; Resilience ResiliencePolicy`), and **`ResiliencePolicy` has NO
  JSON tags** (`Timeout time.Duration; Retry struct{ MaxAttempts int; BaseBackoff, MaxBackoff
  time.Duration }; Breaker struct{ FailureThreshold uint32; FailureRatio float64; OpenTimeout
  time.Duration }`). A raw marshal of `ConnectionDef` would therefore produce **Go-cased** top-level
  keys `Key/Type/Settings/SecretRef/Resilience`.
- **But the handler does not raw-marshal the struct.** `listConnections` (`admin_handlers.go`)
  builds an **explicit `map[string]any`** per def with camelCase top-level keys
  `{"key","type","settings","secretRef","resilience"}`. So the top-level casing the CMS reads back is
  **camelCase** — matching the §2.8 prose and the design's §5.1 methods table / §5.4 reconcile.
- **The nested `resilience` value is the tag-less struct emitted raw** (`"resilience": d.Resilience`),
  so it serializes **Go-cased**: `Timeout` as a `time.Duration` **nanosecond integer**,
  `Retry.MaxAttempts`, `Retry.BaseBackoff`/`MaxBackoff`, and `Breaker.{FailureThreshold,FailureRatio,
  OpenTimeout}`.

The design captures this split **exactly**:
- §3.3.2 pins both the create-request and GET-response `resilience` to the tag-less Go-cased ns shape
  `{ Timeout:<ns>, Retry:{ MaxAttempts, … }, Breaker:{…} }`, and has the transform convert authored
  `timeoutMs`(ms) ↔ `Timeout`(ns).
- §5.1 methods table `listConnections` row: "**Top-level keys camelCase; `resilience` nested value is
  Go-cased `{ Timeout:<ns>, Retry:{ MaxAttempts, … }, Breaker:{…} }`** (tag-less struct)".
- §5.4 reconcile compares top-level camelCase `settings`/`secretRef`, reads `resilience.Timeout`(ns)
  → ms and `resilience.Retry.MaxAttempts`.
- §6.3 reconcile test pins the response top-level keys to camelCase `{key,type,settings,secretRef,
  resilience}` and the nested `resilience` to Go-cased ns `{ Timeout: 2000000000, Retry:{ MaxAttempts:
  2 } }`.

This is a precise, source-faithful close — notably it did **not** make the common mistake of
assuming the whole response is Go-cased or the whole response is camelCase.

---

## Round-4 contract correction (flat bodies) — verified, not a regression

The revision also carried a load-bearing correction beyond the five findings: the admin create bodies
are **flat objects with `env` as a top-level sibling**, not a `{env, flow:{…}}` / `{env,
connection:{…}}` wrapper. Verified against source and consistent — this is a correctness improvement,
not a break of a kept architectural call:
- `createFlowRequest` = `{ Env, FlowID, Method, Path, Tree, Fixtures, Note, Version(ignored) }` — flat.
- `setActiveRequest` = `{ Env, Version, Reason }` — flat.
- `createJDMRequest` = `{ Env, JDMID, Doc, Version, Note }` — flat.
- `createConnectionRequest` = `{ Env, Key, Type, Settings, SecretRef, Resilience, + rejectIfPresent
  secret-value fields }` — flat.
- `validateFlowRequest` = `{ Env, FlowID, Version, Flow *candidateFlowBody }` — candidate mode is the
  **one** body carrying a `flow` sub-object; stored mode is flat `{env, flowId, version}`. The CMS
  uses stored mode on the publish path.
- `decodeJSON` (`admin.go`) uses `DisallowUnknownFields`, so a wrapper or stray field is a hard 400 —
  which is exactly why the flat shape is load-bearing. The design's §5.1/§5.2/§6 all emit flat bodies
  and assert "no wrapper, no stray fields".

The round-4 review (`design-review.md` round 4) had, under its "Verified Assumptions", described the
bodies as wrapped `{env, flow}` envelopes. The round-4 **revision** corrected that against the merged
handler structs; the correction is right and the earlier assumption was the wrong one. Flagging only
to note the lineage — the current design matches source.

---

## Internal consistency & kept architectural calls — intact

Spot-checked that the revision did not break the previously-APPROVED calls:
- **Env-by-base-URL, payload `env=""`** (§3.4): consistent; `checkEnv` + `slice-f` §2.9 ("non-empty
  env ⇒ 400") confirm the single served env `""`.
- **JDM create == activate, `version:0` auto-assign** (§3.2/§5.2/§5.4 step 2): consistent with
  `slice-f` §2.8 and `createJDMRequest` (`version<=0 ⇒ auto-assign`); write-back from the create
  response.
- **create → validate(stored) → publish ordering** (§5.3/§5.4): consistent and necessary — create
  lands `validated=false`, stored-mode validate flips `validated` by `{flowId,version}`, publish 422s
  an un-validated version. The ordering rationale is correct.
- **Credential-free discrete connection settings + `secretRef`-only** (§3.3/§3.3.1): consistent with
  the postgres driver and the edge secret-reject (`hasSecretValueInSettings` + `rejectIfPresent`
  fields in `createConnectionRequest`). CMS denylist is a documented strict superset (adds `pwd`,
  blanket `dsn`).
- **Status-code table** (§5.5): the enumerated 422/409/404/403/401/400/502/504 all match
  `statusForAdmin` + `slice-f` §2.9.

No conflicting statements were found between the round-4 revision note, the CONTRACT CORRECTION box,
and §5. The three agree.

---

## Verified Assumptions (checked against as-built source, correct)

1. `config.ErrUnvalidated → 422` and `config.ErrRouteConflict → 409` are distinct sentinels in
   `statusForAdmin`, matched before generic `config.ErrValidation → 400` (`admin.go`;
   `TestStatusForAdmin`).
2. Publish of an un-validated version surfaces 422 via `setActive → a.fail → statusForAdmin`
   (`admin_handlers.go`; `TestAdminPublishUnvalidated`, `admin_integration_test.go`).
3. Route `(method,path)` collision 409 is **live** (not contingent/502) — AS-BUILT block + the 409
   sentinel.
4. Stored-mode validate 404 on a missing version via `GetFlowVersion → ErrNotFound → 404`
   (`admin_validate.go`; `slice-f` §2.6).
5. `GET /admin/connections` top-level keys are camelCase (explicit map in `listConnections`); nested
   `resilience` is the tag-less `connect.ResiliencePolicy` ⇒ Go-cased with ns `Timeout`
   (`admin_handlers.go` + `connect.go`).
6. All admin create bodies are flat with `env` top-level; `decodeJSON` is `DisallowUnknownFields`, so
   a wrapper/stray field is 400 (`admin_handlers.go`, `admin_validate.go`, `admin.go`).
7. RBAC map requires `flow.read`+`flow.write`+`flow.publish` across the publish sequence (`slice-f`
   §3.4).
8. JDM `createJDMRequest` auto-assigns on `version<=0`; create == activate (`admin_handlers.go`,
   `slice-f` §2.8).
9. Secret-value rejection is enforced at the edge via `rejectIfPresent` fields +
   `hasSecretValueInSettings` (`admin_handlers.go`, `admin_types.go`); the list response redacts
   `settings` through the central `Redactor` while keeping the `secretRef` pointer
   (`TestAdminListConnectionsRedacts`).

## Unverified / Wrong Assumptions

- **CORRECTED since round 4 — body shape.** The round-4 review's "Verified Assumptions" listed the
  bodies as wrapped `{env, flow}`/`{env, connection}` envelopes. Source shows they are **flat**; the
  round-4 revision corrected this and the current design is right. No open issue.
- **UNVERIFIED (environmental, not a design fault) — npm registry versions/peer ranges.** The design
  states all npm versions were verified via `npm view` on 2026-10-03 (Strapi 5.56.0, jdm-editor
  1.52.0 bundling `reactflow@11.11.4`, React 18.3.1, etc.). This reviewer cannot reach the npm
  registry from this environment to re-confirm the exact versions/peer ranges. The claims are
  internally consistent and the pinning discipline (§7, exact pins, committed lockfile, `npm ci`) is
  sound. Flagged as not independently re-verified here, not as a defect. (Not a HIGH/MEDIUM — does
  not affect the verdict.)

---

## Verdict

HIGH findings: **0**. MEDIUM findings: **0**. NIT findings: **0**.

All five round-4 findings (2 MEDIUM + 3 NIT) are verified **closed and correct against the FROZEN,
as-built admin contract and the real Go source** — including the two casing/status traps most likely
to be fixed wrongly (the 422-vs-409 publish gate and the camelCase-top-level / Go-cased-nested
`resilience` split in `GET /admin/connections`). The round-4 flat-body contract correction is also
source-accurate, and the previously-APPROVED architectural calls remain internally consistent.

Because HIGH+MEDIUM == 0, the verdict is **APPROVED**.
