# Design review (round 2) — Slice F: Admin API (control plane) + two live-bug fixes

Reviewed document: `.worktrees/admin-api/docs/lld/slice-f-admin-api.md`
Reviewer context: fresh read, every claim re-verified against the `admin-api`
worktree source (not against STATE.md, not against the mainline tree).
Codegraph: `mcp_codegraph_codegraph_explore` unavailable (no `.codegraph/`
directory in the worktree); verification was done by reading worktree source
directly. Noted, not blocking.

## Verdict

**APPROVED.** This is a resume review of the round-1 `CHANGES_REQUESTED`
(3 HIGH / 6 MEDIUM / 3 NIT). Every round-1 finding is resolved in the revision,
and each resolution was re-checked against the real worktree source. The three
load-bearing HIGH gaps — dry-run suppression, the AC-14 contract reconciliation,
and the `SetActive` response shape — are now correct and implementable as
written. No new blocking issue was introduced by the revision. The endpoint
contract (§2.9) is explicit, final, and internally consistent, so a downstream
Strapi CMS can integrate against it.

---

## Round-1 finding disposition (all re-verified on worktree source)

### F1 (HIGH) — dry-run write-suppression doesn't exist in the worktree — RESOLVED
Round-1 was correct that `actionHandler.Exec` honors nothing. The revision does
not paper over this: §0 and §4.3 take `internal/flow` **in scope** and specify
the exact fix — in `actionHandler.Exec`, before `client.Execute`, branch on
`observ.IsDryRun(ctx) && isWriteOp(op.Kind)`, record `wrote:"suppressed"` via the
attached collector, and continue with `walkChildren`. The dead `flow.dryRunKey{}`
shim is deleted (keeping only `isWriteOp`).

Verified feasible against source:
- `internal/flow/handlers.go` — `actionHandler.Exec(ctx, c *Ctx, n Node, dep Deps, w Walker)`
  still calls `client.Execute(ctx, op)` unconditionally today (the fix is a planned,
  in-scope edit — correct for a design doc). The snippet's symbols all exist:
  `n.ID`, `string(n.Type)`, `n.Children` (`internal/flow/node.go` `Node{ID, Type, Spec, Children}`),
  and `walkChildren(ctx, n.Children, c, dep, w)` is the real linear-continue helper.
- `internal/observ/collector.go` — `IsDryRun(ctx)` and `CollectorFrom(ctx)` exist;
  `tc.Record(nodeID, nodeType, branch string, attrs map[string]any)` is the frozen
  signature the snippet calls. The call `tc.Record(n.ID, string(n.Type), "", map[...])`
  matches exactly.
- `internal/flow/dryrun.go` — the shim is still present and still keyed on the
  package-internal `dryRunKey{}` (confirming round-1), so deleting it is the right
  move.
No reliance on a pre-existing suppression remains.

