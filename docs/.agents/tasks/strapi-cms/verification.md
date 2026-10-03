# FEAT-003 — Verification Evidence (integration core)

Branch `feat/strapi-cms`, worktree `.worktrees/strapi-cms`. All commands run from
the paths shown. Node 20.19.1 / npm 10.8.2 (engine-strict=false per FEAT-001, so
the `engines.node="22.x"` pin does not block install). Resumed from the committed
FEAT-001/FEAT-002 state + the uncommitted partial FEAT-003 on disk.

## What FEAT-003 added / completed on top of the partial work
- `cms/src/plugins/rule-engine/server/tests/fixtures/seed-entries.ts` — seed-derived
  CMS entry fixtures (flow trees verbatim from `internal/config/testdata/seed.json`,
  credential-free discrete connections, JDM graphs). (New — the partial
  publish-transform test imported it but it was missing.)
- `cms/src/plugins/rule-engine/server/tests/fixtures/http-fetch.ts` — a `fetch`-shim
  over Node `http`/`https` so **nock** (which does not intercept undici's native
  `fetch`) can mock and assert every admin request. Injected into the AdminClient
  via its existing `fetchImpl` seam (AdminClient source unchanged).
- `cms/src/plugins/rule-engine/server/tests/publish-sequence.test.ts` — the §6.3
  vitest+nock integration suite (happy-path ordering, the three mandated blocking
  gates, 404/422/409/403, reconcile no-recreate + recreate-on-change, rollback,
  bearer-on-every-call, transport error).
- `cms/src/plugins/rule-engine/server/tests/publish-controller.test.ts` — unit
  coverage for `collectReferences` + the §5.5 `statusForBlocked` mapping + mappers.
- `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/serialize.test.ts`
  — the §6.2 serializer round-trip + casing assertions.
- `controllers/publish.ts` — added `statusForBlocked` so a blocked publish surfaces
  the correct §5.5 status (409 route collision / 404 not-found / 403 under-privileged
  / 422 publish-un-validated or ok:false / 503 transport-5xx), replacing the earlier
  coarse recoverable→503-else-422 branch.
- `server/src/routes/index.ts` + `server/src/index.ts` — complete the server module
  graph (publish route + beforePublish lifecycle subscribe). Plugin remains
  `enabled:false` in `config/plugins.ts` (admin UI is FEAT-004); these are the
  server-side wiring so the gate is live the instant the plugin is enabled.
- Reconciled the FEAT-001 placeholder: `sanity.test.ts` deleted, `vitest.config.ts`
  scoped to the real server + admin serializer suites.

## Gate 1 — install (`npm ci`)
```
cd .worktrees/strapi-cms/cms && npm ci
→ exit 0 (302 packages). Pre-existing transitive advisories from FEAT-001 deps
  reported by npm audit; out of FEAT-003 scope (no new direct deps added).
```

## Gate 2 — build (`npm run build`)
```
cd .worktrees/strapi-cms/cms && npm run build
→ ✔ Compiling TS
→ ✔ Building build context
→ ✔ Building admin panel
→ exit 0
(The Rollup @__PURE__ comment warnings originate in a transitive @ai-sdk/zod file,
 not in FEAT-003 code; build still exits 0.)
```
Also typechecked directly:
```
cd .worktrees/strapi-cms/cms && npx tsc --noEmit -p tsconfig.json
→ exit 0 (no errors)
```

## Gate 3 — tests (`npm run test`) — GREEN
```
cd .worktrees/strapi-cms/cms && npm run test
 ✓ admin/.../FlowCanvasField/serialize.test.ts        (8 tests)   — §6.2 serializer round-trip + casing
 ✓ server/tests/publish-transform.test.ts             (17 tests)  — §6.1 pure transforms
 ✓ server/tests/publish-controller.test.ts            (10 tests)  — collectReferences + §5.5 status map
 ✓ server/tests/publish-sequence.test.ts              (18 tests)  — §6.3 nock integration (ordering + blocking gates)
 ✓ server/tests/connection-denylist-guard.test.ts     (13 tests)  — FEAT-002 secret denylist
 Test Files  5 passed (5)
      Tests  66 passed (66)
→ exit 0
```
§6.3 blocking-gate assertions confirmed GREEN:
- happy path asserts exact order listConnections → createConnection(only changed)
  → createJdm → createFlow → validateFlow(stored `{env:"",flowId,version:N}`) →
  publishFlow(`{env:"",version:N}`); every request carries `Authorization: Bearer`;
  no body carries a flow/connection wrapper; write-backs read distinct keys
  (createFlow().version, createJdm().version, publishFlow().activeVersion).
- validation ok:false → publishFlow ZERO hits (nock pending), lastPublishStatus=failed.
- validator unreachable (503) → publishFlow ZERO hits, recoverable surfaced.
- inline secret (and seed-style `dsn`) → transform throws BEFORE any admin call;
  ZERO admin HTTP calls (no interceptors registered; nock.pendingMocks()==0).
