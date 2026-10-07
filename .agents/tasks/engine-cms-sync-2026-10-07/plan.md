# Implementation Plan — Engine ↔ CMS Sync (webhook/schedule/group publish + flow-canvas drag fix)

All work happens in the worktree:
`/home/nuzirwan/project/rule-engine-api/.worktrees/engine-cms-sync` (NOT the main repo).

Build/test commands (confirmed from `cms/package.json` + `cms/vitest.config.ts`):
- CMS build: `cd <worktree>/cms && npm run build` (expect exit 0, "Building admin panel" succeeds).
- CMS unit/integration tests: `cd <worktree>/cms && npm run test` (vitest; scoped to
  `src/plugins/rule-engine/server/tests/**` and `admin/src/**/*.test.ts`). Tests mock the
  engine admin API with **nock** via the `tests/fixtures/http-fetch.ts` shim — engine need not run.
- Engine build (only if any engine `.go` file is touched — expected NOT to be): 
  `cd <worktree>/engine && CGO_ENABLED=1 go build ./...`.

---

## Confirmed engine admin-API contracts (read from code, not paraphrase)

### Group — `engine/internal/httpapi/admin_groups.go` + `engine/internal/config/group.go`
- Routes (`admin.go`): `PUT /admin/groups/{id}`, `GET /admin/groups/{id}`, `GET /admin/groups`,
  `DELETE /admin/groups/{id}`.
- PUT body (`putGroupRequest`): FLAT `{ env?, name, description?, connections?: string[],
  scaling: ScalingConfig, enabled?: bool, reason? }`. Group id comes from the URL path, NOT the body.
- `ScalingConfig` (nested): `{ mode: "static"|"dynamic"|"ephemeral", minReplicas: int,
  maxReplicas: int, scaleDownDelay?: string (e.g. "5m"), startupTimeout?: string (e.g. "30s"),
  resources?: { cpuRequest, cpuLimit, memoryRequest, memoryLimit } }`.
  Field name is `mode`, NOT `scalingMode`. Durations are **Go duration strings**, not integers.
- `GET /admin/groups` returns `{ groups: GroupSummary[] }` where `GroupSummary` = `{ id, name,
  enabled, version, updatedAt }` (NO scaling detail — summaries only).
- `GET /admin/groups/{id}` returns the full `Group` (nested `scaling`, `connections: string[]`).
- Validation (`ValidateGroup`): id regex `^[a-z][a-z0-9-]*$`, name required, mode in enum,
  minReplicas>=0, **maxReplicas>=minReplicas**, static ⇒ minReplicas>0.

### CMS group shape (FLAT) — `cms/src/api/group/content-types/group/schema.json`
`groupId` (uid), `name`, `description`, `enabled`, `scalingMode`, `minReplicas`, `maxReplicas`,
`scaleDownDelaySeconds` (int, min 60), `startupTimeoutSeconds` (int, min 5),
`resources` (component `config.resource-limits` → `{cpuRequest,cpuLimit,memoryRequest,memoryLimit}`),
`connections` (manyToMany relation to `api::connection.connection`, `inversedBy: groups`).

**Transform (flat CMS → nested engine):**
- `scalingMode` → `scaling.mode`
- `minReplicas`/`maxReplicas` → `scaling.minReplicas`/`scaling.maxReplicas`
- `scaleDownDelaySeconds` (int) → `scaling.scaleDownDelay` = `"${n}s"` (duration string)
- `startupTimeoutSeconds` (int) → `scaling.startupTimeout` = `"${n}s"` (duration string)
- `resources` component → `scaling.resources` (strip Strapi `id`/`__component`; reuse the same
  id-stripping approach as `normalizeDynamicZoneSettings` in `controllers/publish.ts`)
- `connections` relation → `connections: string[]` of connection `key`s (populate the relation,
  map each to its `key`)
- `groupId` → URL path segment (NOT in body)

