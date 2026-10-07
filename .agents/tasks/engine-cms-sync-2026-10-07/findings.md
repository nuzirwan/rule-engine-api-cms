# Engine ↔ CMS Sync — Findings Report

Date: 2026-10-07
Scope: Are the Go rule-engine and the Strapi CMS in sync, in two senses — (A) data/config sync on save, and (B) UI + logic parity?
Mode: Read-only investigation. No code was modified.

---

## Summary answer

**Partial sync, by design — with real drift gaps.**

1. **Save ≠ sync.** Saving a draft in the CMS (content manager edit, or a Strapi `update`) does **nothing** to the engine. Config reaches the engine **only on an explicit Strapi "Publish"** action, intercepted by an app-level Document Service middleware (`cms/src/publish-middleware.ts`). This is intentional (draft/publish is the contract per `architecture.md`), but it means a CMS entry can be edited and left in draft, so **CMS and engine can and will drift** until Publish runs. There is no reverse auto-sync either; `GET /sync/status` + `POST /sync/import` exist to detect/pull drift manually.

2. **Publish only covers 3 of 7 entities.** The publish middleware handles **flow, jdm, connection** only. **webhook, schedule, group** have NO publish path at all — they use default Strapi core controllers that write to Strapi's own DB and never call the engine. The engine admin API and the CMS `AdminClient` both fully support webhooks and schedules (create/update/delete/list), but **nothing in the CMS ever calls those client methods.** This is the single biggest drift risk.

3. **UI is thin and inconsistent.** The rule-engine admin plugin registers only Sync, Environments, FlowDetail, and Templates pages. `WebhooksPage` exists but is **not registered** in the plugin routes, and even if reached it only links out to the content manager — it exposes **no publish/sync action**. There is no Schedules or Groups page. Most operator CRUD happens through Strapi's generic content manager, not a rule-engine-aware UI.

4. **Field parity is close for flow/jdm/connection, with specific gaps**; webhook/schedule/group fields exist on both sides but are **not connected** by any sync code (see §2 tables). Circuit-breaker resilience fields and several group/schedule bookkeeping fields are engine-only.

---

## Evidence

### A. Save-sync mechanism

**The one and only push path is Publish, via an app-level middleware.**

- `cms/src/index.ts` `bootstrap()` registers `strapi.documents.use(buildPublishMiddleware(strapi))`. A comment there and in `cms/src/publish-middleware.ts` records that a middleware registered from the *plugin's* `register()` did NOT fire on the content-manager publish path in Strapi 5, so it was relocated to app level.
- `cms/src/publish-middleware.ts`: the middleware early-returns unless `context.action === 'publish'` AND `context.uid ∈ {api::flow.flow, api::jdm.jdm, api::connection.connection}`. So:
  - A plain **save/update** (draft) → `context.action` is `update`, not `publish` → middleware no-ops → **engine not touched**.
  - **Publish** of a flow → runs `runFlowPublish` (the full ordered sequence) before `next()`, persists write-backs after.
  - **Publish** of a jdm → `runJdmPublish`; of a connection → `runConnectionPublish`.
- `cms/src/plugins/rule-engine/server/src/services/publish-core.ts` + `validation.ts` implement the ordered flow sequence against the engine admin API: `listConnections` → reconcile `createConnection` for changed keys → `createJdm` per referenced JDM → `createFlow` (validated=false) → `validateFlow` (stored mode) → **block** on `ok:false`/404/5xx → `publishFlow`. All HTTP via `admin-client.ts`; the CMS never touches the engine DB. This matches `architecture.md`'s "Publish writes a clean engine-owned record" contract.
- There is also an explicit controller `POST /rule-engine/flows/:id/publish` (`controllers/publish.ts`, routed in `routes/index.ts`) that reuses the same core — a second, manual trigger for the same flow path.

**Drift confirmation.** Because only Publish pushes, and only for 3 types:
- Edit a flow/jdm/connection and don't publish → engine keeps the old active version (drift until publish).
- Edit/create a **webhook/schedule/group** and publish → still nothing reaches the engine (no path exists).
- `controllers/sync.ts` exposes `GET /sync/status` (diff CMS vs engine for flows/jdms/connections — **not** webhooks/schedules/groups) and `POST /sync/import[/:type/:id]` (pull engine → CMS). These are manual, operator-invoked reconciliation tools, confirming drift is expected and must be managed by hand.

### B. Entity-by-entity field parity