### F2 (HIGH) — worktree behind the frozen AC-14 obligation; don't cite STATE.md — RESOLVED
`docs/lld-contracts.md` is present in the worktree and freezes the obligation
verbatim (lines ~246–250: "**Slice A's `actionHandler` MUST call `IsDryRun(ctx)`
and, when true for a write op, skip the I/O and record `wrote:"suppressed"`**
(AC-14). This is a required Slice A implementation"). §0 of the revision states
plainly that STATE.md's "already wired / tested green" claims are **stale/false in
this worktree** (verified against `handlers.go`), and brings `flow.actionHandler`
into compliance (§4.3) instead of assuming it. The design now grounds the claim in
worktree source, not STATE.md. Gate criteria 5 and 7 are met by closing the gap in
scope.

### F3 (HIGH) — `SetActive` returns no action — RESOLVED
Verified `internal/config/pgstore.go` `SetActive(ctx, env, flowID string, version int) error`:
the publish-vs-rollback `action` is computed internally (`action = actPublish`,
`actRollback` when `version < *from`) and written to `audit_log` — **never
returned**. §2.4/§2.5 are rewritten to derive the response `action` from the
**route** (`/publish` vs `/rollback`), with the audit log as the source of truth
for the effective action, and the optional pointer-read refinement deferred. The
"the store returns rollback" wording is gone. This now matches the design's own
`AdminStore` interface (`SetActive(...) error`).

### F4 (MEDIUM) — fixture shape not on `config.FlowFixture` — RESOLVED
Verified `internal/config/store.go` `FlowFixture{Name string; Input map[string]any; Want map[string]any}`
— no `Mocks`, no structured `Expect`. §4.2 keeps that type frozen and defines an
admin-request-local `AdminFixture{Name, Input, Mocks, Expect{Output,BranchPath,Errors}}`
in `internal/httpapi`, with an explicit down-map to `config.FlowFixture` on
persist (name+input preserved, `Expect.Output`→`Want`, `Mocks` validation-only).
No contract-type widening. §3.5/§3.1 reference the richer wire type consistently.

### F5 (MEDIUM) — `auth.bearerToken` unexported — RESOLVED
Verified `internal/auth/middleware.go` `bearerToken(r)` is unexported. §3.3 + §8.8
specify exporting it as `auth.BearerToken(r *http.Request) (string, bool)` and
calling that single RFC-7235 parser from httpapi. The "reuse" claim now names a
reachable symbol.

### F6 (MEDIUM) — `Count()` referenced but not on the interface; malformed-config — RESOLVED
§3.2 removes `Count()` entirely: the disabled-plane check is `g.authn == nil`
(a nil `OperatorAuthenticator` passed by `cmd/engine`), so no interface method is
needed. §3.3 makes `ADMIN_ENABLED=true` with a malformed `ADMIN_TOKENS` entry (not
three parts, non-hex/wrong-length hash, empty/duplicate subject) **or** zero valid
tokens a **fatal startup error** — never a silent drop or an enabled-but-empty
allow-list. Deny-by-default posture is explicit.

### F7 (MEDIUM) — Deps/AdminStore wiring under-specified — RESOLVED
Verified `internal/httpapi/server.go`: `Deps` has no `Admin`/`OperAuth` today, and
`NewHandler(store Store, interp, deps)` takes `store` as the hot-path `Store`
(`ActiveFlow`+`ActiveRoutes`). New §2.1a states one `*config.PgStore` is passed
**both** as the `store` argument and as `deps.Admin`, adds `Admin AdminStore` +
`OperAuth auth.OperatorAuthenticator` to `Deps`, and specifies `newAdmin(deps)`
reads them (plus the in-memory fallback: `deps.Admin == nil` ⇒ admin writes
return a classified "requires config-store mode" error). Seam items §8.10/§8.11.
`*config.PgStore` structurally satisfies both interfaces (all methods verified
present). Resolved.

### F8 (MEDIUM) — route-collision 409-vs-502 is a guess — RESOLVED
Verified `internal/config/pgstore.go` `classifyPg`: a non-deadline driver error
(which a `23505` unique violation is) is wrapped as **`Upstream`** — so today a
`(method,path)` collision would surface as `502`, exactly as round-1 suspected.
§2.3 removes the ambiguous "409 or fall back to 502" hedge and requires an
in-scope store-internal classify tweak (detect SQLSTATE `23505` on
`flows_method_path_key` → typed `Validation`/route_conflict marker → deterministic
`409`), seam item §8.7. It states honestly that **until** the tweak lands a
collision reports the coarse-but-not-wrong `502`. No ambiguity remains in the
contract.

### F9 (MEDIUM) — validate two-mode discriminator under-specified — RESOLVED
§2.6 defines the exact rule with `version` as the tie-breaker: `flowId` + a
**positive** `version` ⇒ stored mode (load via new `GetFlowVersion`, validate,
`MarkValidated` on all-pass; an inline `flow` is ignored; a `version` resolving to
no stored version ⇒ `404`); absent/zero `version` ⇒ candidate mode (inline,
stateless, never `MarkValidated`); neither usable `flow` nor `flowId`+`version`
⇒ `400`. The `GetFlowVersion(ctx, env, flowID, version) (FlowVersion, error)` seam
is correctly flagged as new (§6.3/§8.6) — verified it does not yet exist on
`*config.PgStore`.

### F10 (NIT) — `ValidateTree(tree, refs)` signature — RESOLVED
§2.6/§4.1 reworded to `flow.ValidateTree(fv.Tree, refs)` with the real signature
`ValidateTree(root Node, refs RefResolver)` and `fv.Tree` typed `flow.Node`.

### F11 (NIT) — dry-run trace fidelity overstated — RESOLVED
Verified against `internal/flow/interpreter.go`: `walk` wraps each node in a
`dep.Trace` **span**, not in `observ.TraceNode`/`collector.Record`; there is no
automatic per-node `collector.Record` call. So the revision's §2.7 claim is
correct, and the v1 trace is trimmed to what actually gets emitted (the
suppressed-write record from the §4.3 fix + the final response). The full per-node
walk is posed as stretch D5.

### F12 (NIT) — env comparison ambiguity — RESOLVED
§2.9 states the exact rule: `env` absent ⇒ treated as `""` (ok); explicit
`""` ⇒ ok; any non-empty `env` ⇒ `400`.

---

## Endpoint contract check (for downstream Strapi integration)

The §2.9 status map is explicit and internally consistent; the two status pairs
the task flagged are correctly distinguished:
- **422** — publish of an un-validated version (`SetActive`'s `Validation`
  "cannot publish un-validated"), the publish-blocking gate (AC-13). Verified the
  store returns exactly this `Validation` on an un-validated publish.
- **409** — route `(method,path)` already owned by another flow (unique-violation),
  contingent on the §8.7 classify tweak; stated explicitly, with the honest coarse
  `502` as the pre-tweak behavior.
- **400** — request-shape invalid, tree structural invalid (create), secret value
  present in a connection body.
- **404** — missing flow/version/object (and stored-mode validate with no match).
- **502 / 504** — store `Upstream` / `Timeout` (true transport faults).
- **500** — `Internal`/unclassified only.
- validate (§2.6) and dry-run (§2.7) are the deliberate exceptions: a negative
  *result* is `200` with a body; only a malformed request or transport fault is
  non-200.

Open decisions D1–D5 (operator-auth v1 scheme, validate-stored-mode RBAC role,
seed-is-bootstrap-only, actor-on-context seam, dry-run trace fidelity) are posed
as explicit reviewer confirmations with documented defaults and fallbacks — they
are decisions, not ambiguities, and do not block implementation.

---

## Verified assumptions (checked against worktree source this round)

- `actionHandler.Exec` honors no dry-run flag today; `flow/dryrun.go` is the dead
  shim keyed on `flow.dryRunKey{}` — the §4.3 fix is a genuine in-scope change.
- `Node{ID string, Type NodeType, Spec, Children []Node}` and `walkChildren`,
  `observ.IsDryRun`, `observ.CollectorFrom`, `TraceCollector.Record(nodeID,nodeType,branch,attrs)`
  all exist with the signatures the §4.3 snippet uses — the fix compiles as written.
- `SetActive(ctx, env, flowID string, version int) error` returns only `error`;
  `action` is internal + audit-only (confirms the §2.4/§2.5 rewrite).
- `config.FlowFixture` is `{Name, Input, Want}` — the `AdminFixture` approach is
  the right call.
- `auth.bearerToken` is unexported — export is required and correct.
- `classifyPg` wraps `23505` as `Upstream` — the §2.3 classify tweak is necessary
  and correctly scoped.
- `GetFlowVersion` / `FlowExists` / `JDMExists` / `ConnectionExists` do not yet
  exist — correctly flagged as new in-scope seams (§6.3, §8.2, §8.6).
- All `AdminStore` methods (`PutFlowVersion`, `SetActive`, `Connections`,
  `GetJDM`, `ActiveFlow`, `PutJDMVersion`, `PutConnectionVersion`, `MarkValidated`,
  `AuditTrail`, `Ping`) exist on `*config.PgStore` with matching signatures.
- `internal/httpapi/ops.go` `readyz` currently gates on `reg.HealthCheck` AND
  `store.Ping` — the §5 false-negative root cause and the "drop the registry gate"
  fix are correct.
- `internal/config/seed_pg.go` resolves `sf.Flows[0]` via `ActiveFlow` and returns
  `(false, nil)` on hit — the §6 all-or-nothing root cause is correct; the
  per-object fix is sound.
- `docs/lld-contracts.md` present and freezes the AC-14 obligation — the §0/§4.3
  reconciliation is aligned with the frozen contract.
- `Deps` has no `Admin`/`OperAuth` today and `NewHandler`'s `store` is the hot-path
  `Store` — the §2.1a wiring is accurate and additive.

## Unverified / wrong assumptions

None. Every design claim re-checked this round matched the worktree source. The
one external claim the design itself flags as stale — STATE.md's "dry-run already
wired / tested green" — is correctly called out as false-in-this-worktree in §0
and is not relied upon.

---

## Gate scorecard (task's seven criteria)

1. `/admin/*` thin layer over existing store methods — **MET** (all methods
   verified; the `SetActive`-action and fixture-shape edges that broke this in
   round 1 are resolved).
2. Operator auth separate + deny-by-default — **MET** (`g.authn == nil`
   mount-closed; malformed/empty config fails startup).
3. secret_ref only, nothing logs secrets — **MET**.
4. stdlib net/http only — **MET**.
5. validate/dry-run integrated, not duplicated — **MET** (the suppression it
   integrates with is now brought into compliance in scope, §4.3).
6. `/readyz` + seeding fixes root-cause-based + testable — **MET**.
7. Consistency with FROZEN contracts — **MET** (AC-14 gap closed in scope and
   reconciled against lld-contracts.md).

Blocking (HIGH/MEDIUM) findings: 0. NITs: 0. Verdict: **APPROVED**.