### Webhook — `engine/internal/httpapi/webhook_admin.go`
- Routes: `POST /admin/webhooks` (create → returns `{id, version}`), `PUT /admin/webhooks/{id}`,
  `DELETE /admin/webhooks/{id}`, `POST /admin/webhooks/{id}/publish` (`{version}` → activates),
  `GET /admin/webhooks/{id}`, `GET /admin/webhooks` (`{webhooks: WebhookSummary[]}`).
- **Create is two-step like flows: create (inactive version) then publish(version) to activate.**
- `createWebhookRequest` struct JSON tags (DisallowUnknownFields): **`id`** (NOT `webhookId`),
  `name` (required), `secretRef` (required), `provider` (default generic), `flowId` (required),
  `mapping: map[string]string`, `filter: map[string][]string`, `env`.
- **CONTRACT BUG to fix (see Assumptions §A1):** the CMS `AdminClient.createWebhook` + the
  `CreateWebhookRequest` type in `cms/types/engine.ts` send `webhookId`, and `mapping`/`filter`
  as arrays-of-objects. The engine expects top-level `id` and `mapping`/`filter` as **maps**.
  The existing `webhook-admin.test.ts` only asserts against a nock mock, so it never caught this.

### CMS webhook shape — `cms/src/api/webhook/content-types/webhook/schema.json`
`webhookId` (uid), `name`, `secretRef`, `provider` enum, `flowId` (manyToOne relation → flow),
`mapping` (repeatable `webhook.mapping-field` = `{sourceJsonPath, targetContextPath}`),
`filter` (repeatable `webhook.filter-field` = `{jsonPath, allowedValues: json/string[]}`),
`environment` (relation), `engineVersion` (int), `lastSyncStatus` enum `none|synced|failed`.

**Transform (CMS → engine create body):**
- `webhookId` → body `id`
- `flowId` relation → engine `flowId` string (populate relation, read `.flowId`)
- `mapping[]` `{sourceJsonPath, targetContextPath}` → `map[string]string` `{ [sourceJsonPath]: targetContextPath }`
- `filter[]` `{jsonPath, allowedValues}` → `map[string][]string` `{ [jsonPath]: allowedValues }`
- `secretRef`, `provider`, `name` pass through
- after create returns `{version}`, call `publishWebhook(id, version)` to activate, then write back
  `engineVersion = version` and `lastSyncStatus = 'synced'` (or `'failed'`).

### Schedule — `engine/internal/httpapi/schedule_admin.go`
- Routes: `POST /admin/schedules`, `PUT /admin/schedules/{id}`, `DELETE /admin/schedules/{id}`,
  `GET /admin/schedules/{id}`, `GET /admin/schedules` (`{schedules: ScheduleSummary[]}`),
  `POST /admin/schedules/{id}/run`, `GET /admin/schedules/{id}/runs`.
- Schedule is a **mutable CRUD entity** (not versioned). `createScheduleRequest`: `id` (required,
  regex `^[a-z0-9][a-z0-9\-_]*$`), `name` (required, <=256), `schedule` (cron, required),
  `timezone` (default UTC, validated IANA via `time.LoadLocation`), `flowId` (required),
  `input`, `enabled`, `env` (must be "" or match server env). Engine validates cron
  (`scheduler.ParseSchedule`) on create.
- No separate publish step. Reconcile: `GET /admin/schedules/{id}` → 404 ⇒ `createSchedule`,
  else ⇒ `updateSchedule`.

### CMS schedule shape — `cms/src/api/schedule/content-types/schedule/schema.json`
`scheduleId` (uid), `name`, `schedule` (string <=128), `timezone` (default UTC), `flowId` (relation),
`input` (json), `enabled`, `environment` (relation), `lastRun`/`nextRun` (datetime),
`lastSyncStatus` enum. NOTE: `draftAndPublish: false` on schedule — see Assumptions §A2.