Engine fields = SQL migration columns (`engine/migrations/*.up.sql`) + admin DTO/structs (`engine/internal/config/*.go`, `engine/internal/httpapi/*.go`). CMS fields = `cms/src/api/*/content-types/*/schema.json` + components. "Synced?" = whether a publish/sync code path actually moves the field.

#### flow  — synced via publish middleware ✅
| Field | Engine | CMS schema | Notes |
|---|---|---|---|
| flowId / id | `flows.id` | `flowId` (uid) | match |
| method | `flows.method` enum (GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS via `knownMethods`) | `method` enum (GET/POST/PUT/PATCH/DELETE) | **CMS missing HEAD, OPTIONS** |
| path | `flows.path`, UNIQUE(method,path) | `path` (regex `^/[A-Za-z0-9/_{}.-]+$`) | match |
| tree | `flow_versions.tree` jsonb | `tree` custom field (flow-canvas) | match |
| fixtures | `flow_fixtures` | `fixtures` component (repeatable) | match; admin wire fixture richer (mocks/expect) down-mapped |
| group | `flow_versions.group_id` FK | `group` relation | match (but group itself not synced — see group row) |
| version/validated | engine-assigned, `validated` bool | `engineVersion`, `lastPublishStatus`, `lastValidation` | CMS mirrors engine write-backs |
| note | `flow_versions` note (0002 migration) | `note` | match |

#### jdm — synced via publish middleware ✅
| Field | Engine | CMS | Notes |
|---|---|---|---|
| jdmId / id | `jdms.id` | `jdmId` (uid) | match |
| doc | `jdm_versions.jdm` jsonb | `doc` custom field (jdm-editor) | match |
| version | engine auto-assign (payload always `version:0`) | `engineVersion` | match |
| note | — | `note` | CMS-only metadata |

#### connection — synced via publish middleware ✅ (with resilience gaps)
| Field | Engine | CMS | Notes |
|---|---|---|---|
| key | `connections.key` | `key` (uid) | match |
| type | postgres/valkey/rest | enum postgres/valkey/rest | match |
| settings | `connection_versions.settings` jsonb | `settings` dynamiczone (postgres/rest/valkey components) | match; CMS normalizes dynamiczone→object; **secret denylist enforced both sides** |
| secretRef | `connection_versions.secret_ref` | `secretRef` string | match; secret VALUES rejected on both sides |
| resilience.timeout | `ResiliencePolicy.Timeout` (ns) | `timeoutMs` → ns transform | match |
| resilience.retry.maxAttempts | `Retry.MaxAttempts` | `retry.maxAttempts` | match |
| resilience.retry.BaseBackoff / MaxBackoff | engine struct | **absent in CMS** | **engine-only** |
| resilience.breaker (FailureThreshold/FailureRatio/OpenTimeout) | engine struct | **absent in CMS** | **engine-only — operators cannot author circuit-breaker policy from CMS** |

#### webhook — NOT synced ❌ (fields exist on both sides, no code path)
| Field | Engine (`webhooks`/`webhook_versions`, `config.Webhook`) | CMS schema | Notes |
|---|---|---|---|
| id/webhookId, name, provider (stripe/github/generic), flowId, secretRef, mapping, filter, env, version | present | `webhookId`, `name`, `provider`, `flowId`, `secretRef`, `mapping`, `filter`, `environment`, `engineVersion`, `lastSyncStatus` | Field shapes line up |
| **sync** | admin API `POST/PUT/DELETE /admin/webhooks` + `AdminClient.createWebhook/updateWebhook/deleteWebhook` exist | — | **No caller. `lastSyncStatus` is displayed but never written by any publish path.** |

#### schedule — NOT synced ❌
| Field | Engine (`schedules`, `config.Schedule`) | CMS | Notes |
|---|---|---|---|
| id, name, schedule(cron), timezone, flowId, input, enabled, env | present | `scheduleId`, `name`, `schedule`, `timezone`, `flowId`, `input`, `enabled`, `environment` | Field shapes line up |
| lastRun/nextRun | engine computes | `lastRun`/`nextRun` datetime, `lastSyncStatus` | CMS has display fields |
| createdBy/updatedBy | engine-only | — | engine-only |
| **sync** | `AdminClient.createSchedule/updateSchedule/deleteSchedule/triggerScheduleRun` exist | — | **No caller. Nothing pushes schedules to the engine.** |

