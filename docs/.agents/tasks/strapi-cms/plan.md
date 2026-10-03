# Implementation Plan — Strapi CMS control-plane module

Grounded in the APPROVED design (`docs/.agents/tasks/strapi-cms/design.md`), the frozen AS-BUILT
admin contract (`docs/lld/slice-f-admin-api.md`, verified against the merged Go structs in
`internal/httpapi/admin_handlers.go` / `admin_validate.go` / `internal/connect/connect.go`), and the
engine config model in `internal/config/testdata/seed.json`.

All work lives under the worktree `cms/` dir and is isolated from the Go module. Use ABSOLUTE paths.
Step cwd is the MAIN workspace; the worktree root is
`/home/nuzirwan/project/rule-engine-api/.worktrees/strapi-cms` and the CMS app is its `cms/` subdir.

## Load-bearing contract facts (apply throughout)

- Every admin request body is **FLAT** — `env` is a top-level sibling, there is **no**
  `flow`/`connection` wrapper — decoded `DisallowUnknownFields`, so a wrapper or stray field is a
  hard 400. (Candidate-mode validate is the only `flow`-wrapped body; the CMS uses **stored mode**.)
- Payload `env` = `Environment.payloadEnv` (default `""`), **not** the Environment name. Cross-env
  selection is by base URL + operator token.
- Publish ordering: `listConnections` -> `createConnection` (only changed) -> `createJdm`
  (create==activate) -> `createFlow` (validated=false) -> `validateFlow` (stored `{env,flowId,version:N}`)
  -> `publishFlow`.
- Status codes the CMS keys off: **422** publish-blocking un-validated version (`config.ErrUnvalidated`,
  NOT 409); **409** create-time route `(method,path)` collision; **400** bad shape / non-empty env /
  wrapper / inline secret; **404** stored-mode validate version-not-found; **403** under-privileged
  token; **401** bad/absent bearer.
- Connection settings = discrete credential-free driver shape (`host/port/database/user/sslmode` +
  nested `pool.{maxConns,minConns}`) + `secretRef`; **never a `dsn`, never a secret value**. Denylist
  (case-insensitive): `password,pwd,secret,token,apikey,dsn`.
- Resilience on the wire is **tag-less Go-cased** `{ Timeout:<nanoseconds>, Retry:{ MaxAttempts } }`.
- Flow-canvas serializer preserves **mixed casing**: `operation.{Kind,Payload,Required}` capitalized,
  everything else camelCase.
- Both editor custom fields register with base `type:'json'`. React stays **18.3.1**; `reactflow`
  pinned **11.11.4** to match what `@gorules/jdm-editor@1.52.0` bundles (one React Flow runtime).

## Environment note

Local Node is **v20.19.1**; the design pins `engines.node = "22.x"`. Strapi 5 allows `node >=20`, so
install/build succeed on Node 20, but keep `engines.node="22.x"` and add `cms/.npmrc`
`engine-strict=false` so the `EBADENGINE` warning is non-fatal. Never add Node deps to `go.mod`;
never put Node files under the Go `internal/`/`cmd/` trees.

## VERIFICATION GATE (implementer AND final reviewer MUST RUN, not just read)

1. `cd /home/nuzirwan/project/rule-engine-api/.worktrees/strapi-cms/cms && npm ci` (or `npm install`
   on first scaffold) succeeds.
2. `cd .../cms && npm run build` (Strapi build) succeeds.
3. `cd .../cms && npm run test` (vitest) runs GREEN with the admin API mocked via nock — asserting:
   flat wrapped envelopes (no wrapper, `env=""` top-level), `create -> validate(stored {flowId,version:N})
   -> publish` ordering, the `Authorization: Bearer <token>` header, correctly transformed payloads,
   and that a **422 validation failure**, an **unreachable validator**, AND a **detected inline secret**
   EACH block publish with **zero `createFlow`/`publishFlow` calls**.