### AdminClient methods to reuse vs add — `cms/.../services/admin-client.ts`
- REUSE (exist): `createWebhook`, `updateWebhook`, `deleteWebhook`, `getWebhook`, `listWebhooks`;
  `createSchedule`, `updateSchedule`, `deleteSchedule`, `getSchedule`, `listSchedules`,
  `triggerScheduleRun`; `listConnections`, `createConnection`, `getConnection`.
- FIX: `createWebhook` must send `id` + map-shaped `mapping`/`filter` (and `CreateWebhookRequest`
  type). Confirm `createSchedule`/`updateSchedule` already send a flat body with `env` (they pass
  the request object straight through — the schedule create body currently omits `env`; the engine
  accepts empty/omitted env, so this is OK, but the transform should not inject a non-empty env).
- ADD (none exist for group): `createGroup`/`upsertGroup` (`PUT /admin/groups/{id}`),
  `getGroup` (`GET /admin/groups/{id}`), `listGroups` (`GET /admin/groups`),
  `deleteGroup` (`DELETE /admin/groups/{id}`); add `publishWebhook` (`POST /admin/webhooks/{id}/publish`).
- ADD group + webhook-publish TS types to `cms/types/engine.ts` (`ScalingConfig`, `GroupPutRequest`,
  `EngineGroup`, `GroupSummary`, `ListGroupsResponse`, `PublishWebhookResponse`).

### Publish wiring — `cms/src/publish-middleware.ts` + `cms/src/index.ts`
- `PUBLISHABLE` set currently `{api::flow.flow, api::jdm.jdm, api::connection.connection}`.
  Add `api::webhook.webhook`, `api::schedule.schedule`, `api::group.group`.
- For each new uid, add a branch mirroring the jdm/connection branches: load the doc (populate
  relations/components), run `runWebhookPublish/runSchedulePublish/runGroupPublish`, then `next()`.
- `index.ts` needs no change beyond the middleware already being registered (the new logic lives
  in publish-core, which the middleware imports). See Assumptions §A2 re: schedule draftAndPublish.

### Flow-canvas drag bug — `cms/.../admin/src/components/FlowCanvasField/index.tsx`
Root cause (read in full): there is NO HTML5 drag-and-drop from the palette; the palette uses an
`addNode(type)` onClick that appends a node directly. Separately, `nodesDraggable`/`nodesConnectable`
are gated behind `interactionEnabled`, which only flips true on `onPaneClick`/`onNodeClick` — so a
freshly added node cannot be dragged on the canvas until the user first clicks the pane/node. "Cannot
drag flow nodes onto the canvas" = the intended palette→canvas drag UX was never implemented, and the
gating further blocks dragging existing nodes. Fix: implement onDragStart (palette buttons set
`dataTransfer`), onDragOver/onDrop (canvas, using `screenToFlowPosition` from `useReactFlow`, wrapped in
`ReactFlowProvider`), add the dropped node + `reserialize`; and remove/relax the `interactionEnabled`
gating so nodes are draggable immediately (keep `nodesDraggable`/`nodesConnectable` = `!disabled`).
`@xyflow/react` v12.12.0 (confirmed in `cms/package.json`) — `screenToFlowPosition` + `useReactFlow` +
`ReactFlowProvider` are v12 APIs.

---

## Ordered implementation plan

- [ ] 1. Add group + webhook-publish types to the shared engine types and new AdminClient methods.
      Add `ScalingConfig`, `EngineResourceLimits`, `GroupPutRequest`, `EngineGroup`, `GroupSummary`,
      `ListGroupsResponse`, `PublishWebhookRequest/Response` to `cms/types/engine.ts`. Fix
      `CreateWebhookRequest` to use `id` and map-shaped `mapping`/`filter`. Add `AdminClient.upsertGroup`,
      `getGroup`, `listGroups`, `deleteGroup`, `publishWebhook`, and fix `createWebhook` body shape.
      Files: `cms/types/engine.ts`, `cms/src/plugins/rule-engine/server/src/services/admin-client.ts`.
      Verify: `cd <worktree>/cms && npm run test` — existing suites still pass; update
      `webhook-admin.test.ts` expectations to the corrected `id`/map body in step 2.