#### group — NOT synced ❌ (and field-shape mismatch)
| Field | Engine (`groups`, `config.Group`/`ScalingConfig`) | CMS schema | Notes |
|---|---|---|---|
| id/groupId, name, description, enabled | present | `groupId`, `name`, `description`, `enabled` | match |
| scaling | nested `ScalingConfig{mode,minReplicas,maxReplicas,scaleDownDelay,startupTimeout,resources}` | **flattened**: `scalingMode`, `minReplicas`, `maxReplicas`, `scaleDownDelaySeconds`, `startupTimeoutSeconds`, `resources` component | shape differs (nested vs flat; `*Delay` string "5m" vs integer seconds) |
| connections | engine: `group_connections` junction; `Group.Connections []string` | CMS: `connections` manyToMany relation | both model it, opposite directions |
| version/createdAt/updatedAt | engine-managed | — | engine-only |
| **sync** | admin API `PUT /admin/groups/{id}` etc.; **AdminClient has NO group methods at all** | — | **No client support and no caller. Groups never reach the engine from CMS.** |

#### environment — CMS-only (correct by design) ✅
`architecture.md` is explicit: env is a separate physical DB; the engine serves env `""` only (`Admin.checkEnv` rejects non-empty env). The CMS `environment` content-type (`name`, `adminApiBaseUrl`, `operatorTokenRef`, `payloadEnv`) is **CMS routing metadata** — which engine deployment a publish targets — and correctly has no engine-side table. Not a drift. Note `draftAndPublish:false`, so it also would never trigger the publish middleware.

### C. Logic / operations parity

**Engine admin API operations** (`engine/internal/httpapi/admin.go` `mount`, plus `webhook_admin.go`, `schedule_admin.go`, `admin_groups.go`):
- flows: create, publish, rollback, validate, dry-run, list, get, versions, patch (group assign), audit
- jdms: create, list, get
- connections: create, list, get
- groups: PUT, GET, list, delete, internal version
- webhooks: list, create, get, update, delete, publish, logs
- schedules: list, create, get, update, delete, run, runs

**CMS exposure of those operations:**
| Operation | Engine | CMS `AdminClient` | CMS actually calls it? |
|---|---|---|---|
| flow create/validate/publish | ✅ | ✅ | ✅ via publish sequence |
| flow dry-run | ✅ | ✅ (`dryRunFlow`) | partial — client exists; used by environment/validate controllers, not the main publish |
| flow rollback | ✅ | ✅ (`rollbackFlow`) | ✅ via `flow.rollback` controller / FlowDetail UI |
| flow list/get/versions, audit | ✅ | ✅ | ✅ (sync status, audit viewer, version history) |
| jdm create/list/get | ✅ | ✅ | ✅ (publish + sync) |
| connection create/list/get | ✅ | ✅ | ✅ (publish + sync) |
| webhook CRUD/publish/logs | ✅ | ✅ (client methods) | ❌ **never called** |
| schedule CRUD/run/runs | ✅ | ✅ (client methods) | ❌ **never called** |
| group CRUD | ✅ | ❌ **no client methods** | ❌ |

**Validation parity:**
- **Consistent (enforced both sides):**
  - connection secret denylist — CMS `SECRET_DENYLIST` (publish-transform.ts, superset incl. `pwd`,`dsn`) + engine `secretValueKeys` (admin_types.go). CMS throws before any HTTP; engine rejects at the edge.
  - group scalingMode enum (static/dynamic/ephemeral) — CMS enum + engine `chk_scaling_mode` CHECK + `ValidateGroup`.
  - group replica bounds — CMS `minReplicas min:0`/`maxReplicas min:1` + engine `chk_replicas_range` and `static ⇒ min≥1` (`chk_static_min` / `ValidateGroup`). *(Note: CMS enforces `maxReplicas ≥ 1` but does not enforce `maxReplicas ≥ minReplicas`; the engine does — but groups never reach the engine, so the engine check is unreachable from CMS.)*
  - flow structural + dangling-ref validation — engine `ValidateTree` on create; CMS blocks publish on `validateFlow` `ok:false`.
  - group id regex `^[a-z][a-z0-9-]*$` — CMS `groupId` regex + engine `groupIDRegex`.
- **One-sided / weaker:**
  - **cron expression**: schedule `schedule` is only `string maxLength:128` in CMS — no cron syntax validation; the engine validates cron on create. Since schedules aren't pushed, a bad cron can sit in CMS indefinitely.
  - **timezone**: CMS is a free string (default UTC); engine validates IANA timezone. Not cross-checked.
  - **flow method enum** narrower in CMS (no HEAD/OPTIONS) than engine `knownMethods`.
  - **webhook/schedule required fields**: engine enforces (`name`, `secretRef`, `flowId` for webhook; `id`,`name`,`schedule`,`flowId` for schedule). CMS marks some required (`name`, `provider`) but `flowId` is an optional relation and `secretRef` is optional in the webhook schema — looser than the engine, and never validated because no push occurs.

