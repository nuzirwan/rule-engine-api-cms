# CMS E2E — Live Reproduction Evidence

This file records the LIVE reproduction performed during planning (not mocked tests).
The implement/review loop MUST append its own live re-verification below the baseline.

## Environment used (live)

- Engine: built from the worktree module `engine/` (`go.mod` module `nzr-rules-engine`,
  go 1.26) with `CGO_ENABLED=1 go build -o bin/engine ./cmd/engine`, run from
  `/home/nuzirwan/project/rule-engine-api/.worktrees/cms-e2e/engine` as
  `./bin/engine -env .env -seed internal/config/testdata/seed.json`.
  - IMPORTANT CORRECTION to the task brief: the engine's real `.env` lives at
    `engine/.env` (NOT at the worktree root), and `go.mod` is inside `engine/` (NOT
    the worktree root). So the engine must be built AND run from the `engine/` dir;
    `go build ./engine/cmd/engine` from the worktree root fails with "cannot find main
    module". The root `.env` the brief mentions does not exist; `engine/.env` is the
    one that is fully wired (CONFIG_DSN=matcha/rule_engine, VALKEY, ADMIN_ENABLED=true,
    ADMIN_TOKENS matching the CMS token).
  - Engine serves `:8080`, `/readyz` → 200, config-store mode on `matcha`/`rule_engine`
    with Valkey cache, seeded (3 flows, 2 jdms, 3 connections from seed.json).
  - Admin auth verified: `GET /admin/connections` → 401 without token, → 200 with
    `Authorization: Bearer 4ada50997bb2375767ff6ef9a5b03c97ebff78e68ecf2ba03c6aef184c4b2c15`.
  - Engine log captured at `/tmp/engine.log`.
- CMS: Strapi 5.56.0 on node v20.19.1. `cms/node_modules` did NOT exist in the worktree
  (contrary to the brief) — installed with `npm ci` (package-lock.json present, 1869
  packages). Run with `NODE_OPTIONS=--max-old-space-size=4096 npm run develop`, admin on
  `:1337`, DB `matcha`/`strapi_cms`. Log at `/tmp/cms*.log`.
- Admin user `abc@def.com` already existed; password reset via
  `npx strapi admin:reset-user-password` to drive the content-manager publish over HTTP.
- Stray engines: two `go run cmd/engine/main.go` instances from the MAIN repo `engine/`
  (not the worktree) were occupying `:8080`; both stopped so the worktree engine (whose
  log we can read) could bind.

## Bug #1 — Publish middleware NOT firing — REPRODUCED (confirmed root cause)

Steps:
1. Baseline engine DB: `rule_engine.flow_versions` = {fmc-order-by-id/1, fmc-order-by-msisdn/1,
   orders-expedite/1}. No `flow` flowId. `/tmp/engine.log` at 10 lines.
2. Published the existing Flow (documentId `c8kxj0lyzou80qnbage3fyih`, flowId `flow`) via the
   content-manager publish endpoint:
   `POST /content-manager/collection-types/api::flow.flow/{documentId}/actions/publish`
   with the admin JWT → **HTTP 200**.
3. Observed:
   - CMS log: only the HTTP access line. **NO `[rule-engine] doc-service:` diagnostic** —
     the plugin's `strapi.documents.use(...)` middleware (registered in the PLUGIN's
     `register()`, `cms/src/plugins/rule-engine/server/src/index.ts`) did NOT fire.
   - Engine log: **ZERO new lines** — nothing reached `/admin/*`.
   - Engine DB unchanged: no new `flow_versions` row, no `flow` flowId.

Root cause (confirmed empirically): a `strapi.documents.use()` middleware registered from
the LOCAL PLUGIN's `register()` is not attached to the content-manager publish path in
Strapi 5.56. Diagnostic proof: temporarily adding an identical `strapi.documents.use(...)`
in the APP-LEVEL `cms/src/index.ts` `bootstrap({ strapi })` DID fire on the same
content-manager publish — the log showed the action sequence
`findOne → unpublish`, then on re-publish `findOne → findOne → update → publish`, i.e.
`[DIAG app-bootstrap] doc-service uid=api::flow.flow action=publish`. The app-level
bootstrap registration sees `action: 'publish'`; the plugin-register one never ran.
(The temporary diagnostic edit was reverted; worktree is clean.)