4. Go module untouched: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/strapi-cms &&
   CGO_ENABLED=1 go build ./...` still succeeds.

## Build-loop stop contract

The build loop stops when
`/home/nuzirwan/project/rule-engine-api/.worktrees/strapi-cms/docs/.agents/tasks/strapi-cms/review.json`
has `verdict=APPROVED`, written by the semantic reviewer (the loop's last step) **after the full gate
above passes**. On iteration exhaustion the loop PAUSES.

---

## Plan

- [ ] 1. Scaffold the isolated Strapi 5 TypeScript app with exact-pinned deps + test harness (design §2, §7).
      Scaffold in-place at `cms/` with the official Strapi 5.56.0 generator (TypeScript). Pin EVERY dep
      exactly (no `^`/`~`) per §7: `@strapi/strapi 5.56.0`, `@strapi/design-system 2.2.4`, `@strapi/icons 2.2.4`,
      `@gorules/jdm-editor 1.52.0`, `reactflow 11.11.4`, `react 18.3.1`, `react-dom 18.3.1`,
      `react-router-dom 6.30.3`, `styled-components 6.1.13`, `pg 8.13.1`, `zod 3.24.2`; dev `@strapi/sdk-plugin 6.1.1`,
      `vitest 2.1.8`, `nock 13.5.6`. Verify each pin resolves on npm at build time (`npm view <pkg>@<ver> version`);
      if one no longer exists, record it and pick the nearest patch. `engines.node="22.x"`; scripts
      `build`/`develop`/`start`/`test`(=`vitest run`). Add `cms/.npmrc` (`save-exact=true`,
      `engine-strict=false`), `cms/.nvmrc` (22.11.0), `cms/.gitignore`
      (node_modules/build/dist/.cache/.strapi/.tmp), `cms/.env.example`, vitest config, a tsconfig for the
      server services, the empty plugin skeleton (`src/plugins/rule-engine/{admin/src,server/src/{services,controllers,lifecycles,routes}}`, `types/`),
      and register the plugin in `config/plugins.ts`. Add one placeholder test so `npm run test` is green.
      Files: `cms/package.json`, `cms/package-lock.json`, `cms/.npmrc`, `cms/.nvmrc`, `cms/.gitignore`,
      `cms/.env.example`, `cms/vitest.config.ts`, `cms/tsconfig.json`, `cms/config/plugins.ts`,
      `cms/src/plugins/rule-engine/**` (skeleton), `cms/src/plugins/rule-engine/server/tests/sanity.test.ts`.
      Verify: `cd cms && npm install && npm ci && npm run build && npm run test` all exit 0; from the
      worktree root `CGO_ENABLED=1 go build ./...` exits 0 and `git status --porcelain -- go.mod go.sum internal cmd` is empty.

- [ ] 2. Define the four content types + components with Draft & Publish and the Connection secret-denylist guard (design §3).
      Flow (§3.1: flowId uid, method enum, path string, `tree` json-for-now, `fixtures` repeatable `flow-fixture`,
      `environment` relation, note, engineVersion read-only, lastPublishStatus enum, lastValidation json; draftAndPublish),
      `flow-fixture` component (§3.1.1: name required, input required json, mocks/expect nullable json);
      Jdm (§3.2: jdmId uid, `doc` json-for-now, environment, note, engineVersion; draftAndPublish);
      Connection (§3.3: key uid, type enum postgres/valkey/rest, `settings` component per-type discrete
      credential-free shape §3.3.1, secretRef, `resilience-policy` component authoring timeoutMs+retry.maxAttempts,
      environment, engineVersion; draftAndPublish; NEVER a `dsn` field);
      Environment (§3.4: name uid, adminApiBaseUrl, operatorTokenRef, payloadEnv default `""`; no draftAndPublish).
      Add the Connection `beforeCreate`/`beforeUpdate` lifecycle guard rejecting any settings key matching
      `password,pwd,secret,token,apikey,dsn` (case-insensitive) with a unit-testable guard function.
      Files: `cms/src/api/{flow,jdm,connection,environment}/content-types/*/schema.json` (+ generated
      routes/controllers/services), `cms/src/components/flow/flow-fixture.json`,
      `cms/src/components/connection/{postgres,rest,valkey}-settings.json`,
      `cms/src/components/connection/resilience-policy.json`,
      `cms/src/api/connection/content-types/connection/lifecycles.ts`, a vitest test for the guard.
      Verify: `cd cms && npm run build && npm run test` exit 0 (schemas compile; denylist guard test passes);
      `CGO_ENABLED=1 go build ./...` exits 0.

- [ ] 3. Implement the publish pipeline core + flow-canvas serializer + the full vitest/nock test suite (design §4.2, §5, §6). THIS IS THE VERIFICATION-GATE CORE.
      Shared wire/engine TS types (`cms/types/engine.ts`) matching the as-built flat structs. `publish-transform.ts`
      PURE fns (§5.2): `flowToEnginePayload`/`jdmToEnginePayload`(version:0)/`connectionToEnginePayload`
      (Go-cased ns resilience; THROWS on denylist/dsn) — all flat, no wrapper, no stray fields. `admin-client.ts`
      (§5.1) native-fetch typed client: createFlow/validateFlow(stored `{env,flowId,version}`)/publishFlow/
      rollbackFlow/createJdm/createConnection/listConnections/dryRunFlow/audit; `Authorization: Bearer` + `env=payloadEnv`
      on every call; token read at call time, never persisted/logged. `validation.ts` + `controllers/publish.ts`
      + `beforePublish` lifecycle (§5.3/§5.4): the ordered reconcile->createJdm->createFlow->validate(stored)->publish
      sequence with write-backs from distinct keys and BLOCK-on-failure semantics. Flow-canvas PURE serializer
      (`admin/src/components/FlowCanvasField/serialize.ts`, §4.2) canvas<->engine Node tree preserving mixed casing,
      layout in a sidecar map. Full test suite (§6.1/§6.2/§6.3): transform round-trips seed trees/graphs; serializer
      round-trip + casing; happy-path ordering/flat-payload/bearer assertions; and the three mandated blocking gates
      (422, unreachable validator, inline secret) each proving zero `createFlow`/`publishFlow` calls; plus validate-404,
      publish-422, create-409, 403, reconcile-no-spurious-recreate, rollback. Remove the step-1 placeholder test.
      Files: `cms/types/engine.ts`, `cms/src/plugins/rule-engine/server/src/services/{publish-transform,admin-client,validation}.ts`,
      `cms/src/plugins/rule-engine/server/src/controllers/publish.ts`,
      `cms/src/plugins/rule-engine/server/src/{lifecycles,routes}/*`,
      `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/serialize.ts`,
      `cms/src/plugins/rule-engine/server/tests/*.test.ts`, serializer test alongside `serialize.ts`.
      Verify: `cd cms && npm run test` runs the §6 suite GREEN (incl. the three blocking gates); `npm run build`
      exits 0; `CGO_ENABLED=1 go build ./...` exits 0.

- [ ] 4. Mount the two editor custom fields + ValidationPanel and point Flow.tree/Jdm.doc at them (design §4, §5, §6.4).
      Plugin admin entrypoint (`admin/src/index.ts`) registering `rule-engine.flow-canvas` and `rule-engine.jdm-editor`
      both with base `type:'json'`. `JdmEditorField` (§4.1: `<DecisionGraph>` in a design-system `Field`, debounced
      onChange, raw-JSON fallback). `FlowCanvasField` (§4.2: reactflow 11 canvas with the engine Node palette +
      per-node spec forms, reusing the FEAT-003/step-3 `serialize.ts` — no duplicate serializer — layout sidecar,
      raw-JSON fallback). `ValidationPanel` rendering `lastValidation` structural + fixture diffs. Repoint
      Flow.`tree` -> `rule-engine.flow-canvas` and Jdm.`doc` -> `rule-engine.jdm-editor` (base stays json).
      Add the §6.4 smoke test (both fields register with base json without throwing; malformed stored JSON renders
      the raw-JSON fallback, not a crash).
      Files: `cms/src/plugins/rule-engine/admin/src/index.ts`,
      `cms/src/plugins/rule-engine/admin/src/components/{JdmEditorField,FlowCanvasField,ValidationPanel}/**`,
      `cms/src/api/flow/content-types/flow/schema.json` + `cms/src/api/jdm/content-types/jdm/schema.json` (repoint),
      `cms/src/plugins/rule-engine/admin/src/__tests__/customfields.smoke.test.ts`.
      Verify: `cd cms && npm run build` (admin bundle with both custom fields compiles) exits 0; `npm run test`
      (serializer round-trip + §6.4 smoke + the step-3 suite) exits 0; `CGO_ENABLED=1 go build ./...` exits 0.

## Final gate (run the full VERIFICATION GATE above)

- [ ] 5. Run the complete verification gate end to end and confirm all four checks pass:
      `cd cms && npm ci && npm run build && npm run test` all exit 0 (with the three blocking-gate assertions
      green), and `CGO_ENABLED=1 go build ./...` from the worktree root exits 0. The semantic reviewer then writes
      `docs/.agents/tasks/strapi-cms/review.json` with `verdict=APPROVED` only after this full gate passes.