---

## Conclusions

1. **(A) Save-sync:** A saved field is **not** automatically synced. Sync happens only on explicit **Publish**, and only for **flow / jdm / connection**. CMS↔engine drift is a built-in possibility (draft edits, and any webhook/schedule/group change). Reverse drift is handled manually via `/sync/status` and `/sync/import` (flows/jdms/connections only).

2. **(B) UI + logic parity:** Not in sync for half the entities.
   - webhook, schedule, group are authorable in the CMS (content types + components exist, and for webhook/schedule the admin HTTP client exists) but **have no publish/sync wiring** — their edits never reach the engine. `lastSyncStatus` fields on webhook/schedule are decorative today.
   - group has **no AdminClient methods at all** and a flattened-vs-nested field-shape mismatch.
   - The operator UI is incomplete: `WebhooksPage` is unregistered and action-less; there is no Schedules or Groups page; `dry-run` is only partially surfaced.

## Concrete drift risks

- **Silent webhook/schedule/group drift (highest):** operators configure these in the CMS, see `lastSyncStatus` badges, and reasonably assume Publish propagates them — it does not. The engine runs whatever was seeded directly, independent of CMS state.
- **Draft-vs-active flow drift:** editing a flow/jdm/connection without publishing leaves the engine on the prior active version with no warning in the content manager (only the plugin Sync page surfaces it).
- **Circuit-breaker policy unauthorable:** engine resilience supports breaker + backoff tuning; CMS exposes only timeout + maxAttempts, so breaker config can only be set outside the CMS → config the CMS can never represent or reconcile.
- **Looser CMS validation for schedules/webhooks:** invalid cron, non-IANA timezone, or missing engine-required fields can be saved in CMS; because they're never pushed, the mismatch is latent and would surface only if a sync path is later added.
- **flow method enum** divergence (no HEAD/OPTIONS in CMS) blocks authoring those routes from the CMS even though the engine accepts them.

## Recommendations (not implemented)

1. Add publish paths for **webhook and schedule** mirroring the flow/jdm/connection middleware: extend `PUBLISHABLE` in `publish-middleware.ts`, add `runWebhookPublish`/`runSchedulePublish` in `publish-core.ts` (the `AdminClient` methods already exist), and write back `lastSyncStatus`.
2. Add **group** support end-to-end: group methods on `AdminClient` (none exist), a transform reconciling the flat CMS shape to the engine's nested `ScalingConfig`, and a publish path.
3. Extend `/sync/status` + `/sync/import` to cover webhooks/schedules/groups so reverse drift is detectable for every entity.
4. Register `WebhooksPage` in the plugin routes and give it a Publish/Sync action; add Schedules and Groups pages (or clearly document that these are content-manager-only until wired).
5. Expose breaker/backoff resilience fields in the connection resilience component, or document them as engine-only.
6. Tighten CMS validation to match the engine: cron syntax + IANA timezone for schedules, flow method enum (add HEAD/OPTIONS or document the restriction), and `maxReplicas ≥ minReplicas`.
7. Surface a draft-vs-published drift indicator in the content manager / plugin for flow/jdm/connection.

### Key file references
- Push trigger: `cms/src/index.ts`, `cms/src/publish-middleware.ts`
- Flow publish sequence: `cms/src/plugins/rule-engine/server/src/services/{publish-core,validation,publish-transform,admin-client}.ts`, `controllers/publish.ts`
- Manual reconcile: `cms/src/plugins/rule-engine/server/src/controllers/sync.ts`, `routes/index.ts`
- Engine admin API: `engine/internal/httpapi/{admin,admin_handlers,webhook_admin,schedule_admin,admin_groups}.go`
- Engine schema: `engine/migrations/{0001_init,0003_webhooks,0004_groups,0004_schedules}.up.sql`; structs in `engine/internal/config/{store,webhook,schedule,group}.go`, `engine/internal/connect/connect.go`
- CMS content types: `cms/src/api/*/content-types/*/schema.json`; components in `cms/src/components/**`
- UI: `cms/src/plugins/rule-engine/admin/src/index.tsx` (route registration), `pages/WebhooksPage.tsx` (unregistered)