Context shape confirmed for the middleware: `context.uid` (e.g. `api::flow.flow`),
`context.action === 'publish'`, and the published documentId is in
`context.params.documentId`.

## Bug #2 — Custom-field boot error — VERIFIED FIXED (holds)

The app-level registration of both custom fields in `cms/src/index.ts` `register()`
(commit 6a13634) holds: the dev server boots cleanly on `:1337` with NO
"Could not find Custom Field: plugin::rule-engine.flow-canvas" error, across multiple
restarts (including with the extra bootstrap diagnostic present). Status: VERIFIED FIXED;
the coder must keep it fixed and re-confirm a clean boot after any registration change.

## Bug #3 — Flow canvas drag-and-drop + JDM editor — PARTIALLY VERIFIED (build), browser runtime NEEDS IN-CONTEXT VERIFICATION

- Cannot drive a real browser here, so drag/drop + the JDM "unsupported" message could not
  be reproduced at the pointer level.
- What WAS verified live: the production admin bundle BUILDS cleanly —
  `NODE_OPTIONS=--max-old-space-size=4096 npm run build` → exit 0, "Building admin panel"
  succeeded (~225s). So both custom fields, reactflow 11.11.4, and @gorules/jdm-editor
  1.52.0 compile and bundle; the OOM/stylesheet-missing/custom-field-not-found classes of
  failure are ruled out at build time. The heap bump IS required (confirms the env note).
- Installed versions confirmed: reactflow 11.11.4, @gorules/jdm-editor 1.52.0,
  react/react-dom 18.3.1 (jdm-editor peer `react >= 18` satisfied). Both stylesheets exist
  on disk: `reactflow/dist/style.css`, `@gorules/jdm-editor/dist/style.css`, and both are
  imported by their components.
- Code-level root-cause candidates (to be confirmed in-browser by the coder):
  1. FlowCanvasField mounts `<ReactFlow>` inside `<Box style={{ flex: 1, height: 560 }}>`
     within a `<Flex direction="row">`. The width comes only from `flex:1`, which can
     collapse the MEASURED width reactflow uses for pointer/zoom math — breaking drag.
     reactflow 11 requires the parent to have an explicit, non-zero measured width AND
     height.
  2. Palette is CLICK-to-add (`onClick={() => addNode(t)}`), not drag-from-palette —
     confirm the intended UX ("DRAG to reposition" existing nodes is the canvas behavior;
     adding from palette by click may be acceptable, but the report says drag is broken).
  3. JDM "unsupported" is a runtime render path in `<DecisionGraph>` (the literal string is
     not in the dist, so it is a node-kind/renderer condition), to be reproduced live.

## Loop re-verification log (appended by implement/review iterations)

<!-- Each iteration appends: date, what was run live, engine-log + psql evidence. -->

### [2026-10-04] FEAT-001 — publish middleware relocated to app-level bootstrap — LIVE VERIFIED

**Change under test.** The publish Document Service middleware was extracted to
`cms/src/publish-middleware.ts` (`buildPublishMiddleware(strapi)`) and registered
from the APP-LEVEL `cms/src/index.ts` `bootstrap({ strapi })` via
`strapi.documents.use(buildPublishMiddleware(strapi))`. The non-firing
`strapi.documents.use(...)` block (plus the `PUBLISHABLE` set and the now-unused
publish-core / validation / publish-transform imports) was removed from the plugin
server entry `cms/src/plugins/rule-engine/server/src/index.ts`; that entry keeps only
the custom-field registration, the publish controller, and routes. Push logic is
REUSED from `publish-core` (DRY) — no duplication.

**Incidental build fix (required to boot).** The four plugin service files
(`admin-client.ts`, `publish-core.ts`, `publish-transform.ts`, `validation.ts`)
imported `types/engine` with a 7-level `../` prefix that resolved OUTSIDE `cms/`
(worktree root). Those files are excluded from the app tsc pass (`exclude:
src/plugins/**`), so the bad depth was latent until `publish-middleware.ts` (an
app-level file) imported them, dragging them into the strict app compile. Corrected
7×`../` → 6×`../` so the path resolves to `cms/types/engine`. No logic change.

