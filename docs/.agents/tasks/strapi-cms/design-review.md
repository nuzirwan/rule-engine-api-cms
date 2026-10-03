# Design Review — Strapi CMS control-plane module (round 4)

Reviewed doc: `docs/.agents/tasks/strapi-cms/design.md` (status: "revised after design-review (round 3)").
Reviewer: fresh design-review subagent, no authoring context.
Date: 2026-10-03.

Sources read and cross-checked:
- `.worktrees/admin-api/docs/lld/slice-f-admin-api.md` (the real admin-API wire contract)
- `docs/lld/slice-d-configstore.md` (config store + versioning + validate/dry-run)
- `docs/lld-contracts.md` (`Store`/`FlowVersion`/`FlowFixture` seams)
- `internal/connect/drivers/postgres.go` (the real postgres driver `buildDSN`/`applyPoolSettings`)
- `internal/config/testdata/seed.json` (the real flow trees, JDM graphs, connections, fixtures)
- worktree layout under `.worktrees/strapi-cms` and `go.mod`

Verdict basis: this is round 3 of the design and most of the earlier structural problems are
genuinely fixed and verifiable against source. One concrete, test-affecting contract mismatch
remains, plus a few smaller gaps. Count of HIGH+MEDIUM findings drives the verdict.

---

## Findings

### 1. MEDIUM — Publish-blocking status code is wrong: design says 409, the real spec says 422

The design states throughout that publishing a not-yet-validated flow version returns **409**:

- §5.1 methods table: `publishFlow` — "**409 if the version is not validated**".
- §5.3 step 5 and the closing sentence: "even a stray `publishFlow` would 409".
- §5.4 step 6: "if 409 'not validated': abort".
- §5.5 error table row: "**409 un-validated version**".
- §6.3 test: "**Publish 409 (un-validated)**: force `publishFlow` → 409".

The real contract disagrees. `slice-f` §2.4 is explicit:

> `SetActive` refuses an un-validated version (publish-blocking, AC-13), so publishing a
> never-validated version returns **`422 Unprocessable Entity`** (mapped from the store's
> `Validation` "cannot publish un-validated" case — see §2.9 for why this one maps to 422 not 400).

`slice-f` §2.9 confirms it in the status table: `config.Validation "publish un-validated" → 422`.
The **only** `409` in the entire `slice-f` spec is the create-time route `(method,path)` collision
on `POST /admin/flows`, and even that is **contingent on an unlanded store fix** — until that fix
lands the engine reports `502` for a route collision (`slice-f` §2.3, §2.9). So `409` is not the
publish-gate code under any reading.

Why this blocks: §6.3's "Publish 409 (un-validated)" nock test would assert the wrong HTTP status.
A nock interceptor keyed to 409 will not match the engine's 422, so the test either fails or (worse)
passes against a mock that encodes the wrong contract and ships a client that mishandles the real
422. §5.5's error-handling row also keys recovery/UX off the wrong code.

Concrete fix: replace every "409 (un-validated / not validated)" occurrence for the **publish** path
with **422**. Specifically:
- §5.1 `publishFlow` row: "`200 { flowId, activeVersion, action:"publish" }`; **422 if the version
  is not validated**".
- §5.3 step 5 / closing line: "a stray `publishFlow` would **422**".
- §5.4 step 6: "if **422** 'not validated': abort".
- §5.5 table: "**422** un-validated version".
- §6.3 test name + interceptor: "Publish 422 (un-validated): force `publishFlow` → 422".

Keep `409` only if/when the design intends to model the create-time route collision — and if it
does, note that collision currently surfaces as `502` until the `slice-f` §2.3 store fix lands (see
finding 4).

### 2. MEDIUM — Operator RBAC role requirement is understated; a single "operator role" token will 403 on publish

§5.4 and §5.1 describe the operator credential as one bearer token with a single "operator role
only" (Strapi RBAC) gating publish. But `slice-f` §3.4 defines a **route→role RBAC map** on the
engine side with **distinct** roles the bearer token's subject must actually carry:

| Route the CMS calls | Required engine role (`slice-f` §3.4) |
| --- | --- |
| `POST /admin/flows`, `/admin/jdms`, `/admin/connections` | `flow.write` |
| `POST /admin/flows/{id}/publish`, `/rollback` | `flow.publish` |
| `POST /admin/flows/validate`, `/dry-run` | `flow.read` |
| `GET /admin/connections`, `GET /admin/audit/...` | `flow.read` |

The engine's `ADMIN_TOKENS` entry maps each token to `subject : comma-roles` (`slice-f` §3.3). A
CMS operator token provisioned with only one role (e.g. `flow.write`) will succeed on create but get
a **403** on `publishFlow` (needs `flow.publish`), and a token with only `flow.publish` will 403 on
create. The full publish sequence (reconcile → create connections/JDMs → createFlow → validate →
publish) touches **all three** roles: `flow.read` (listConnections, validate), `flow.write` (creates),
and `flow.publish` (publish/rollback).

Why this matters: the design never states the token subject must carry `flow.read` + `flow.write` +
`flow.publish`, nor does §5.5 handle the 403 for a correctly-authenticated-but-under-privileged token
(the 401 row covers only missing/invalid bearer). An operator setting up the CMS from this design
would hand it a single-role token and the publish flow would fail mid-sequence with an unhandled 403.

Concrete fix:
- In §5.1 (Operator credential) and §3.4/§5.6, state that the CMS operator token's engine-side
  subject **must be granted all three roles** `flow.read, flow.write, flow.publish` (per `slice-f`
  §3.3/§3.4), and that this is configured in the engine's `ADMIN_TOKENS`, not in the CMS.
- Add a §5.5 error row: `any admin op → 403 (authenticated but role not granted) | no (fix token
  roles) | "operator token lacks required role (<route needs flow.write/publish/read>)" | error,
  token redacted`.
- Optionally add a §6.3 assertion that the test token subject is configured with all three roles so
  the mocked happy-path mirrors a correctly-provisioned real token.

### 3. NIT — Stored-mode validate can return 404; neither the error table nor the sequence handles it

§5.1.1 pins the publish gate to stored-mode validate (`{env, flowId, version:N}`). `slice-f` §2.6
states that stored mode is the **one** validate case that can be non-200: "A `version` that resolves
to **no stored version** is a `404` ... a request to validate a thing that doesn't exist is a bad
request, not a negative result." The design's §5.3/§5.5 handle only transport/5xx and `200 ok:false`
for `validateFlow` — a 404 falls through unclassified.

In the normal publish sequence this should not fire (step 4 `createFlow` persists `N` immediately
before step 5 validates `N`), so it is a NIT, not a gate. But a race (version pruned, wrong `N`
written back, retry against a stale entry) would surface a 404 the client treats as an unexpected
error.

Concrete fix: add a `validateFlow → 404 (version not found)` row to §5.5 — not recoverable by retry,
surface "created version not found; re-create and retry", `lastPublishStatus=failed`. One line.

### 4. NIT — §5.5 treats all create 4xx as a fixable author error, but a route collision currently returns 502

§5.5 maps `createFlow` failures as either "4xx (bad payload / 400 unknown field / dsn in settings) →
not recoverable, author must fix" or "5xx/transport → recoverable, retry". Per `slice-f` §2.3/§2.9 a
`(method, path)` **route collision** (a different `flowId` claiming an owned route) currently surfaces
as **`502`** (the `409` mapping is contingent on an unlanded `internal/config` fix). So a genuinely
permanent, author-fixable error (two flows claiming one route) is reported by the engine today as a
transient-looking `502`, and the design's error table would tell the author "engine unavailable,
retry" forever.