- validate 404 → blocked, non-recoverable, publishFlow not called.
- publish 422 (keyed to 422, not 409) → abort.
- createFlow 409 route collision → abort, validate/publish not called.
- publish 403 under-privileged → abort, non-recoverable, exactly one attempt (no retry).
- reconcile: GET differing only by driver defaults + Go-cased ns resilience does NOT
  re-create; a genuine maxConns change DOES re-create.
- rollback → POST /admin/flows/{id}/rollback `{env:"",version}`.

## Gate 4 — Go untouched (`go build` + clean porcelain)
```
cd .worktrees/strapi-cms && CGO_ENABLED=1 go build ./...
→ exit 0

git -C .worktrees/strapi-cms status --porcelain -- go.mod go.sum internal cmd
→ (empty)  — no Go file changed; the Node app is fully isolated in cms/.
```

## Notes
- nock interop: nock 13.5.6 does not intercept undici's native `fetch`; the suite
  injects an `http`-backed fetch shim through the AdminClient's `fetchImpl` seam so
  nock sees/asserts the traffic. The AdminClient production path (native fetch) is
  unchanged.
- No raw secret value is ever sent/stored/logged: the transform throws on any
  denylist key before any admin call; connections carry `secretRef` only.

---

# FEAT-004 — Verification Evidence (editors — the UI layer)

Branch `feat/strapi-cms`, worktree `.worktrees/strapi-cms`. Commands run from the
paths shown. Node 20.19.1 / npm 10 (engine-strict=false per FEAT-001, so the
`engines.node="22.x"` pin does not block install). Resumed from the committed
FEAT-003 state (`631ce18`).

## What FEAT-004 added
- `cms/src/plugins/rule-engine/package.json` — the plugin root manifest Strapi's
  loader requires for a path-resolved local plugin: `strapi.kind:"plugin"`,
  `strapi.name:"rule-engine"`, and an `exports` map pointing `./strapi-admin` at
  `admin/src/index.tsx` and `./strapi-server` at `server/src/index.ts`. Without it
  the admin bundler (`getMapOfPluginsWithAdmin`, verified in
  `@strapi/strapi/dist/src/node/core/plugins.js`) does not include the plugin's
  admin entry, and the server loader (`@strapi/core` get-enabled-plugins) cannot
  read the plugin info.
- `cms/src/plugins/rule-engine/admin/src/index.tsx` — admin entry; `register(app)`
  calls `app.customFields.register` for both fields via the pure descriptors.
- `cms/src/plugins/rule-engine/admin/src/customFields.ts` — PURE descriptors for
  both fields, each base `type:'json'` (finding 13), with LAZY `Input` loaders so
  the heavy jdm-editor/reactflow bundles load only when a field renders (keeps the
  §6.4 smoke runtime-free).
- `cms/src/plugins/rule-engine/admin/src/lib/parseStored.ts` — PURE parse-or-fallback
  helper shared by both fields (the raw-JSON fallback decision), unit-tested by §6.4.
- `cms/src/plugins/rule-engine/admin/src/components/JdmEditorField/index.tsx` —
  wraps `@gorules/jdm-editor` `<JdmConfigProvider>`/`<DecisionGraph value onChange>`
  in a design-system `Field`, debounced onChange (300ms), writes back via
  `onChange({target:{name,value,type:'json'}})`, raw-JSON fallback on parse failure.
- `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx` —
  reactflow 11 `<ReactFlow>` canvas with the 11-node engine palette and a per-node
  spec side-panel; REUSES the committed `./serialize.ts` (`canvasToTree`/
  `treeToCanvas`) for canvas<->tree conversion, keeps x/y in a sidecar layout map,
  same raw-JSON fallback. The FEAT-003 serializer is imported, not re-implemented.
- `cms/src/plugins/rule-engine/admin/src/components/RawJsonFallback/index.tsx` —
  shared degrade-gracefully editor (textarea; commits only when the text parses).
- `cms/src/plugins/rule-engine/admin/src/components/ValidationPanel/index.tsx` —
  renders `lastValidation {structural[], fixtures[]}` inline (structural codes +
  per-fixture PASS/FAIL diffs).
- `cms/src/plugins/rule-engine/admin/src/smoke.test.ts` — the §6.4 admin smoke test.
- `cms/src/plugins/rule-engine/server/src/index.ts` — now also registers both custom
  fields server-side (`strapi.customFields.register([...type:'json'])`) so the
  `type:'customField'` attributes validate at load time.
- `cms/src/api/flow/content-types/flow/schema.json` — `tree` repointed to
  `plugin::rule-engine.flow-canvas` (`type:'customField'`, base json).
- `cms/src/api/jdm/content-types/jdm/schema.json` — `doc` repointed to
  `plugin::rule-engine.jdm-editor` (`type:'customField'`, base json).