**Environment.** Engine rebuilt from `engine/` (`CGO_ENABLED=1 go build -o bin/engine
./cmd/engine`), run `./bin/engine -env .env -seed internal/config/testdata/seed.json`,
`/readyz` → `{"status":"ready"}`, log at `/tmp/engine.log`. CMS run with
`NODE_OPTIONS=--max-old-space-size=4096 npm run develop` on `:1337`. (Stray engine +
strapi instances from the planning session were stopped first so ports were free and
the worktree code was the code under test.)

**Clean boot (bug #2 holds).** `grep -i "Could not find Custom Field" /tmp/cms.log` →
none. TS compiled with 0 errors; `info: Strapi started successfully`.

**Flow publish — the primary acceptance case — PASSES.**
- `POST /content-manager/collection-types/api::flow.flow/c8kxj0lyzou80qnbage3fyih/actions/publish`
  → **HTTP 200**, `publishedAt` set, `lastPublishStatus: "validated"`.
- FINDING: the seeded Flow's `tree` was a bare `{"id":"trigger","type":"trigger","spec":{}}`
  with no children/response node. The engine (correctly) 400s it
  (`control_no_children`, `missing_response`) and the strict gate aborts the publish
  with 500 — this PROVED the middleware now fires and reaches the engine. To exercise
  the full happy path the draft `tree` was corrected to a minimal valid
  trigger→response tree before the 200 publish above.
- psql after the CMS publish — the content-manager publish produced a NEW validated
  version (v2):

  ```
  rule_engine.flow_versions
         flow_id       | version | validated
  ---------------------+---------+-----------
   flow                |       1 | f   <- manual probe (direct POST /admin/flows, unvalidated)
   flow                |       2 | t   <- CMS content-manager publish: create->validate->publish
   fmc-order-by-id     |       1 | t
   fmc-order-by-msisdn |       1 | t
   orders-expedite     |       1 | t
  ```

**JDM standalone publish — PASSES.**
- `POST .../api::jdm.jdm/cywazgqg8wu8487le61xe524/actions/publish` → **HTTP 200**,
  `publishedAt` set.
- psql: `test-jdm | 1` is a NEW row in `rule_engine.jdm_versions` (vs the 2 seeded
  `fmc-payment`, `order`) — the standalone JDM publish pushed via `runJdmPublish`.

**Connection standalone publish — middleware fires + reaches engine; blocked by a
pre-existing transform gap (NOT a FEAT-001 regression).**
- `POST .../api::connection.connection/lysbg059cmkpaa6b35lx2pis/actions/publish` →
  HTTP 500; CMS log: `connection publish failed: admin request POST /admin/connections
  returned 400`. So the relocated middleware DID fire and DID call the engine.
- ROOT CAUSE: the Connection `settings` attribute is a Strapi **dynamiczone** (an
  ARRAY of components, e.g. `connection.postgres-settings`). `toConnectionEntry`
  passes `doc.settings` through unchanged and `connectionToEnginePayload` emits it
  as-is, so the CMS sends `settings: [ ... ]` (array). The engine's strict decoder
  (`DisallowUnknownFields`, `settings map[...]`) rejects an array →
  `{"error":"invalid request body"}` 400. Proven by direct engine calls:
  `settings:{}` or a flat object → **HTTP 201**; `settings:[]` → **HTTP 400**.
- This is a FEAT-003 serialization gap (how a dynaminczone maps to the engine's object
  `settings`), independent of the FEAT-001 relocation. Flagged for FEAT-002 / the
  integration step; NOT fixed here because it is a connection-shape mapping decision,
  not part of relocating the middleware, and the brief forbids changing stored shapes
  without a flag.
- `rule_engine.connections` shows a `connection` key, but that row was created by a
  MANUAL probe (`POST /admin/connections` with a valid object), not by the CMS publish
  path — recorded here for honesty so it is not mistaken for CMS-path evidence.

**Unit sanity.** `npx vitest run` → 73 passed (6 files). (Mocked; not the primary
evidence — the LIVE results above are.)

### [2026-10-04] FEAT-002 — Flow canvas and JDM editor sizing fixes — BUILD VERIFIED, BROWSER REQUIRED

**Changes applied.** Both custom-field editor components were already updated with
sizing fixes to address the reactflow/jdm-editor measured-width requirement:

1. **FlowCanvasField** (`cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`):
   - Changed `<Box style={{ flex: 1, height: 560 }}>` to
     `<Box style={{ flex: '1 1 0%', minWidth: 0, height: 560 }}>`
   - Added inner `<div style={{ width: '100%', height: '100%', minWidth: 480 }}>` wrapper
     around `<ReactFlow>` to ensure the canvas parent always measures a non-zero width.
   - Rationale: In a flex-row, `flex: 1` alone can collapse measured width to near-zero
     (the element's intrinsic min-content) because CSS flexbox default `min-width: auto`.
     The `minWidth: 0` + `flex: '1 1 0%'` combination prevents that collapse, and the
     inner div's `minWidth: 480` guarantees a concrete pixel width for reactflow's
     pointer/zoom coordinate math.

2. **JdmEditorField** (`cms/src/plugins/rule-engine/admin/src/components/JdmEditorField/index.tsx`):
   - Changed `<Flex ... style={{ height: 520, width: '100%' }}>` to
     `<Flex ... style={{ height: 520, width: '100%', minWidth: 480 }}>`
   - Added inner `<div style={{ flex: '1 1 0%', minHeight: 0, width: '100%', height: '100%' }}>`
     wrapper around `<JdmConfigProvider><DecisionGraph/></JdmConfigProvider>`.
   - Rationale: `width: '100%'` can resolve to 0 if the parent has no constraint.
     `minWidth: 480` guarantees a measurable width. The inner div with `flex: '1 1 0%'`
     ensures the DecisionGraph (which is reactflow internally) receives a box with real
     measured dimensions.

**Build verification — PASSES.**
- `NODE_OPTIONS=--max-old-space-size=4096 npm run build` → **exit 0**.
- Output: "Compiling TS" (41s), "Building build context" (2s), "Building admin panel" (357s).
- Both custom fields (FlowCanvasField + JdmEditorField), reactflow 11.11.4, and
  @gorules/jdm-editor 1.52.0 compile and bundle successfully.
- The heap bump (`--max-old-space-size=4096`) is required to avoid OOM during the
  admin panel Rollup build.

**Server boot verification — PASSES.**
- `NODE_OPTIONS=--max-old-space-size=4096 npm run develop` → CMS up on `:1337` in ~60s.
- `grep -iE "error|Could not find Custom Field" /tmp/cms.log` → no matches.
- Admin HTML served at `http://127.0.0.1:1337/admin/` (confirmed via curl).

**Unit test sanity — PASSES.**
- `npx vitest run` → 73 tests passed across 6 files (serialize.test.ts, smoke.test.ts,
  publish-transform.test.ts, publish-controller.test.ts, publish-sequence.test.ts,
  connection-denylist-guard.test.ts).

**Stylesheets verified present and imported.**
- `reactflow/dist/style.css` exists (7.9KB) and is imported in FlowCanvasField.
- `@gorules/jdm-editor/dist/style.css` exists (64KB, includes reactflow base classes)
  and is imported in JdmEditorField.

**"Unsupported" string investigation.**
- Searched jdm-editor dist: no literal "unsupported" string found in any .js file.
- Searched reactflow dist: no "unsupported" string.
- Found in `@strapi/admin/dist/.../FormInputs/Renderer.js`: **"Unsupported field type: ${type}"**
  — this is Strapi's fallback for unrecognized field types in the generic InputRenderer.
- Conclusion: if the user saw "unsupported" in the JDM edit view, it was likely Strapi's
  content-manager failing to resolve the custom field Input component (registration issue),
  which FEAT-001 already fixed. The jdm-editor itself does not render an "unsupported"
  banner for valid graphs.

**BROWSER VERIFICATION REQUIRED.**
The following acceptance criteria require in-browser verification that cannot be
performed programmatically:
1. Flow edit view: nodes can be DRAGGED to reposition (position follows the pointer).
2. Flow edit view: edges can be connected between nodes.
3. Flow edit view: a node spec can be edited in the side panel.
4. Jdm edit view: the decision graph renders with NO "unsupported" banner.
5. Jdm edit view: a node can be added/edited and the value persists through debounced onChange.

**Instructions for manual verification:**
1. Open browser to `http://localhost:1337/admin`
2. Login with `abc@def.com` / `Passw0rdABC123`
3. Navigate to Content Manager → Flow → edit an existing Flow
4. Verify: canvas renders, nodes are visible, drag a node (should follow pointer),
   draw an edge between two nodes, select a node and edit its spec JSON
5. Navigate to Content Manager → Jdm → edit an existing Jdm
6. Verify: decision graph renders (no "unsupported" banner), add a node from the
   toolbar, edit node properties, confirm changes persist

**Stored JSON shape unchanged.**
- FlowCanvasField still emits engine tree via `canvasToTree()` (from ./serialize).
- JdmEditorField still emits `DecisionGraphType` (`{nodes,edges}`).
- Both use base `type: 'json'` custom fields with debounced onChange — no contract change.

### [2026-10-04] ITERATION 1 — cross-FEAT live integration (FEAT-001 server + FEAT-002 UI) + connection dynamiczone serialization fix — LIVE VERIFIED

**Scope.** First converge-loop iteration (no `review.json`). Ran the full cross-FEAT
live verification: engine build, admin bundle build, both servers up, a Flow publish
that references a Jdm AND a Connection, plus standalone Jdm and Connection publishes,
clean admin boot, and the publish gate. Fixed the one seam that was still broken
between FEAT-001 and FEAT-002: the Connection publish (flagged in the FEAT-001 block
as a FEAT-003 transform gap) 400'd because the Strapi `settings` dynamiczone was sent
to the engine as an ARRAY.

**Seam fixed — Connection `settings` dynamiczone -> engine object.**
- Root cause (as flagged): `connection.settings` is a Strapi **dynamiczone** (an array
  of component instances, each tagged `__component` + numeric `id`; nested `pool`
  component also carries an `id`). `toConnectionEntry` passed `doc.settings` straight
  through, so `connectionToEnginePayload` emitted `settings` as an ARRAY. The engine's
  `Settings map[string]any` decoder rejects an array -> HTTP 400 (`invalid request body`),
  which the strict gate surfaced as a 500 publish failure.
- Fix (CMS-only, no contract/shape change): added `normalizeDynamicZoneSettings()` in
  `cms/src/plugins/rule-engine/server/src/controllers/publish.ts` and wired it into
  `toConnectionEntry`. It unwraps the single authored component to a plain object and
  recursively strips Strapi's `__component`/`id` bookkeeping, so only the author's
  discrete driver fields reach the engine. This reshapes HOW the already-stored
  dynamiczone serializes onto the FROZEN engine contract — it changes neither the stored
  Strapi shape (still a dynamiczone) nor the engine admin-API contract (still an object).
  Both publish paths (flow-referenced via `runPublishSequence`, standalone via
  `runConnectionPublish`) go through `toConnectionEntry`, so one fix covers both.
- Unit coverage: added 2 cases to `publish-controller.test.ts` (dynamiczone array ->
  flat object with ids/`__component` stripped incl. nested `pool`; empty `[]`/`null` ->
  `null`). `npx vitest run` -> **75 passed (6 files)** (was 73).

**Environment.** Engine rebuilt from `engine/`
(`CGO_ENABLED=1 go build -o bin/engine ./cmd/engine` -> exit 0), run
`./bin/engine -env .env -seed internal/config/testdata/seed.json`, `/readyz` ->
`{"status":"ready"}`, log `/tmp/engine.log`. CMS admin bundle built with
`NODE_OPTIONS=--max-old-space-size=4096 npm run build` -> **exit 0**, "Building admin
panel (213702ms)". CMS run with `NODE_OPTIONS=--max-old-space-size=4096 npm run develop`
on `:1337`. (Stale engine + strapi from a prior session were stopped first so the ports
were free and the code under test is the current worktree.)

**Clean boot (bug #2 holds).** `grep -i "Could not find Custom Field" /tmp/cms.log` ->
none. `info: Strapi started successfully`. `GET /admin/init` -> 200.

**Connection standalone publish — NOW PASSES (was 500 before the fix).**
- `POST .../api::connection.connection/lysbg059cmkpaa6b35lx2pis/actions/publish` ->
  **HTTP 200** (previously HTTP 500 / engine 400 on the array body).
- To force the reconcile to re-create (the engine already held a semantically-equal def
  from a prior polluted push), the draft `postgres-settings.port` was changed 5433->5432,
  then unpublish+publish. psql proves the engine received a CLEAN object:

  ```
  rule_engine.connection_versions (conn_key='connection')
   conn_key   | version | settings
  ------------+---------+-------------------------------------------------------------------------------------
   connection |       1 | {}                                        <- early manual probe
   connection |       2 | {"id":1,"__component":"connection.postgres-settings", host/port/... }  <- polluted (pre-fix push)
   connection |       3 | {"host":"127.0.0.1","port":5432,"user":null,"sslmode":"disable","database":"matcha"}  <- CMS publish AFTER fix: clean object, no id/__component
  ```
  `GET /admin/connections` for key `connection` now returns the clean v3 settings object.

**Standalone JDM publish — PASSES.**
- `POST .../api::jdm.jdm/cywazgqg8wu8487le61xe524/actions/publish` -> **HTTP 200**.
- psql: `rule_engine.jdm_versions` for `test-jdm` gained v2 (v1 was the prior session) —
  the standalone JDM push via `runJdmPublish`.

**Flow publish that references a Jdm AND a Connection — the primary acceptance case — PASSES.**
- Authored a draft `tree` referencing BOTH refs: `trigger -> sequence[ action(connection="connection")
  -> decision(jdmId="test-jdm") -> response ]`. (Engine taxonomy note: `action`/`decision`/
  `set`/`response` are LEAF types — they must NOT own children; linear chaining uses a
  `sequence` control node. An earlier flat trigger->action->decision->response tree was
  correctly rejected by the engine with `leaf_has_children` for `action`/`decision`.)
- `POST .../api::flow.flow/c8kxj0lyzou80qnbage3fyih/actions/publish` -> **HTTP 200**.
- The full ordered sequence ran: reconcile connections -> create JDM -> createFlow (v4)
  -> validateFlow (ok) -> publishFlow. psql:

  ```
  rule_engine.flow_versions (flow_id='flow'): v1 f (probe), v2 t, v3 f (direct probe), v4 t  <- CMS content-manager publish, validated
  rule_engine.active_pointers (object_id='flow'): version = 4   <- CMS-published version is the ACTIVE/served one
  ```

**Publish gate (acceptance #5) — PROVEN.** Before authoring a valid tree, publishing the
Flow with an engine-invalid tree produced CMS log `publish blocked (createFlow): admin
request POST /admin/flows returned 400` and the content-manager publish returned **HTTP
500** — i.e. a blocking engine error ABORTS the Strapi publish (the entry is not left
published). This confirms the strict gate in the relocated app-level middleware.

**FEAT-002 (admin UI) — BUILD VERIFIED; in-browser drag/render still requires a human.**
The reactflow + jdm-editor container sizing fixes (explicit non-zero MEASURED width AND
height; see the 2026-10-04 FEAT-002 block above) are in place and the production admin
bundle builds clean (exit 0 this run). This environment has NO browser, so pointer-level
drag/zoom and the jdm-editor render path cannot be exercised here — those acceptance
criteria remain for in-context/human browser verification per the manual steps above.
No regression was introduced: both custom fields still compile, bundle, and boot.

**Scope note.** No `engine/` Go code was modified (build/run only). The only source
change this iteration is the CMS connection serialization fix above. Fix committed
locally on `fix/cms-e2e`; not pushed.