- [ ] 2. Add pure transforms + publish-core runners for webhook, schedule, group.
      In `publish-transform.ts` add `webhookToEnginePayload` (webhookId→id, flowId relation→string,
      mapping[]→map, filter[]→map), `scheduleToEnginePayload`, `groupToEnginePayload` (flat→nested
      ScalingConfig, seconds→"Ns" duration strings, resources strip ids, connections→key[]). In
      `publish-core.ts` add `runWebhookPublish` (create→publishWebhook→write back engineVersion +
      lastSyncStatus), `runSchedulePublish` (GET→404?create:update→write back lastSyncStatus/lastRun),
      `runGroupPublish` (populate connections/resources→upsertGroup). Reuse `clientForEntry` for the
      per-entry environment. Update `webhook-admin.test.ts` to assert `id` + map shapes.
      Files: `cms/.../server/src/services/publish-transform.ts`, `publish-core.ts`,
      `cms/.../server/tests/webhook-admin.test.ts`.
      Verify: `cd <worktree>/cms && npm run test`.

- [ ] 3. Extend publish wiring to webhook/schedule/group.
      Add `api::webhook.webhook`, `api::schedule.schedule`, `api::group.group` to `PUBLISHABLE` and add
      a branch per uid in `buildPublishMiddleware` mirroring the jdm/connection pattern (load doc with
      relations/components populated, call the matching runner, map TransformError/AdminApiError to a
      thrown publish-blocked message, then `next()`).
      Files: `cms/src/publish-middleware.ts`.
      Verify: `cd <worktree>/cms && npm run test` && `npm run build`.

- [ ] 4. Validation parity (findings §C).
      Add cron-expression + IANA-timezone validation for schedule (in the schedule transform/runner,
      throwing TransformError before any admin call); add `HEAD`/`OPTIONS` to the flow method enum in
      `cms/src/api/flow/content-types/flow/schema.json`; enforce `maxReplicas >= minReplicas` in the
      group transform (throw TransformError when violated). Prefer a lightweight cron validator (regex
      for 5-field + `@alias`/`@every`) rather than adding a dependency — see Assumptions §A3.
      Files: `publish-transform.ts` (or a new `validation-parity.ts`), `flow/schema.json`.
      Verify: `cd <worktree>/cms && npm run test` && `npm run build`.

- [ ] 5. Reverse sync for webhook/schedule/group in sync.ts.
      Extend `SyncStatusResponse`, `syncStatus`, `importAll`, `importOne`, and `VALID_SYNC_TYPES` to
      cover webhooks/schedules/groups (list from engine via the new/existing client methods, diff vs
      CMS docs, and import engine→CMS reconciling nested ScalingConfig→flat on the group import side,
      "Ns" duration→seconds, and map→array for webhook mapping/filter).
      Files: `cms/.../server/src/controllers/sync.ts`; extend `tests/sync.test.ts`.
      Verify: `cd <worktree>/cms && npm run test`.

- [ ] 6. Admin UI: register pages + Publish/Sync actions.
      Register `WebhooksPage` in `admin/src/index.tsx` routes and migrate its layout imports from the
      deprecated `@strapi/design-system` `HeaderLayout`/`ContentLayout` to `Layouts.*` from
      `@strapi/admin/strapi-admin` (matching `SyncPage.tsx`) — this is the likely NEW build error.
      Add a Publish/Sync button that POSTs to a new plugin route that triggers the entity publish.
      Add minimal Schedules + Groups pages following the SyncPage/EnvironmentsPage pattern; at minimum
      register them and wire a Publish/Sync action. Add the backing plugin routes/controllers
      (`POST /webhooks/:id/publish`, `/schedules/:id/publish`, `/groups/:id/publish`) reusing the
      runners from step 2.
      Files: `admin/src/index.tsx`, `admin/src/pages/WebhooksPage.tsx`, new `SchedulesPage.tsx`,
      `GroupsPage.tsx`, `server/src/routes/index.ts`, new/edited server controllers.
      Verify: `cd <worktree>/cms && npm run build` (exit 0, admin panel builds).