- `cms/config/plugins.ts` — rule-engine plugin flipped to `enabled:true`.
- `cms/package.json` — `build` script prefixed with
  `NODE_OPTIONS=--max-old-space-size=4096` (see note below).

## Gate 1 — install (`npm ci`)
```
cd .worktrees/strapi-cms/cms && npm ci
→ exit 0 (302 packages). No new direct deps added by FEAT-004 (jdm-editor 1.52.0 and
  reactflow 11.11.4 were already pinned by FEAT-001); pre-existing transitive
  advisories reported by npm audit, out of scope.
```

## Gate 2 — build (`npm run build`) — admin bundle WITH both custom fields
```
cd .worktrees/strapi-cms/cms && npm run build
→ ✔ Compiling TS (7.3s)
→ ✔ Building build context
→ ✔ Building admin panel (~187–220s)
→ exit 0
```
The admin bundle now includes the rule-engine plugin's admin entry (both custom
fields registered) and the content types point Flow.tree/Jdm.doc at them.
(The Rollup `@__PURE__` comment warnings originate in a transitive @ai-sdk/zod file,
not FEAT-004 code; build still exits 0.)

Note — heap size: the default Node heap (~2 GB) OOMs while Rollup bundles the admin
panel once the two editors (jdm-editor pulls antd + monaco + a ZEN WASM module,
plus reactflow) are in the graph. TS compile and build-context both complete; only
the admin bundle step hit the ceiling. Fix is a build-infra bump, not a code change:
the `build` script sets `NODE_OPTIONS=--max-old-space-size=4096`. Verified both
inline (`NODE_OPTIONS=... npm run build`) and via the plain `npm run build` script
(embedded prefix) — both exit 0. The posix env prefix matches the design's posix CI
(`cd cms && npm ci && npm run build`, §6.4); no new dependency introduced.

## Gate 3 — tests (`npm run test`) — GREEN (full suite incl. §6.2 + §6.4)
```
cd .worktrees/strapi-cms/cms && npm run test
 ✓ admin/.../FlowCanvasField/serialize.test.ts   (8 tests)   — §6.2 round-trip + mixed-casing (still passing)
 ✓ server/tests/publish-transform.test.ts        (17 tests)  — §6.1 pure transforms
 ✓ admin/src/smoke.test.ts                        (7 tests)   — §6.4 admin smoke (NEW)
 ✓ server/tests/publish-controller.test.ts        (10 tests)
 ✓ server/tests/publish-sequence.test.ts          (18 tests)  — §6.3 nock integration
 ✓ server/tests/connection-denylist-guard.test.ts (13 tests)
 Test Files  6 passed (6)
      Tests  73 passed (73)
→ exit 0
```
§6.4 smoke assertions (GREEN):
- `register(app)` registers BOTH fields without throwing; `app.customFields.register`
  called twice; names == `['flow-canvas','jdm-editor']`; both `type==='json'`,
  `pluginId==='rule-engine'`.
- descriptors expose lazy `Input` component loaders (functions).
- malformed stored JSON → `parseStoredJson` returns `ok:false` with the raw text
  preserved + an "Invalid JSON" error (the raw-JSON fallback decision — no crash).
- bare scalar string → fallback ("not a JSON object").
- well-formed object / JSON string → `ok:true` parsed; null/empty → supplied empty
  value (fresh entry opens blank-but-valid).

The §6.2 serializer round-trip + casing test (operation.{Kind,Payload,Required}
capitalized; set/condition/decision/action keys camelCase) is unchanged and still
GREEN — FlowCanvasField imports the SAME `serialize.ts`, no duplicate serializer.

## Gate 4 — Go untouched (`go build` + clean porcelain)
```
cd .worktrees/strapi-cms && CGO_ENABLED=1 go build ./...
→ exit 0

git -C .worktrees/strapi-cms status --porcelain -- go.mod go.sum internal cmd
→ (empty)  — no Go file changed; the Node app is fully isolated in cms/.
```

## Notes
- Custom field registered on BOTH halves per Strapi 5 docs: server
  (`strapi.customFields.register`, base type json) so the `type:'customField'`
  attribute validates, and admin (`app.customFields.register`, base type json) so
  the editor mounts in the Content Manager. The base `type:'json'` is what makes
  Strapi persist the serialized value into the JSON column with no double-encode
  (a `string` base would double-encode).
- Plugin-entry resolution confirmed against the installed Strapi 5 source: the admin
  bundler reads `exports['./strapi-admin'].import` from the plugin root package.json
  (`core/plugins.js` `getMapOfPluginsWithAdmin`), and the server loader resolves
  `exports['./strapi-server']` (`@strapi/core` loaders/plugins/index.js).
- Heavy editor imports are lazy (dynamic `import()` in the descriptor `Input`
  loaders), so the §6.4 smoke runs in the node vitest env (no jsdom) without pulling
  jdm-editor/reactflow; the bundle only loads an editor when its field renders.