Concrete fix: add a note to the §5.5 `createFlow` rows that a `502` from `POST /admin/flows` **may**
be a route `(method,path)` collision rather than an outage until the `slice-f` §2.3 store fix lands,
and that the CMS should pre-validate route uniqueness client-side (it already validates the `path`
pattern in §5.6 — extend to a uniqueness check against existing flows, or at least surface "route may
already be owned by another flow" on a 502 from create). Low priority because it depends on an engine
fix the CMS cannot make, but worth stating so the UX copy is not actively misleading.

### 5. NIT — `listConnections` response field name assumed `secretRef`; verify against the engine's JSON casing

§5.1 methods table and §5.4 reconcile assume `GET /admin/connections` returns objects with a
`secretRef` key (camelCase). `slice-f` §2.8's response example does use `secretRef`, so this is
consistent with the spec prose — but the design elsewhere (§4.2, finding 10) correctly warns that Go
structs serialized without JSON tags leak Go-cased keys (e.g. `Kind/Payload/Required`). The
connection-list response is produced by the engine from `connect.ConnectionDef`; if that struct
serializes without JSON tags, the real keys could be `Key/Type/Settings/SecretRef/Resilience`
(Go-cased), not the camelCase the design's `settingsDiffer`/reconcile compares against.

The design did not verify the actual JSON casing of the `GET /admin/connections` response against the
`connect.ConnectionDef` struct tags (it was verified for the flow `tree`'s `operation` sub-object but
not for the connection-list response). Concrete fix: confirm the serialized casing of
`connect.ConnectionDef` (read the struct tags in `internal/connect`) and pin the reconcile
comparison + the `audit()`/`listConnections` client types to the real keys, exactly as §4.2 does for
the flow tree. If the keys are Go-cased, §5.4 and §6.3's reconcile test must compare `SecretRef`/
`Settings`, not `secretRef`/`settings`.

---

## Verified Assumptions (checked against source, correct)

1. **Connection settings follow the real driver.** `internal/connect/drivers/postgres.go`:
   `buildDSN` reads discrete `host/port/database/user/sslmode` (and a `dsn` wins if present);
   `applyPoolSettings` reads nested `pool.maxConns`/`pool.minConns`; the password is injected from
   the resolved secret, never from the DSN string. Design §3.3.1's discrete, credential-free,
   `pool.{maxConns,minConns}` shape — and its deliberate omission of a flat `poolMax` and a `schema`
   key — is accurate.
2. **Seed connections embed credentials in `settings.dsn`.** `seed.json` `fmc-pg` is
   `postgres://root:root@127.0.0.1:5432/fmc_utility...` and `orders-pg` carries a `dsn` too. Design
   §3.3.1's "seed DSNs are a dev-only artifact the CMS does not reproduce; the CMS authors the
   discrete credential-free shape" is correct, and the `dsn` blanket denylist reject matches both the
   engine edge (`slice-f` §2.8) and the fact that the CMS never emits `dsn`.
3. **Mixed operation key casing.** `seed.json` confirms `operation.{Kind,Payload,Required}` are
   capitalized (Go-struct keys, no JSON tags) while `trueKey/falseKey`, `targetPath/from`,
   `jdmId/input/saveAs`, `connection/saveAs` are camelCase. Design §4.2 finding 10 and the §6.2
   casing test are accurate.
4. **Resilience has three encodings.** `seed.json` uses flat `{timeoutMs, maxAttempts}`; `slice-f`
   §2.8 request uses `{timeoutMs, retry:{maxAttempts}}`; the GET response marshals `timeout` in ns.
   Design §3.3.2's "send request shape, read ns response, convert ns→ms on compare" is accurate.
5. **Seed fixtures assert nothing.** `seed.json` fixtures are `{name, input}` only (no `mocks`/
   `expect`). Design §3.1.1's nullable `mocks`/`expect` and the §6.3 "use a fixture that declares an
   `expect`" for the blocking test are the right call.
6. **Wrapped envelopes + `DisallowUnknownFields`.** `slice-f` §2 decodes bodies strictly. Design
   §5.1/§5.2's `{env, flow}` / `{env, connection}` / `{env, jdmId, version:0, doc}` envelopes and the
   "no stray fields" assertion are correct.
7. **Payload `env` is `""`, cross-env by base URL.** `slice-f` §2.9: absent or `""` ok, any non-empty
   `env` → 400; `seed.json` `env` is `""`. Design §3.4's `payloadEnv` default `""` + per-Environment
   base URL selection is correct.
8. **Operator auth is separate from the public JWT.** `slice-f` §3.1–3.3: distinct `OperatorGuard`,
   `Authorization: Bearer <token>`, engine stores `sha256` hashes in `ADMIN_TOKENS`, deny-by-default/
   mount-closed. Design §5.1's named `ADMIN_API_OPERATOR_TOKEN` (CMS plaintext) vs the engine's hash
   list, never configured by the CMS, is accurate.
9. **Create→validate(stored)→publish ordering.** `slice-f` §2.3 lands `validated=false`, §2.6
   stored-mode marks validated by `{flowId, version}`, publish refuses un-validated. Design §5.4's
   ordering is correct (and necessary).
10. **JDM create == activate, `version:0` auto-assign.** `slice-f` §2.8: `PutJDMVersion` inserts and
    activates in one txn; `version<=0` ⇒ auto-assign. Design §3.2/§5.2 (`version:0`, write-back from
    the create response) is correct.
11. **Audit route IS mounted.** `slice-f` §2.1 mount block registers `GET /admin/audit/{type}/{id}`
    and §2.8 documents it. Design §5.1/§8 R3-C's correction (ship a typed `audit()` client, defer the
    viewer) is accurate.
