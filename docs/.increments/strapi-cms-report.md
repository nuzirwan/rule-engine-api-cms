# Strapi CMS control plane — increment report

Branch: `feat/strapi-cms` · reviewed **APPROVED** (`docs/.agents/tasks/strapi-cms/review.json`).

This increment adds an isolated **Strapi 5** application under `cms/` that authors engine config
(flows, JDM decision graphs, connections) with visual editors and publishes it to the Go rule
engine **strictly through the `/admin/*` HTTP control plane** frozen in
`docs/lld/slice-f-admin-api.md`. The CMS never touches the engine's Postgres config store directly,
and it keeps its own, separate Postgres database and schema. The Go module (`go.mod`, `go.sum`,
`internal/`, `cmd/`) is byte-identical to mainline — all changes live under `cms/` and
`docs/`.

## App structure (`cms/`)

A standard Strapi 5 TypeScript app plus one in-tree plugin:

- `cms/config/` — Strapi config. `database.ts` targets the CMS's own Postgres db + schema (see
  "Isolated Postgres" below); `plugins.ts` enables the in-tree `rule-engine` plugin; `server.ts`,
  `admin.ts`, `api.ts`, `middlewares.ts` are standard.
- `cms/src/api/{flow,jdm,connection,environment}/` — the four content types (schema + default
  controller/route/service per type).
- `cms/src/components/{flow,connection}/` — reusable components (flow fixtures; connection driver
  settings `postgres`/`rest`/`valkey`; resilience policy).
- `cms/src/plugins/rule-engine/` — the integration plugin, split into an **admin** half (the two
  visual custom-field editors + ValidationPanel) and a **server** half (the publish pipeline,
  admin-API client, pure transforms, controller, and Draft&Publish lifecycle):
  - `server/src/services/publish-transform.ts` — pure CMS→engine flat transforms + the recursive
    secret denylist.
  - `server/src/services/admin-client.ts` — typed native-fetch wrapper over `/admin/*`.
  - `server/src/services/validation.ts` — the ordered, publish-blocking sequence + connection
    reconcile.
  - `server/src/controllers/publish.ts` — Strapi controller + the §5.5 HTTP status mapping.
  - `server/src/lifecycles/flow.ts` — the `beforePublish` gate (runs the same sequence).
  - `server/src/index.ts` — server registration (both custom fields + the flow lifecycle).
  - `admin/src/{index.tsx,customFields.ts}` — admin registration of both custom fields with base
    `type:'json'` and lazy editor loaders.
  - `admin/src/components/FlowCanvasField/{index.tsx,serialize.ts}` — the reactflow canvas and the
    single shared canvas↔engine-tree serializer.
  - `admin/src/components/{JdmEditorField,ValidationPanel,RawJsonFallback}/` — the JDM editor, the
    validation display, and the raw-JSON degrade path for malformed stored JSON.
  - `server/tests/**` + `admin/src/**/*.test.ts` — vitest + nock integration/unit suite.
- `cms/package.json` — exact-pinned deps (no `^`/`~`): Strapi 5.56.0 family, `@gorules/jdm-editor`
  1.52.0, `reactflow` 11.11.4, `pg` 8.13.1, `zod` 3.24.2, `vitest` 2.1.8, `nock` 13.5.6, `typescript`
  5.5.3. The `build` script bumps the Node heap (`--max-old-space-size=4096`) — build-infra only,
  not a code change.

Build artifacts (`cms/dist`, `cms/.strapi`, `cms/node_modules`, `cms/public/uploads`) are not
committed.

## The four content types

All author engine config as structured Strapi documents; the publish pipeline turns them into FLAT
admin-API bodies. The three publishable types use Draft & Publish; `Environment` does not.

- **Flow** (`collectionType`, draft&publish) — a versioned flow mirroring the engine's
  flows/flow_versions/flow_fixtures. Fields: `flowId` (uid), `method` (enum GET/POST/PUT/PATCH/
  DELETE), `path` (string, regex-guarded), `tree` (**customField** `plugin::rule-engine.flow-canvas`
  — the reactflow editor), repeatable `fixtures` (`flow.flow-fixture`), `environment` relation,
  `note`, plus CMS bookkeeping (`engineVersion`, `lastPublishStatus` enum, `lastValidation` json).