- [ ] 7. Fix the flow-canvas drag bug.
      Wrap the canvas in `ReactFlowProvider`; add palette `onDragStart` (set `dataTransfer` nodeType),
      canvas `onDragOver` + `onDrop` using `useReactFlow().screenToFlowPosition`, append the node and
      call `reserialize`; relax the `interactionEnabled` gating so `nodesDraggable`/`nodesConnectable`
      are `!disabled`. Keep the existing `addNode` button path working. Add a component/unit test if a
      harness exists (jsdom), else a `serialize`-level test asserting a dropped node reserializes into
      the tree.
      Files: `admin/src/components/FlowCanvasField/index.tsx` (+ test).
      Verify: `cd <worktree>/cms && npm run build` && `npm run test`.

- [ ] 8. Full verification + cleanup.
      Run `cd <worktree>/cms && npm run build` (must exit 0) and `npm run test` (all green). Remove any
      temp files. If any engine `.go` file was touched, run `cd <worktree>/engine && CGO_ENABLED=1 go
      build ./...`.
      Verify: both commands succeed.

---

## Assumptions to verify during implementation (do not dismiss)

- **A1 — webhook `id` vs `webhookId` + map-shaped mapping/filter.** The engine create body uses
  top-level `id` and `map` mapping/filter; the current `AdminClient.createWebhook` and
  `webhook-admin.test.ts` use `webhookId` + arrays. Treat the engine Go struct as the source of
  truth and fix the client/types/test. Verify by reading `createWebhookRequest` tags (done) and by a
  nock assertion that the posted body has `id` and object-shaped `mapping`/`filter`.
- **A2 — schedule `draftAndPublish:false`.** The schedule content-type has `draftAndPublish:false`,
  so the Document Service `publish` action never fires for schedules (confirmed in findings §B/env
  note). The publish-middleware branch for `api::schedule.schedule` therefore will NOT be reached on
  a content-manager publish. Options: (a) flip schedule to `draftAndPublish:true` to match
  webhook/group, or (b) drive schedule sync only via the explicit plugin Publish/Sync action (step 6)
  and the reverse-sync import. **Recommended default: (b)** — do not change the draft/publish contract
  of an existing content-type as a side effect; wire schedule push through the explicit action +
  reverse sync, and still add the `PUBLISHABLE` branch so it works if the type is ever flipped.
  Confirm the current schema value before deciding.
- **A3 — cron validation without a new dependency.** The engine uses `scheduler.ParseSchedule`; the
  CMS has no cron library. Prefer a self-contained validator (5-field cron + `@hourly/@daily/...`
  aliases + `@every <dur>`) over adding a dependency, to keep the CMS build lean. If parity needs to
  be exact, flag it rather than silently diverging.
- **A4 — webhook/schedule `flowId` is a relation, not a scalar.** Both content-types model `flowId`
  as a manyToOne relation to `api::flow.flow`. The runner MUST populate the relation and read the
  target's `flowId` string. If the relation is empty, the engine rejects (flowId required) — surface
  that as a publish-blocked error, do not send an empty flowId.
- **A5 — group `connections` direction.** CMS `group.connections` is manyToMany `inversedBy groups`;
  engine wants `connections: []string` of connection keys. Populate and map to `key`. Confirm the
  connection content-type exposes `key` (it does — `connection` uid field).