12. **Response shapes.** `slice-f` §2.3 `createFlow → {flowId, version, validated:false}`; §2.8
    `createJdm → {jdmId, version}`, `createConnection → {key, version}`. Design §6.3's pinned
    write-back source keys (`createFlow().version`, `createJdm().version`,
    `publishFlow().activeVersion`) are correct.
13. **Persisted `FlowFixture` is thin.** `lld-contracts.md` + `slice-f` §4.2: persisted
    `config.FlowFixture` is `{Name, Input, Want}`; the admin plane accepts a richer in-flight
    `AdminFixture` with `Mocks`/structured `Expect`. Design §3.1.1's present-only mapping is
    consistent (the CMS emits what the create/validate request accepts).
14. **Worktree isolation.** `.worktrees/strapi-cms/go.mod` has no Node/Strapi/React references; the
    `cms/` subtree does not yet exist (expected — this is a design). The design's isolation rules
    (edit only `cms/**`, no Node in `go.mod`, no Node files in Go `internal/`/`cmd/`) are consistent
    with the current state.
15. **Exact pinning.** §7 pins all direct deps with no `^`/`~`, `save-exact=true`, committed
    lockfile, `npm ci` in CI. Meets the task's pinning requirement. (npm registry version/peer claims
    could not be re-verified from this environment — see note below — but they are internally
    consistent and the pinning discipline itself is sound.)

## Unverified / Wrong Assumptions

- **WRONG — publish-blocking status is 409.** The real contract returns **422** for publishing an
  un-validated version (`slice-f` §2.4/§2.9). `409` in `slice-f` is exclusively the create-time route
  collision, itself contingent on an unlanded store fix. See finding 1.
- **UNDERSTATED — "operator role only" is sufficient.** The engine RBAC map requires three distinct
  roles across the publish sequence (`flow.read`, `flow.write`, `flow.publish`); a single-role token
  403s mid-sequence. See finding 2.
- **UNVERIFIED — `GET /admin/connections` JSON key casing.** The design assumes camelCase
  `secretRef`/`settings` in the reconcile comparison but did not read `connect.ConnectionDef`'s struct
  tags to confirm the serialized casing (it only verified the flow tree's casing). See finding 5.
- **UNVERIFIED (environmental, not a design fault) — npm registry versions/peer ranges.** The design
  states all versions were verified via `npm view` on 2026-10-03 (Strapi 5.56.0, jdm-editor 1.52.0
  bundling `reactflow@11.11.4`, React 18.3.1, etc.). This reviewer cannot reach the npm registry to
  re-confirm those exact versions and peer ranges from this environment. The claims are internally
  consistent and plausible against current practice (Strapi 5 on React 18, jdm-editor on reactflow
  v11), and the single-React-Flow-runtime rationale is sound; flagged only as not independently
  re-verified here, not as a defect.

---

## Verdict

HIGH findings: 0. MEDIUM findings: 2 (findings 1 and 2). NIT findings: 3 (findings 3, 4, 5).

Because HIGH+MEDIUM > 0, the verdict is **CHANGES_REQUESTED**. Both MEDIUMs are concrete,
source-verified contract mismatches that would make the §6 tests encode the wrong behavior (a 409
interceptor that never matches the engine's 422; a single-role token that 403s on publish). They are
small, mechanical fixes — a quick loop-back is cheaper than shipping a client wired to the wrong
status code and role model.