- **Jdm** (`collectionType`, draft&publish) — a versioned GoRules ZEN decision graph mirroring
  jdms/jdm_versions. Fields: `jdmId` (uid), `doc` (**customField** `plugin::rule-engine.jdm-editor`
  — the GoRules editor), `environment` relation, `note`, `engineVersion`.
- **Connection** (`collectionType`, draft&publish) — a typed pointer at a DB/REST source mirroring
  connections/connection_versions. Fields: `key` (uid), `type` (enum postgres/valkey/rest),
  `settings` (dynamic zone over the credential-free driver components), `secretRef` (a reference —
  **never a secret value, never a DSN**), `resilience` (`connection.resilience-policy`),
  `environment` relation, `engineVersion`.
- **Environment** (`collectionType`, no draft&publish) — CMS metadata selecting which engine admin
  deployment a publish targets. Fields: `name` (uid — not the payload env), `adminApiBaseUrl`,
  `operatorTokenRef` (names the env var holding the bearer), and `payloadEnv` (the value sent in the
  admin body's `env` field, default empty string `""`).

## Visual editor integrations

Two editors are mounted as Strapi **custom fields**, both registered with base `type:'json'` on both
the admin and server halves so the serialized value persists into the JSON column with no
double-encoding:

- **reactflow flow canvas** (`FlowCanvasField`) — a drag-and-drop DAG canvas backing `Flow.tree`. It
  imports `canvasToTree`/`treeToCanvas` from the single shared `./serialize` module (the same module
  the server transform consumes), so there is exactly one definition of the serializer. The walk
  keeps node x/y layout in a sidecar map out of the engine tree, and passes `spec` through verbatim
  so mixed casing survives the round trip (`operation.{Kind,Payload,Required}` stay capitalized while
  other keys stay camelCase).
- **GoRules JDM editor** (`JdmEditorField`) — the `@gorules/jdm-editor` ZEN decision-graph editor
  backing `Jdm.doc`.
- **ValidationPanel** — surfaces the structural + fixture validation results from the admin API in
  the editing UI.
- **RawJsonFallback** — degrades to a raw-JSON editor if a stored value is malformed, so a bad
  document never bricks the edit screen. Custom-field Input loaders are lazy.

## Validation + publish pipeline

Publish is a single ordered, **publish-blocking** sequence (`runPublishSequence` in
`validation.ts`), shared identically by the controller (`publish.ts`) and the Draft & Publish
`beforePublish` lifecycle (`flow.ts`). It takes an `AdminClient`, the plain entries, and a
write-back callback, so it is fully unit-testable with nock. The sequence:

1. **listConnections → createConnection** for only the keys that meaningfully differ
   (`connectionNeedsCreate` is conservative — it compares only the keys the author set, ignores
   driver-defaulted extras and the Go-cased ns resilience, and errs toward re-create when
   inconclusive).
2. **createJdm** per referenced JDM (create == activate), writing back `engineVersion`.
3. **createFlow** (validated=false), writing back `engineVersion = N`.
4. **validateFlow** in STORED mode (`{env,flowId,version:N}`).
5. **publishFlow** (`{env,version:N}`) only when validation returns `ok:true`.

Three mandated gates each abort **before** `publishFlow`:

- a `200 {ok:false}` validation failure (`PublishBlockedError` at stage `validateFlow`);
- an unreachable validator — 5xx/transport (recoverable block); a 404 on stored-mode validate is a
  non-recoverable block ("re-create and retry");
- a detected inline secret — the transform builds all connection payloads up front, so the recursive
  denylist throw aborts **before any admin HTTP call is made at all**.

Write-backs read distinct response keys (`createFlow().version`, `createJdm().version`,
`publishFlow().activeVersion`) and record `lastPublishStatus`/`lastValidation` on the Flow document.
The controller maps blocked stages to HTTP status per §5.5 (409/404/403/422 echoed as-is, a
status-less validation failure → 422, 5xx/transport → 503).

## How it authenticates to and calls the admin API

`admin-client.ts` is a thin, typed `fetch` wrapper around `/admin/*`. Every request:

- **FLAT bodies** — `env` is a top-level sibling, with no `flow`/`connection` wrapper, matching the
  Go handlers' `DisallowUnknownFields` decode. Stored-mode validate sends exactly
  `{env,flowId,version}`; publish/rollback send exactly `{env,version}`.
- **`env=""`** — the transforms build payloads with `env=''`; the client strips that and re-stamps
  its own resolved `payloadEnv` (default `""`) so the wire body carries `env` exactly once at top
  level.
- **Bearer token on every call** — `Authorization: Bearer <token>` is set on every request. The
  token is resolved **at call time** (`resolveAdminConfig`) from the per-Environment
  `operatorTokenRef` env var or the global `ADMIN_API_OPERATOR_TOKEN`; it is never persisted on the
  instance beyond the request and never logged.
- **Base URL via env** — from the per-Environment `adminApiBaseUrl` override or the global
  `ADMIN_API_BASE_URL`; trailing slashes are trimmed. `payloadEnv` falls back to `ADMIN_API_ENV`
  then `""`.

Non-2xx responses map to a typed `AdminApiError` carrying the HTTP status and a `recoverable` flag
(5xx/transport recoverable, 4xx author-fixable). No raw secret value is ever accepted, stored, or
transmitted: connections carry `secretRef` only, and the CMS-side denylist is a belt over the
engine's edge guard.

## Isolated Postgres database + schema

`cms/config/database.ts` defaults the CMS to Postgres (`DATABASE_CLIENT=postgres`; SQLite is a local
dev opt-in only) and points it at the CMS's **own** database and schema, configured entirely through
`CMS_DB_*` env vars:

- `CMS_DB_NAME` (default `strapi_cms`), `CMS_DB_SCHEMA` (default `strapi_cms`), plus
  `CMS_DB_HOST`/`CMS_DB_PORT`/`CMS_DB_USER`/`CMS_DB_PASSWORD`, SSL (`CMS_DB_SSL*`), and pool
  (`CMS_DB_POOL_*`) knobs.
- The config carries an explicit comment forbidding the CMS from ever using Postgres `public` or the
  engine's `rule_engine` schema. The CMS database is physically separate from the engine's config
  store — the CMS reaches engine config **only** via the admin HTTP API, never by reading or writing
  the engine DB.

This satisfies the directive to use Postgres with a different database and schema from the rule
engine.

## Verification

Recorded in `docs/.agents/tasks/strapi-cms/verification.md` and re-run at finalize: `npm ci` exit 0,
`npm run build` exit 0 (admin bundle with both custom fields), `npm run test` exit 0 (vitest + nock
suite green covering the flat/`env=""`/Bearer contract, the ordered sequence, all three blocking
gates, the serializer casing round-trip, and the custom-field registration smoke), and
`CGO_ENABLED=1 go build ./...` exit 0 with `git status --porcelain -- go.mod go.sum internal cmd`
empty (Go untouched).

## Limitations

- **Deferred audit viewer UI** — the admin client exposes `audit(type,id)` (`GET /admin/audit/...`),
  but there is no in-app UI to browse the engine audit trail yet.
- **`@xyflow/react` v12 migration deferred** — the flow canvas is on `reactflow` 11.11.4; the move to
  `@xyflow/react` v12 is not done.
- **Cosmetic (non-blocking):** the controller's catch calls `persistWriteBack(..., (err).writeBack)`,
  but `runPublishSequence` throws errors that carry no `.writeBack`, so that call is always a no-op.
  The explicit `update({lastPublishStatus:'failed'})` in the `PublishBlockedError` branch still
  records the failure, so no gate or bookkeeping is affected — only the in-sequence `lastValidation`
  diff on an `ok:false` block is not persisted through that path.
