# Design — Strapi CMS control-plane module

Status: **revised after design-review (round 3)** · Last updated: 2026-10-03

This design covers the **Strapi CMS control-plane module** for `nzr-rules-engine` — the authoring
UI that is the original ask of the whole project (HLD §2, ADR-004/005/006; `docs/STATE.md`
"Strapi control-plane module (the CMS)"). The Go v1 engine is complete and proven live; this
module is a **separate Node/React/Strapi build** that authors config and publishes it to the
engine's admin HTTP API. It never touches the engine's Postgres config store directly, and it is
**not** part of the Go module (no Node deps in `go.mod`, no Node files under the Go `internal/` or
`cmd/` trees).

All npm version numbers below were verified against the npm registry on 2026-10-03 (`npm view`),
including peer-dependency ranges and the exact `reactflow` version `@gorules/jdm-editor@1.52.0`
bundles. The admin HTTP wire contract below was reconciled against the **real** admin-API spec
`.worktrees/admin-api/docs/lld/slice-f-admin-api.md` (branch `feat/admin-api`), not the earlier
sketch in `slice-d-configstore.md` §5, and the connection settings schema was settled against the
**actual Slice B postgres driver** `internal/connect/drivers/postgres.go`.

> **Revision note (round 3).** This revision resolves the 5 round-2 findings (0 HIGH, 3 MEDIUM, 2
> NIT) in `docs/.agents/tasks/strapi-cms/design-review.md`: the **validate request is pinned to
> `slice-f` stored mode** — `{ env, flowId, version:N }` as top-level siblings, no `flow` wrapper
> (finding 1, new §5.1.1); the Connection **settings shape is explicitly stated to follow the real
> driver** (`pool.{maxConns,minConns}`, no flat `poolMax`, no `schema` key — schema qualification
> lives in the SQL) (finding 2); the secret denylist is relabelled a **strict superset** of the
> engine edge guard (`pwd` is CMS-only), dropping the "mirror" wording (finding 3); §6.3 pins the
> **version write-back source keys** (`createFlow().version` vs `publishFlow().activeVersion`)
> (finding 4); and the `listConnections` reconcile step specifies **authored-key settings comparison
> with defaults normalized, erring toward a harmless re-create** (finding 5). It also corrects a
> round-2 inaccuracy caught while verifying source: `GET /admin/audit/{type}/{id}` **is** mounted in
> `slice-f` (§2.1/§2.8), so the CMS ships a typed `audit()` client method and defers only the audit
> **viewer** as scope (§10.1 R3-C). All resolutions were checked against the real
> `.worktrees/admin-api/docs/lld/slice-f-admin-api.md` and `internal/connect/drivers/postgres.go`;
> stale `slice-f §3.x` citations were corrected to the spec's real `§2.x`/`§3`/`§9` numbering.
> Round-2 changes (13 round-1 findings — wrapped envelopes, `env=""`, driver-faithful credential-free
> Connection, named operator bearer, create→validate→publish ordering, JDM create==activate, JDM
> `version:0`, pinned resilience shapes, nullable fixtures, mixed spec casing, wired
> `listConnections`, `type:'json'` custom fields) remain in force; full per-finding responses for
> both rounds are in §10.

---

## 1. Overview

The CMS is a Strapi 5 application living entirely under
`/home/nuzirwan/project/rule-engine-api/.worktrees/strapi-cms/cms`. It gives non-engineers a
visual authoring surface for the three config objects the engine consumes — **Flow** (a versioned
flow-tree JSON), **JDM/Rule** (a GoRules ZEN decision graph), and **Connection** (a typed pointer
at a DB/REST source holding a `secret_ref`, never a secret value). Authoring uses two embedded
React editors mounted as Strapi **custom fields**: `@gorules/jdm-editor` for ZEN decisions and a
React Flow canvas for the flow tree.

Strapi owns its own private database for drafts and editorial state (ADR-005). The engine's config
store is a *separate*, engine-owned Postgres (ADR-005/006) that the CMS reaches **only** through
the engine's admin HTTP API (`/admin/*`). On publish, a **publish-transform** service converts the
CMS's draft content into the engine config shapes from `slice-d-configstore` / `lld-contracts` /
the real `slice-f` wire contract, creates the flow version, validates it through
`POST /admin/flows/validate` (publish-blocking), and then publishes it through
`POST /admin/flows/{id}/publish`. The admin API target base URL and operator credential are
configurable via env.

Why this shape rather than a bespoke admin UI: Strapi gives us the React admin, RBAC,
draft/publish state, and a plugin system to host the two visual editors for free (ADR-004), and
keeping all engine integration behind the documented HTTP contract means the CMS stays decoupled
from the engine's private schema (ADR-005, R11 contract-drift mitigation).

### Technology stack (locked once this design is approved)

| Concern | Choice | Version (exact pin) | Notes |
| --- | --- | --- | --- |
| CMS framework | Strapi | **5.56.0** | Latest stable (`@strapi/strapi` dist-tag `latest`, verified `npm view` 2026-10-03). Strapi 5, not 4. |
| Runtime | Node.js | **22.x LTS** (`22.11.0`) | Strapi 5 `engines`: `node >=20.0.0 <=26.x.x` (verified). Pin via `.nvmrc` + `package.json engines`. 22 is the active LTS inside that range. |
| Package manager | npm | **10.x** (ships with Node 22) | Lockfile committed; exact pins (see §7). |
| Admin UI React | React / React-DOM | **18.3.1** | Strapi 5 peer dep is `react ^18.0.0`; jdm-editor peer dep is `react >= 18`; design-system peer is `react ^18.0.0` (all verified). React **19 exists (19.3.0)** but is **deliberately NOT used** — Strapi 5.56 peers on 18. |
| Flow canvas | reactflow | **11.11.4** | **v11 `reactflow` package**, matching the exact version `@gorules/jdm-editor@1.52.0` bundles (verified: its `dependencies.reactflow === "11.11.4"`), so the app ships one React Flow runtime. NOT `@xyflow/react` v12 (12.12.0 exists; see §4.2). |
| ZEN editor | @gorules/jdm-editor | **1.52.0** | Latest stable (verified). Bundles `reactflow@11.11.4`, `antd@5.21.2`, `@gorules/zen-engine-wasm@0.23.1` (WASM ZEN engine for in-editor simulation), `zod@^3.24.2`. Peer `react >= 18`. |
| Plugin scaffolding | @strapi/sdk-plugin | **6.1.1** | For building the custom-field + admin plugin (verified). |
| Admin design system | @strapi/design-system | **2.2.4** | Strapi 5 admin component kit (verified). Peers: `react ^18`, `styled-components ^6`, `@strapi/icons ^2`. |
| Admin icons | @strapi/icons | **2.2.4** | design-system peer dep (`@strapi/icons ^2`); pin to match the design-system release. |
| DB (Strapi internal) | Postgres (`pg` driver) | `pg` **8.13.1** | Strapi's own private DB (ADR-005). Separate from the engine config store. SQLite allowed for local dev only. |
| HTTP client (publish) | native `fetch` (Node 22 global) | — | No extra dep; Node 22 ships a stable global `fetch`/`undici`. Avoids an open-range HTTP lib. |
| Validation (transform) | zod | **3.24.2** | Shape-check CMS content before building admin payloads (jdm-editor already carries `zod@^3.24.2`; we pin our own direct dep to the same line). |
| Test runner | vitest | **2.1.8** | Fast, ESM-native, good HTTP mocking story; pin exact. (vitest 5.x exists but 2.x is the conservative pin matched to the toolchain; see §7.) |
| HTTP mock (tests) | nock | **13.5.6** | Intercept the admin API in publish-transform tests (§6). (nock 14.x exists; 13.x is pinned for the CJS/ESM interop the test harness uses; see §7.) |

All versions are **exact** (no `^`/`~`) per §7.

---

## 2. `cms/` app layout (isolated under the worktree)

The Strapi app is created with the official generator and lives wholly inside the worktree. Nothing
here is referenced by the Go build.

```
.worktrees/strapi-cms/cms/
  package.json                 # exact-pinned deps (§7); "engines": { "node": "22.x" }
  package-lock.json            # committed
  .nvmrc                       # 22.11.0
  .npmrc                       # save-exact=true
  .env.example                 # ADMIN_API_BASE_URL, ADMIN_API_OPERATOR_TOKEN, ADMIN_API_ENV, DB_* (see §5)
  tsconfig.json
  config/
    database.ts                # Strapi's OWN private DB (pg); never the engine config store
    server.ts
    admin.ts
    plugins.ts                 # registers the rule-engine plugin + custom fields
  src/
    api/
      flow/                    # content type: Flow         (§3)
      jdm/                     # content type: Jdm (Rule)    (§3)
      connection/              # content type: Connection    (§3)
      environment/             # content type: Environment   (§3)
    plugins/
      rule-engine/             # the custom plugin
        admin/src/
          components/
            FlowCanvasField/   # React Flow custom field  (§4)
            JdmEditorField/    # @gorules/jdm-editor field (§4)
            ValidationPanel/   # surfaces /admin/flows/validate results (§5)
          index.ts             # registerCustomFields({ type: 'json', ... }) for both fields (§4)
        server/src/
          index.ts
          content-types/       # (optional server-side CT additions)
          services/
            publish-transform.ts   # CMS content -> engine config shapes (§5) — unit tested
            admin-client.ts        # typed client for /admin/* (base URL + bearer token) (§5)
            validation.ts          # pre-publish validate-hook orchestration (§5)
          controllers/
            publish.ts             # POST /rule-engine/flows/:id/publish (admin-only route)
          lifecycles/              # beforePublish hook wiring (§5)
          routes/
        tests/                     # vitest unit/integration (§6)
  types/                           # shared TS types for the engine config shapes + wire envelopes
```

Build isolation rules (from `docs/STATE.md` merge-contention rule and the task contract):
- This worktree edits only `cms/**` and this design doc. It never edits `go.mod`, `go.sum`, the Go
  `internal/`/`cmd/` trees, or the engine migrations.
- The admin API is built concurrently in `.worktrees/admin-api` (branch `feat/admin-api`) and may
  be unmerged when this runs. We therefore design against the **documented** HTTP contract (§5,
  reconciled against `slice-f`) and make the target fully configurable, so the CMS can be built and
  unit-tested with the admin API mocked, independent of that worktree's merge state.

---

## 3. Content types (mirroring the engine config model)

The content types mirror the config model in `slice-d-configstore` §1–2 and the `Store`/`FlowVersion`/
`ConnectionDef` seams in `lld-contracts`. The guiding rule: **the CMS authors drafts; the engine's
version/pointer/audit machinery is the source of truth for activation.** So the CMS stores the
*authored body* plus enough bookkeeping to drive a publish, and lets the engine own immutable
version numbers, the active pointer, and the audit log. The CMS records the engine-assigned version
back onto the entry after a successful publish (read-only mirror), but does not try to be a second
version authority — that would duplicate the engine's `*_versions`/`active_pointers` model and
invite drift (R11).

Strapi 5 **Draft & Publish** is enabled on Flow, Jdm, and Connection: Strapi's own draft/published
state is the *editorial* gate ("is this entry ready to attempt an engine publish"), distinct from
the *engine* activation that the publish-transform performs. The two are related but not the same —
see §5.

### 3.1 Flow

Mirrors `flows` + `flow_versions` + `flow_fixtures` (slice-d §1).

| CMS field | Type | Maps to engine model | Notes |
| --- | --- | --- | --- |
| `flowId` | uid (string) | `flows.id` (stable identity) | e.g. `fmc-order-by-id`. Immutable after create. |
| `method` | enumeration GET/POST/PUT/PATCH/DELETE | `flows.method` | with `path`, UNIQUE route. |
| `path` | string | `flows.path` | Go 1.22 ServeMux pattern, e.g. `/order/{order_id}`. Validated against a pattern regex. |
| `tree` | **custom field `rule-engine.flow-canvas`** (base `type: 'json'`) | `flow_versions.tree` (Slice A Node tree) | authored on the React Flow canvas (§4); persisted as the engine's `Node` JSON. |
| `fixtures` | component (repeatable) `flow-fixture` | `flow_fixtures.body` | `{ name, input, mocks?, expect? }` per slice-d §5; `mocks`/`expect` optional (§3.1.1). Travel with the version. |
| `environment` | relation → Environment | selects which engine admin plane the publish targets | drives `adminApiBaseUrl` selection (§5), NOT the payload `env`. |
| `note` | text | `flow_versions.note` | author's "why". |
| `engineVersion` | integer (read-only) | mirror of the engine-assigned `flow_versions.version` | written back after `createFlow`; display only. |
| `lastPublishStatus` | enumeration (none/validated/published/failed) | — (CMS bookkeeping) | surfaced in list view. |
| `lastValidation` | json (read-only) | — | last `/admin/flows/validate` result for the UI (§5). |

#### 3.1.1 `flow-fixture` component

Fields: `name` (string, **required**), `input` (json, required), `mocks` (json, **nullable/optional**),
`expect` (json, **nullable/optional**) — shaped as `slice-d` §5's FlowFixture. `slice-d` §5 states
`mocks`/`expect` are optional and "a fixture asserts whichever it declares," so the component makes
them nullable rather than required (finding 9). The transform maps present-only fields through and
omits absent ones, so a seed-style `{name, input}` fixture round-trips without inventing empty
`mocks`/`expect` objects.

Consequence for testing: a fixture with no `expect` asserts nothing, so it proves little about the
publish gate. The §6 "validation blocks publish" test therefore uses a fixture that **declares an
`expect`** whose assertion the engine fails, so a real failing fixture is exercised (finding 9) —
it does not rely on the seed's assertion-free fixtures.

### 3.2 Jdm (Rule)

Mirrors `jdms` + `jdm_versions` (slice-d §1).

> **Engine semantics (finding 6):** `POST /admin/jdms` **creates and activates** the JDM version in
> **one transaction** (`slice-f` §2.8: "`PutJDMVersion` both inserts and activates — JDMs have no
> separate validate gate"). There is **no** separate JDM validate/publish gate. So a JDM is
> **live engine-side the moment `createJdm` returns**, and `engineVersion` is written back from the
> *create* response — not "after publish" (there is no distinct JDM publish step).

| CMS field | Type | Maps to engine model | Notes |
| --- | --- | --- | --- |
| `jdmId` | uid (string) | `jdms.id` | stable id referenced by decision nodes (e.g. `fmc-payment`). |
| `doc` | **custom field `rule-engine.jdm-editor`** (base `type: 'json'`) | `jdm_versions.jdm` (GoRules JDM graph) | authored in `@gorules/jdm-editor` (§4); the `{nodes, edges}` JDM graph exactly as in `seed.json`. |
| `environment` | relation → Environment | publish-target admin plane (base URL) | |
| `note` | text | `jdm_versions.note` | |
| `engineVersion` | integer (read-only) | mirror of `jdm_versions.version` | written back **from the `createJdm` response** (create == activate). |

### 3.3 Connection

Mirrors `connections` + `connection_versions` (slice-d §1), with the **hard rule that no secret
value is ever stored** — only a `secret_ref` (slice-d §2 "connections versioned, secrets not";
`[[config-and-secrets]]`; AC-20).

> **Settings schema reshaped to the real driver (finding 3).** The engine's Slice B postgres driver
> (`internal/connect/drivers/postgres.go`, `buildDSN`) accepts EITHER a ready-made `dsn` string OR
> **discrete `host`/`port`/`database`/`user`/`sslmode`** settings, and injects the **password from
> the resolved `secretRef`** onto the parsed pool config so the credential never lives in the DSN
> string. The real admin API (`slice-f` §2.8) **rejects any `settings` key named `dsn`**
> (case-insensitive) with a 400. Therefore the CMS Connection content type authors the
> **discrete-field, credential-free** shape — `host/port/database/user/sslmode/pool` plus a
> `secretRef` pointing at the password — which is exactly what the driver composes and what the
> admin API accepts. The CMS never produces a `dsn` field.

| CMS field | Type | Maps to engine model | Notes |
| --- | --- | --- | --- |
| `key` | uid (string) | `connections.key` | stable connection key (e.g. `fmc-pg`). |
| `type` | enumeration postgres/valkey/rest | `connections.type` | drives which `settings` sub-shape is valid. |
| `settings` | component (polymorphic by `type`) → `connection_versions.settings` | per-type discrete fields, **never credentials, never a `dsn`** (see table below). |
| `secretRef` | string | `connection_versions.secret_ref` | opaque reference (e.g. `env:FMC_PG_DSN`, `vault://…`). **The only secret-adjacent field, and it holds a reference, not a value.** For postgres it points at the password (driver injects it). |
| `resilience` | component `resilience-policy` | `connection_versions.resilience` | pinned shape, see §3.3.2. |
| `environment` | relation → Environment | publish-target admin plane (base URL) | |
| `engineVersion` | integer (read-only) | mirror | written back from the `createConnection` response `{key, version}`. |

#### 3.3.1 Per-type `settings` sub-shape (credential-free)

| `type` | settings fields | Notes |
| --- | --- | --- |
| `postgres` | `host` (string), `port` (int, default 5432), `database` (string), `user` (string), `sslmode` (enum disable/require/verify-full, default disable), `pool` `{ maxConns, minConns }` | matches `buildDSN` discrete path; password comes from `secretRef`. **No `dsn`.** |
| `rest` | `baseURL` (string), `headers` (json, non-secret headers only) | matches the seed's `ship-rest` shape. Auth headers that carry a token use `secretRef`, never an inline value. |
| `valkey` | `addr` (string), `db` (int, default 0), `pool` `{ maxConns, minConns }` | password via `secretRef`. |

> **Settings follows the DRIVER shape, which is the real consumer (finding 2).** The CMS authors
> exactly the keys the engine's postgres driver reads — `host/port/database/user/sslmode` and a
> nested `pool.{maxConns,minConns}` — verified against `internal/connect/drivers/postgres.go`
> (`buildDSN` reads `host/port/database/user/sslmode`; `applyPoolSettings` reads
> `pool.maxConns`/`pool.minConns`). It deliberately does **not** author two keys that appear in some
> admin-API prose/examples: a flat **`poolMax`** (the driver only consumes nested
> `pool.{maxConns,minConns}`, so a flat `poolMax` would be an inert key) and a **`schema`** key
> (schema qualification lives in the SQL — the seed queries `fmc_order.order_status` by qualifying
> the table name in the query, not via a connection setting — so there is no `schema` settings home,
> and inventing one would be a key the driver never reads). If `poolMax`/`schema` ever need
> first-class support, that is a **driver** change to reconcile first; until then the CMS emits only
> driver-consumed keys. `DisallowUnknownFields` does not reach inside `settings` (it is jsonb/map),
> so an inert key would not 400 — this is a fidelity choice, not a decoder requirement.

A **content-type lifecycle guard** (`beforeCreate`/`beforeUpdate`) and the transform (§5.2) both
reject a Connection whose `settings` JSON contains any key matching the secret denylist. The denylist
is **`password`, `pwd`, `secret`, `token`, `apikey`, `dsn`** (case-insensitive). Two notes on how
this relates to the engine's own edge guard (finding 3):
- The engine edge guard (`slice-f` §2.8) rejects `settings` keys matching **`password`, `secret`,
  `token`, `apiKey`** (plus a blanket `dsn` reject). The CMS denylist is a **strict superset** of
  that list: it adds **`pwd`** (a common password alias the engine guard does not list) and keeps the
  blanket **`dsn`** reject. It is a superset for defense-in-depth, **not** a byte-for-byte mirror — a
  `pwd` key would be rejected CMS-side but accepted by the engine edge, which is the safe direction.
- `dsn` is a **blanket reject** (not "dsn-with-embedded-credentials"), matching the engine's own
  blanket `dsn` reject and the fact that the CMS authors the discrete shape instead. This removes the
  earlier internal contradiction (a `dsn` the CMS both produces and forbids). The engine schema has
  no column for a secret value anyway.

> **The seed's connections are a dev-only artifact the CMS does not reproduce (finding 3).**
> `internal/config/testdata/seed.json` connections embed a credential-bearing `dsn`
> (`fmc-pg` → `postgres://root:root@…`). That shape is a local dev/test seed; it is **rejected by
> both the engine's `dsn` guard and the CMS denylist**, by design. The CMS therefore does **not**
> claim to round-trip the literal seed connections. The connection round-trip test (§6.1) uses
> **credential-free** fixtures in the discrete `host/port/database` + `secretRef` shape — the shape
> the engine actually consumes in production. The flow/JDM round-trip tests still use the literal
> seed trees/graphs (those contain no secrets).

#### 3.3.2 Pinned `resilience` request shape (finding 8)

Three resilience encodings appear across the codebase: the seed uses flat `{timeoutMs, maxAttempts}`;
`slice-f` §2.8 **request** uses `{timeoutMs, retry: {maxAttempts}}`; the `slice-f` §2.8 **GET
response** returns `{timeout: <nanoseconds>, retry: {...}}` (the Go `ResiliencePolicy` marshalled
with `time.Duration` as ns). The CMS **sends the `slice-f` request shape** and **reads the GET
response shape**:

```jsonc
// resilience as the CMS SENDS it on create (slice-f §3.7 request):
{ "timeoutMs": 2000, "retry": { "maxAttempts": 2 } }
// resilience as the CMS READS it back from GET /admin/connections (ns + nested retry):
{ "timeout": 2000000000, "retry": { "maxAttempts": 2 } }
```

The `resilience-policy` component authors `timeoutMs` (ms) + `retry.maxAttempts`; the transform emits
the request shape verbatim. When reconciling against a GET (presence check, §5.4 step 1), the client
converts the response `timeout` ns → ms before comparing. The transform is **unit-tested to emit
exactly `{timeoutMs, retry:{maxAttempts}}`** (§6.1), closing the "three observed encodings"
ambiguity.

### 3.4 Environment

Mirrors the per-env isolation model (ADR-006). Not a 1:1 engine table — it is CMS metadata that
tells the publish-transform *which engine admin API* to target.

> **The Environment `name` is NOT the payload `env` (finding 2).** The real engine serves **exactly
> one env** and uses the empty env `""`; `slice-f` §2 (intro) + §9 state the request's `env` field
> "must be absent or equal to the served env; a mismatch is a `400`." Per-env isolation
> (dev/staging/prod) is achieved by targeting a **different engine deployment per env via its base
> URL** (ADR-006), not by a payload discriminator. So the Environment `name` selects **which base
> URL / operator token** the client uses; the payload `env` sent to that engine is `""` by default.

| CMS field | Type | Notes |
| --- | --- | --- |
| `name` | uid (dev/staging/prod) | selects the target engine deployment (base URL + operator token). **Not** sent as the payload `env`. |
| `adminApiBaseUrl` | string | base URL of that env's engine admin plane; overrides the global default. This is the real cross-env selector. |
| `operatorTokenRef` | string | reference (env var name / secret-backend pointer) to that env's operator plaintext bearer token; resolved at call time, never stored as a value. |
| `payloadEnv` | string (default `""`) | the value sent in the admin payload's `env` field. Defaults to `""` (the single served env) and **must equal the target engine's served env**, else the engine 400s. Configurable only for a future multi-env-aware engine. |

Promotion (dev→staging→prod) in v1 is modeled as: select a validated Flow/Jdm/Connection entry,
re-run the publish sequence against the target Environment (different base URL + token). The engine's
`PromoteVersion` carries the exact version + fixtures server-side; the CMS drives promotion by
targeting the destination env's admin API. (A richer one-click promotion UI is a follow-up; see §8.)

---

## 4. Editor UI integration (custom fields)

Both editors mount as Strapi 5 **custom fields** registered by the `rule-engine` plugin's admin
entrypoint. **Both register with base `type: 'json'`** via
`app.customFields.register({ name, pluginId, type: 'json', components, ... })` (finding 13) — the
underlying base type is what makes "Strapi persists the serialized value into the content type's
JSON column" true; a `string` base type would double-encode. Each custom field is a React component
that receives Strapi's `{ name, value, onChange, attribute }` props, renders its editor, and calls
`onChange({ target: { name, value, type: 'json' } })` with the serialized JSON. Wrapping with
`@strapi/design-system` `Field` primitives keeps the native label/error/hint chrome.

### 4.1 JDM editor — `@gorules/jdm-editor` 1.52.0

`@gorules/jdm-editor` exposes a `<JdmConfigProvider>` + `<DecisionGraph>` React component pair for
authoring a ZEN decision graph (`{nodes, edges}` with `decisionTableNode`, `inputNode`,
`outputNode`, etc. — exactly the shape in `seed.json`'s `jdms[].doc`). The `JdmEditorField`:
- Registers with base `type: 'json'` (finding 13).
- Renders `<DecisionGraph value={graph} onChange={setGraph} />` inside the design-system `Field`.
- Debounces `onChange` (bundled `use-debounce`) and writes the serialized graph JSON back through
  Strapi's `onChange` as a JSON value.
- Uses the editor's bundled WASM ZEN engine (`@gorules/zen-engine-wasm@0.23.1`, a transitive dep)
  only for **in-editor simulation/preview**. It is NOT the authority — the engine's
  `/admin/flows/validate` + `/admin/flows/dry-run` remain the publish-blocking gate (§5). We never
  ship the WASM result as a substitute for engine validation.

Verified (`npm view @gorules/jdm-editor@1.52.0`): peer deps `react >= 18`, `react-dom >= 18`; it
bundles `reactflow@11.11.4`, `antd@5.21.2`, `@monaco-editor/react`, `@gorules/zen-engine-wasm@0.23.1`,
`zod@^3.24.2`. The antd theming is scoped so it does not fight Strapi's design system (the editor
renders inside its own provider).

### 4.2 Flow canvas — React Flow (`reactflow` 11.11.4)

The flow tree is authored on a drag-and-drop canvas built on **`reactflow` v11 (11.11.4)** — the
same major+exact version jdm-editor bundles (verified via its `dependencies.reactflow`), chosen
deliberately so the app ships **one** React Flow runtime rather than mixing v11 and the newer
`@xyflow/react` v12 (12.12.0 exists; two copies risk context/CSS collisions and bloat). This is a
conscious trade: v12 (`@xyflow/react`) is the current line, but matching jdm-editor's pinned v11 is
the lower-risk choice while jdm-editor itself is on v11. If jdm-editor later moves to
`@xyflow/react`, the canvas migrates with it (tracked as a follow-up, §8).

The `FlowCanvasField`:
- Registers with base `type: 'json'` (finding 13).
- Renders `<ReactFlow>` with a custom node palette matching the engine's Node taxonomy (HLD §3):
  `trigger`, `action`, `condition`, `switch`, `sequence`, `parallel`, `forEach`, `decision`, `set`,
  `logger`, `response`. Each canvas node type has a side-panel form for its `spec` (e.g. an `action`
  node's `connection` + `operation`, a `decision` node's `jdmId` + `input` + `saveAs`).
- Maintains an internal `{nodes, edges}` canvas graph for layout, and on change **serializes to the
  engine's recursive `Node` tree** — the canvas is a DAG-with-layout; the serializer walks it from
  the `trigger` root and emits the nested `{ id, type, spec, children }` shape (the exact shape in
  `seed.json`). Control nodes own their children; branch edges on `condition`/`switch` map to the
  `trueKey`/`falseKey`/branch keys in the spec. Node x/y positions are kept in a sidecar layout map
  so the engine tree stays pure (the engine ignores layout).

> **Spec key casing is mixed and must be preserved exactly (finding 10).** In `seed.json`, an
> `action` node's `operation` sub-object uses **capitalized Go-struct keys** `Kind` / `Payload` /
> `Required` (the Go `connect.Operation` struct is serialized with **no** JSON tags), while
> control/leaf spec keys are **camelCase**: `condition` uses `trueKey`/`falseKey`, `set` uses
> `targetPath`/`from`, `decision` uses `jdmId`/`input`/`saveAs`, `action` uses `connection`/`saveAs`.
> The serializer MUST emit `operation.{Kind,Payload,Required}` capitalized and everything else
> camelCase. This exact casing is pinned by the serializer round-trip test (§6.2) so a naive
> all-camelCase serializer (an easy bug) is caught.

- The serializer is the UI-side half of the publish-transform contract and is unit-tested against
  the `seed.json` trees (round-trip: canvas → engine tree → canvas), asserting the mixed casing above.

Both custom fields degrade gracefully: if a stored JSON value fails to parse, the field shows a
raw-JSON fallback editor and an error, never a blank/broken canvas (so a hand-edited or
engine-returned value can always be inspected).

---

## 5. Validation hooks + publish/promotion pipeline

This is the core integration. It lives in three server-side services in the plugin:
`admin-client.ts`, `validation.ts`, `publish-transform.ts`, orchestrated by a `publish` controller
and a `beforePublish` lifecycle.

### 5.1 Admin API client (`admin-client.ts`)

A thin typed wrapper over native `fetch`, configured from env/Environment:
- **Base URL**: `ADMIN_API_BASE_URL` (global default), overridden per-entry by
  `Environment.adminApiBaseUrl`. This is the **only** cross-env selector (finding 2).
- **Operator credential (finding 4)**: the client sends the **plaintext** operator token in the
  **`Authorization: Bearer <token>`** header on every admin call. The token is read at call time
  from `ADMIN_API_OPERATOR_TOKEN` (global default) or the per-Environment `operatorTokenRef`. It is
  **never persisted** in Strapi's DB and **never logged**.
  - Naming is explicit to prevent conflation with the engine side: the CMS variable
    `ADMIN_API_OPERATOR_TOKEN` holds the **plaintext bearer** the CMS sends. The **engine** stores
    only `sha256(token)` hashes in its own `ADMIN_OPERATOR_TOKENS` (comma-separated `id:sha256hex`,
    `slice-f` §2.2–2.3) — the CMS does **not** configure that engine var and must never be given a
    plaintext token to put there. A `401` from the admin plane is the deny-by-default path
    (`slice-f` §2.5).
- **Payload `env` (finding 2)**: every admin body carries `env` = the Environment's `payloadEnv`
  (default `""`). The client never sends `dev`/`staging`/`prod` as `env`.
- **Strict shapes (finding 1)**: `slice-f` §2 decodes every body with `json.Decoder` +
  `DisallowUnknownFields`, so the client sends **exactly** the documented wrapped envelopes and no
  extra fields; a mis-shaped or bare body is a hard `400`.

Methods map to the real `slice-f` contract (wrapped envelopes shown in §5.2):

| Method | Endpoint | Request envelope | Response | Purpose |
| --- | --- | --- | --- | --- |
| `createFlow(env, flow)` | `POST /admin/flows` | `{ env, flow }` | `201 { flowId, version, validated:false }` | create a flow version (`PutFlowVersion`); does NOT publish. |
| `validateFlow(env, flowId, version)` | `POST /admin/flows/validate` | **stored mode:** `{ env, flowId, version }` (top-level siblings, **no** inline `flow`) | `200 { ok, structural, fixtures }` | structure+fixtures; marks the persisted `{flowId, version}` validated on all-pass. See §5.1.1 for why the CMS always uses stored mode. |
| `publishFlow(env, flowId, version)` | `POST /admin/flows/{id}/publish` | `{ env, version }` | `200 { flowId, activeVersion, action:"publish" }` | advance active pointer (`SetActive`); **409 if the version is not validated**. |
| `rollbackFlow(env, flowId, version)` | `POST /admin/flows/{id}/rollback` | `{ env, version }` | `200 { flowId, activeVersion, action:"rollback" }` | move pointer back. |
| `createJdm(env, jdmId, doc)` | `POST /admin/jdms` | `{ env, jdmId, version:0, doc }` | `201 { jdmId, version }` | create **and activate** a JDM version (one transaction). |
| `createConnection(env, conn)` | `POST /admin/connections` | `{ env, connection }` | `201 { key, version }` | register+activate a connection def (secret_ref only). |
| `listConnections(env)` | `GET /admin/connections` | — | `200 { connections: [ { key, type, settings, secretRef, resilience } ] }` | list active connection defs (resilience in ns/`retry` shape). |
| `dryRunFlow(env, req)` | `POST /admin/flows/dry-run` | `{ env, flowId, version?, input, mocks? }` | `200 { trace, response, errors }` | trace, writes suppressed (optional preview, not a gate). |
| `audit(env, type, id)` | `GET /admin/audit/{type}/{id}` | — | `200 { objectType, objectId, entries:[…] }` | read audit trail (`type` ∈ flow/jdm/connection). Client method exists; no v1 publish-path caller (§8, finding 12). |

> **In-CMS audit display is deferred as a product choice (finding 12).** `slice-f` §2.1 **does**
> mount `GET /admin/audit/{type}/{id}` (handler `auditTrail`, documented in §2.8: `{type}` ∈
> `{flow, jdm, connection}`, newest-first entries). So the route exists in the contract — the CMS is
> **not** blocked by a missing endpoint. The CMS simply does **not build an in-CMS audit viewer in
> v1**: audit is a read-only diagnostic the operator can hit directly, and a polished in-product
> timeline is a follow-up (§8). The `admin-client` exposes a typed `audit(type, id)` method against
> `GET /admin/audit/{type}/{id}` for when that UI is built, but no v1 publish-path code calls it, so
> the publish sequence does not depend on it. (This corrects the round-2 claim that the route was not
> mounted — it is; the deferral is editorial scope, not a contract gap.)

Request/response shapes for validate follow `slice-d` §5 / `slice-f` §2.6: `validate` returns `{ok,
structural, fixtures}` with `ok:true` only when `structural` is empty and every fixture passed; a
failing validate is a **200 with `ok:false`**, not a 5xx — the client treats `ok:false` as "blocked",
and a non-2xx HTTP status as a transport/engine error (§5.5).

#### 5.1.1 Validate request shape — pinned to stored mode (finding 1)

`slice-f` §2.6 defines **two** validate modes selected by the body fields, decoded with
`DisallowUnknownFields`, so the exact field placement matters (a wrong guess is a hard `400`):

- **Candidate mode** — body `{ env, flow: { flowId, method, path, tree, fixtures } }`. Validates the
  inline candidate **statelessly**; it **never** calls `MarkValidated`. Useful for a pre-save "is
  this draft structurally sound" check, but it does **not** mark a persisted version.
- **Stored mode** — body `{ env, flowId, version }` with `flowId` present and `version` a **positive
  int**, as **top-level siblings** (no `flow` wrapper). The engine loads that persisted version's
  tree, validates it, and on all-pass calls `MarkValidated(flowId, version)`. An inline `flow`
  object, if also present, is **ignored** in stored mode (`version` is the explicit discriminator).

The publish gate requires the engine to actually mark the created version validated (an inline
candidate-mode validate marks nothing, so publish would 409). **Decision: the publish sequence
always uses stored mode** — it sends exactly `{ env, flowId, version: N }` (top-level), the three
fields and nothing else, after `createFlow` has persisted version `N`. This is the one concrete,
decoder-safe location: `version` is a top-level sibling of `flowId`, not a field inside a `flow`
object, and no `flow` object is sent at all. The §6.3 happy-path nock interceptor asserts this body
**verbatim** (`{ env: "", flowId, version: N }`, no extra keys).

The pre-save candidate-mode check (`{ env, flow }`) is available to the UI as a non-marking
convenience, but it is **not** part of the publish-blocking gate; only the stored-mode call (which
flips the engine's `validated` flag) gates publish.

### 5.2 Publish-transform (`publish-transform.ts`) — wrapped envelopes (finding 1)

Pure functions (no I/O) that convert a CMS entry into the engine payload. Being pure makes them
directly unit-testable (§6). Every function emits the **wrapped envelope carrying `env`** that the
real admin API requires:

```ts
// env is the Environment.payloadEnv (default "")
flowToEnginePayload(entry, env) -> {
  env,
  flow: { flowId, method, path, tree, fixtures }   // fixtures: present-only mocks/expect
}

jdmToEnginePayload(entry, env) -> {
  env,
  jdmId,
  version: 0,                                       // 0 => engine auto-assigns max+1 (finding 7)
  doc                                               // the {nodes, edges} graph, passed through
}

connectionToEnginePayload(entry, env) -> {
  env,
  connection: {
    key, type,
    settings,                                       // discrete, credential-free (§3.3.1)
    secretRef,
    resilience: { timeoutMs, retry: { maxAttempts } } // pinned request shape (§3.3.2, finding 8)
  }
}
```

- `flowToEnginePayload` passes `tree` through (the canvas already serialized the engine `Node`
  shape, mixed casing preserved — §4.2), maps the `fixtures` component array to the FlowFixture
  array (omitting absent `mocks`/`expect`), carries `note`.
- `jdmToEnginePayload` passes the JDM graph through and **always sets `version: 0`** so the strict
  decoder gets the documented auto-assign (`slice-f` §2.8: `version<=0` ⇒ auto-assign) and never a missing-field surprise
  (finding 7).
- `connectionToEnginePayload` passes the discrete settings through and **asserts no secret value is
  present** — it throws a `TransformError` if the settings denylist matches (`password`, `pwd`,
  `secret`, `token`, `apikey`, `dsn`), re-applying the content-type guard's **superset** denylist
  (belt and suspenders; a strict superset of the engine edge guard, which adds `pwd` — see §3.3.1,
  finding 3). It emits the pinned resilience request shape.

Each transform returns the exact envelope the matching endpoint decodes with
`DisallowUnknownFields`; a round-trip unit test asserts no stray fields leak into the envelope
(finding 1).

### 5.3 Validation hook (`validation.ts`) — publish-blocking

Wired as a Strapi `beforePublish`-style lifecycle on Flow (and invoked explicitly by the publish
controller so the gate holds regardless of entry point). Note the **ordering change (finding 5)**:
the flow version is **persisted first**, then validated **by `{flowId, version}`** so the engine
actually marks it `validated` (an inline validate marks nothing). The full ordered sequence is §5.4;
the gate's contract is:

1. The flow version has been created (`createFlow` → version `N`, `validated=false`).
2. Call `admin-client.validateFlow(env, flowId, N)` in **stored mode** — body `{ env, flowId,
   version: N }` (top-level, no inline `flow`; §5.1.1) — so the engine loads the persisted version
   `N` and, on all-pass, marks it validated.
3. If the HTTP call fails (transport/5xx) → **block** publish, surface a "could not reach validation"
   error (recoverable: the author can retry).
4. If `ok:false` → **block** publish; store the `{structural, fixtures}` result on `lastValidation`
   and set `lastPublishStatus = failed`; the `ValidationPanel` renders the structural errors and
   per-fixture diffs inline (slice-d §5 response shape). The engine has **not** marked `N` validated,
   so even a stray `publishFlow` would 409.
5. If `ok:true` → the engine has marked `N` validated; proceed to `publishFlow` (§5.4). Optionally
   run `dryRunFlow` for a trace preview, but dry-run is **not** a gate (writes suppressed, may hit
   real read sources) — author-facing insight only.

A flow can **never** be published with `ok:false` or an unreachable validator — enforced twice: the
hook blocks, and the engine's `SetActive` 409s an un-validated version. This is the guarantee the §6
tests assert.

Connections and JDMs have no standalone validate endpoint in the contract; they are validated
transitively (a flow referencing an unknown `jdmId`/`connection key` fails `/admin/flows/validate`
with a `structural` error — slice-d §5 behavior 1). So the publish sequence (§5.4) creates
connections and JDMs **before** validating the flow that references them.

### 5.4 Publish sequence (orchestrated by `publish.ts` controller)

Triggered by an admin-only route `POST /rule-engine/flows/:id/publish` (Strapi RBAC: operator role
only). For a given Flow entry and its target Environment (which supplies `baseURL`, bearer token, and
`payloadEnv` — default `""`):

```
env = environment.payloadEnv            // default ""  (NOT the Environment name)

1. Reconcile connections (finding 11):
     present = admin-client.listConnections(env)        // GET /admin/connections
     for each referenced Connection by key:
       payload = connectionToEnginePayload(connEntry, env)
       if key absent in present OR settingsDiffer(present[key], payload) OR resilienceDiffer(ns->ms):
         admin-client.createConnection(env, payload)     // POST /admin/connections -> {key, version}
       else skip (unchanged; avoid PutConnectionVersion version churn)
2. For each referenced Jdm (by jdmId):
     admin-client.createJdm(env, jdmToEnginePayload(jdmEntry, env))   // POST /admin/jdms
     // create == ACTIVATE in one txn (finding 6); JDM is LIVE now.
     write back jdmEntry.engineVersion from the {jdmId, version} response
3. flowPayload = flowToEnginePayload(flowEntry, env)
4. {version:N} = createFlow(env, flowPayload)            // POST /admin/flows (validated=false)
     write back flowEntry.engineVersion = N
5. res = validateFlow(env, flowId, N)                    // POST /admin/flows/validate {env, flowId, version:N} (stored mode, §5.1.1)
     if transport/5xx OR res.ok == false: BLOCK (abort), store lastValidation, lastPublishStatus=failed
     // on all-pass the engine MarkValidated(N)
6. publishFlow(env, flowId, N)                           // POST /admin/flows/{id}/publish {env, version:N}
     if 409 "not validated": abort, surface "validate first" (should not happen after step 5 ok)
7. write back lastPublishStatus=published; clear lastValidation errors
```

Ordering rationale (finding 5): `slice-f` §2.3 lands a version `validated=false`; `slice-f` §2.6
marks it validated **only in stored mode, when validate references the persisted `{flowId, version}`
as top-level fields** (§5.1.1); `slice-f` §2.4 **409s publish on an un-validated version**. So create
(step 4) must precede validate (step 5), and validate must reference the created version `N` in
stored mode, before publish (step 6). The earlier validate-before-create ordering would have left
`N` un-validated and 409'd on publish.

Connection presence check (finding 11): step 1 now calls `listConnections` and **skips** keys already
present with matching settings/resilience, because `PutConnectionVersion` is append-and-activate
(`slice-f` §2.8) — re-sending an unchanged connection on every publish causes needless version
churn. The comparison is deliberately **conservative so a spurious diff never defeats the
churn-avoidance goal (finding 5)**:
- **Resilience**: the GET response encodes `timeout` in nanoseconds with a nested `retry`
  (§3.3.2); the client normalizes it to the CMS's ms shape (`timeout` ns → `timeoutMs`) before
  comparing, then compares only `timeoutMs` + `retry.maxAttempts`.
- **Settings**: compared on the **authored keys present in the CMS entry only** — the GET response
  may include driver-defaulted keys the author never set (e.g. `port` defaulting to `5432`,
  `sslmode` to `disable`, or a different key order), and a naive deep-equal over the full response
  would report a false diff and re-create. So the client compares each key the CMS authored against
  the response value (filling the same documented driver defaults on the CMS side before compare),
  and ignores response-only keys.
- **Inconclusive comparison errs toward *skip* when only defaults/ordering differ.** If the
  comparison cannot cleanly decide (unexpected shape in the response), the step treats it as a
  **harmless re-create** rather than silently skipping a genuine change — an extra immutable
  connection version is benign (append-only, no pointer semantics beyond re-activate), whereas
  skipping a real change would publish stale config. So: skip only when authored keys demonstrably
  match; otherwise re-create.

Each step is logged (structured) with `flowId`, `env`, and the engine-returned version. The sequence
is **not** transactional across the admin API (the engine owns per-object transactionality); if a
later step fails after earlier creates succeeded, the created-but-unpublished flow version is a
harmless immutable row (no active pointer moved) — the author re-runs publish. JDM creates, however,
**did** activate (finding 6), so a mid-sequence failure after step 2 leaves the new JDM live; this is
acceptable because a JDM only takes effect when a published flow references it, and the operator
re-runs to completion. Rollback is a separate explicit action (`rollbackFlow`) exposed as its own
admin route.

### 5.5 Error handling (concrete, per operation)

| Operation | Failure | Recoverable? | Caller receives | Logged? |
| --- | --- | --- | --- | --- |
| `listConnections` | 5xx/transport | yes | publish blocked; "engine unavailable, retry" | warn, `env` |
| `createConnection`/`createJdm`/`createFlow` | 4xx (bad payload / 400 unknown field / `dsn` in settings) | no (author must fix) | publish aborted; field-level error; `lastPublishStatus=failed` | error, redacting any value |
| `createConnection`/`createJdm`/`createFlow` | 5xx/transport | yes | publish aborted; "engine unavailable, retry" | error |
| `validateFlow` | transport/5xx | yes | publish blocked; "validation unreachable, retry" | warn, `flowId/env` |
| `validateFlow` | 200 `ok:false` | yes (fix flow) | publish blocked; structural+fixture diffs in `ValidationPanel` | info (expected negative) |
| `publishFlow`/`rollbackFlow` | **409 un-validated version** | no | abort; "validate first" (should not occur post-validate) | error |
| `publishFlow`/`rollbackFlow` | 404 version absent | no | abort; "version not found" | error |
| `publishFlow`/`rollbackFlow` | 5xx/transport | yes | abort; retry | error |
| transform (`connectionToEnginePayload`) | secret value detected in settings | no (author must remove) | publish aborted BEFORE any admin call; "remove secret, use secretRef" | error, **never log the value** |
| auth | 401 from admin plane (missing/invalid bearer) | no | "operator credential invalid/expired" | error, token redacted |

The operator token is referenced by key, never echoed to logs or responses. Any admin response body
is logged with a redactor that drops keys on the secret denylist.

### 5.6 Input validation rules (CMS edge, before any admin call)

| Input | Required | Type/limit | On failure |
| --- | --- | --- | --- |
| `flowId`/`jdmId`/`connection.key` | yes | `^[a-z0-9][a-z0-9-]{0,63}$` | content-type validation error; save rejected |
| `method` | yes | enum GET/POST/PUT/PATCH/DELETE | enum guard |
| `path` | yes | matches ServeMux pattern `^/[A-Za-z0-9/_{}.-]+$`, ≤ 512 chars | validation error |
| `tree` | yes | parseable JSON; root node `type == trigger`; depth ≤ engine bound (R8) | field error; publish blocked (engine re-checks structurally) |
| `connection.type` | yes | enum postgres/valkey/rest | enum guard |
| `connection.settings` | yes | JSON object; **no `dsn`**; no secret-denylist keys | lifecycle guard rejects (§3.3) |
| `secretRef` | optional | string ≤ 256, opaque reference | — |
| `resilience` | optional | `timeoutMs` int ≥ 0; `retry.maxAttempts` int ≥ 1 | component validation |
| `fixtures[].name` | yes (if fixtures present) | non-empty, unique within flow | component validation |
| `fixtures[].mocks`/`[].expect` | optional (nullable) | JSON object when present | — (finding 9) |
| `ADMIN_API_BASE_URL` | yes (env or Environment) | valid `http(s)://` URL | plugin bootstrap fails fast with a clear message |
| `ADMIN_API_OPERATOR_TOKEN` | yes (env or Environment) | non-empty plaintext bearer | bootstrap fails fast |
| `payloadEnv` | optional | string; default `""`; must equal target engine's served env | engine 400s a mismatch (surfaced) |

Invariant ownership: the CMS owns **shape/edge** validation (fail fast, good UX); the **engine owns
semantic/structural authority** via `/admin/flows/validate` and its own version/pointer transaction
(slice-d/slice-f). The CMS never assumes its local checks are sufficient to publish — the engine's
validate is always the final publish-blocking gate (R11: both sides validate the contract).

---

## 6. Test strategy

Tests use **vitest** with **nock** mocking the admin API HTTP endpoints — no real engine, no real
network, no Strapi DB needed for the unit layer. The two things under test are exactly the two the
task names: the **publish-transform** and the **validation-hook/publish-sequence** logic. Every
nock interceptor asserts the **wrapped envelope** and the **`Authorization: Bearer` header**.

### 6.1 Unit — publish-transform (pure functions)
- `flowToEnginePayload` wraps as `{ env:"", flow:{...} }` and reproduces the `seed.json` flow trees
  (structure round-trip) for `fmc-order-by-id` / `orders-expedite`, preserving the mixed spec casing
  (finding 10). Assert **no stray top-level fields** (strict-decoder safety, finding 1).
- Fixtures array maps to the FlowFixture shape with `mocks`/`expect` **omitted when absent**
  (finding 9); a `{name, input}`-only seed fixture round-trips.
- `jdmToEnginePayload` wraps as `{ env:"", jdmId, version:0, doc }` and reproduces `seed.json` JDM
  graphs (`fmc-payment`, `order`); assert `version === 0` is always present (finding 7).
- `connectionToEnginePayload` wraps as `{ env:"", connection:{...} }`, passes `secretRef` through,
  emits resilience as `{ timeoutMs, retry:{ maxAttempts } }` (finding 8), and **throws** when a
  secret value appears in `settings` (assert the denylist catches `password`, `dsn`, `token`, etc.).
  Fixtures are **credential-free discrete-shape** connections (`host/port/database` + `secretRef`),
  NOT the literal seed DSNs (finding 3); a test explicitly asserts a seed-style `dsn` connection is
  **rejected** by the transform.

### 6.2 Unit — flow-canvas serializer (UI half)
- Canvas graph → engine tree → canvas graph round-trip over the seed trees (branch keys, nesting,
  control-node children preserved; layout kept in sidecar, absent from the engine tree).
- **Casing assertion (finding 10)**: `operation.{Kind,Payload,Required}` emitted capitalized; `set`
  `targetPath`/`from`, `condition` `trueKey`/`falseKey`, `decision` `jdmId`/`input`/`saveAs` emitted
  camelCase. A naive all-camelCase serialization fails this test.

### 6.3 Integration — validation hook + publish sequence (nock-mocked admin API)
Each test asserts the **exact calls, order, wrapped payloads, and bearer header**, and the
**blocking behavior**:
- **Happy path**: assert order `listConnections` → `createConnection*` (only missing keys) →
  `createJdm*` (each `{env:"", jdmId, version:0, doc}`) → `createFlow` → `validateFlow` (stored mode,
  body `{env:"", flowId, version:N}` — §5.1.1) → `ok:true` → `publishFlow` (`{env:"", version:N}`);
  every request carries `Authorization: Bearer <token>`.
  - **Write-back source keys pinned (finding 4)**: the test asserts the version write-backs read
    **distinct** response keys — `flowEntry.engineVersion` from **`createFlow().version`** (the
    create response is `{flowId, version, validated:false}`), `jdmEntry.engineVersion` from
    **`createJdm().version`** (`{jdmId, version}`), and the active-pointer read (where surfaced) from
    **`publishFlow().activeVersion`** (`{flowId, activeVersion, action}`). Pinning the exact source
    key prevents cross-wiring `version` (create) with `activeVersion` (publish/rollback).
- **Validation blocks publish (finding 9)**: use a fixture that **declares an `expect`** the engine
  fails; `validateFlow` → `200 {ok:false, fixtures:[{passed:false, diff}]}`; assert `publishFlow` is
  **never called** (nock interceptor asserts zero hits), `lastPublishStatus=failed`, diff stored on
  `lastValidation`. (The flow version `N` was created but never validated — harmless.)
- **Validator unreachable**: `validateFlow` → 503; assert publish blocked, `publishFlow` never
  called, recoverable error surfaced.
- **Publish 409 (un-validated)**: force `publishFlow` → 409; assert abort + "validate first" surfaced
  (defense-in-depth path, finding 5).
- **Secret in settings**: transform throws before any admin call; assert **zero** admin HTTP calls.
- **`dsn` in settings (finding 3)**: a connection carrying `settings.dsn` is rejected by the
  transform before any call; assert zero admin HTTP calls.
- **Create fails mid-sequence**: `createFlow` → 500 after connections/JDMs; assert `validateFlow`/
  `publishFlow` not called, `lastPublishStatus=failed`.
- **Connection reconcile (findings 11, 5)**: `listConnections` returns an existing key whose GET
  response carries driver-defaulted keys the author never set (`port:5432`, `sslmode:"disable"`) and
  resilience in ns; assert `createConnection` is **not** called for it (settings compared on authored
  keys with defaults filled, resilience ns→ms normalized), proving a defaulted/ns response does not
  trigger a spurious re-create. A separate case with a genuinely changed `maxConns` **does** call
  `createConnection`.
- **Operator credential**: assert every intercepted admin request carries
  `Authorization: Bearer <token>` from env/Environment, and that no interceptor ever receives a
  secret value or the token in a body.
- **Rollback**: `rollbackFlow` route calls `POST /admin/flows/{id}/rollback` with `{env:"", version}`
  for the prior version.

### 6.4 Admin plugin smoke (lightweight)
- Custom-field registration: both fields register with base `type: 'json'` (finding 13) without
  throwing; a malformed stored JSON renders the raw-JSON fallback, not a crash.

CI runs `npm ci && npm run test` inside `cms/` (its own job, separate from the Go CI). No Docker/DB
is required for the unit+integration layer because the admin API is mocked and the transform/hook
logic is isolated from Strapi's DB. (A fuller end-to-end against a live admin API is a follow-up once
`feat/admin-api` merges — §8.)

---

## 7. Dependency pinning

**All dependencies are pinned to exact versions — no `^`, `~`, or other open ranges** — in both
`dependencies` and `devDependencies`, with `package-lock.json` committed and `npm ci` used in CI
(`[[config-and-secrets]]` / supply-chain discipline; task requirement 7). Set `save-exact=true` in an
`.npmrc` so future `npm install` keeps pinning.

Direct dependencies (exact):

```jsonc
// package.json (excerpt) — exact pins only
{
  "engines": { "node": "22.x" },
  "dependencies": {
    "@strapi/strapi": "5.56.0",
    "@strapi/design-system": "2.2.4",
    "@strapi/icons": "2.2.4",
    "@gorules/jdm-editor": "1.52.0",
    "reactflow": "11.11.4",
    "react": "18.3.1",
    "react-dom": "18.3.1",
    "react-router-dom": "6.30.3",
    "styled-components": "6.1.13",
    "pg": "8.13.1",
    "zod": "3.24.2"
  },
  "devDependencies": {
    "@strapi/sdk-plugin": "6.1.1",
    "vitest": "2.1.8",
    "nock": "13.5.6"
  }
}
```

Notes on these pins (all verified via `npm view` 2026-10-03):
- `react`/`react-dom` **18.3.1** satisfy Strapi 5 (`react ^18.0.0`), jdm-editor (`react >= 18`), and
  design-system (`react ^18.0.0`). React 19 (19.3.0) exists and is **deliberately avoided** (Strapi
  5.56 peers on 18).
- `reactflow` **11.11.4** matches the version jdm-editor **bundles** (its `dependencies.reactflow ===
  "11.11.4"`), so only one React Flow runtime ships. `@xyflow/react` v12 (12.12.0) is NOT used (§4.2).
- `@strapi/icons` **2.2.4** satisfies the design-system peer `@strapi/icons ^2`, pinned to match the
  design-system release.
- `react-router-dom` **6.30.3** and `styled-components` **6.1.13** satisfy Strapi 5's declared peers
  (`react-router-dom ^6.30.3`, `styled-components ^6.0.0`); pinned exact to the current satisfying
  release.
- `zod` **3.24.2** matches jdm-editor's `zod ^3.24.2`; a direct pin keeps a single zod v3 line (zod 4
  exists but is not used — jdm-editor is on v3).
- `vitest` **2.1.8** and `nock` **13.5.6**: newer majors exist (vitest 5.x, nock 14.x) but the
  conservative pins match the Node 22 + CJS/ESM interop the Strapi plugin test harness uses; revisit
  on a later toolchain bump.
- Transitive deps are pinned by the committed `package-lock.json`; any transitive with a known
  advisory is addressed via an npm `overrides` pin (also exact) rather than a range.
- A new/unusual package name is reviewed before adding (typosquat guard); every package above is a
  well-known, actively maintained one.

---

## 8. Out of scope / follow-ups (documented, not built here)

- **One-click cross-env promotion UI** driving `PromoteVersion` with a visual dev→staging→prod
  pipeline. v1 models promotion as "re-run publish against the target Environment" (§3.4).
- **End-to-end tests against a live admin API** — deferred until `feat/admin-api` merges; v1 mocks
  the admin API (§6).
- **Migration to `@xyflow/react` v12** — tracked to follow jdm-editor's own React Flow upgrade (§4.2).
- **Secret-backend integration** (Vault/SSM) for resolving `operatorTokenRef`/`secretRef` beyond env
  — the CMS stores only references; wiring a concrete backend is engine/ops scope (R12).
- **In-CMS audit viewer** — `GET /admin/audit/{type}/{id}` **is** mounted (`slice-f` §2.1/§2.8), and
  the `admin-client` exposes a typed `audit(type, id)` method for it, but a polished in-product audit
  timeline (flow/jdm/connection history with actor + from/to version) is a follow-up; v1 ships the
  client method only (finding 12).
- **DB-level operator audit attribution** — `slice-f` §3.5 proposes an actor-on-context seam so each
  admin write attributes to the operator subject; its fallback attributes all admin writes to a
  single `"admin"` actor with the operator subject in the access log. Either way this is an
  engine-side concern, not a CMS one.
- **Full RBAC role matrix** beyond the single operator role gating publish.

---

## 9. Traceability

- Content types ↔ engine model: §3 maps every field to `slice-d-configstore` §1–2 and
  `lld-contracts` (`FlowVersion`, `ConnectionDef`, `flow_fixtures`, `jdm_versions`); the Connection
  settings shape is reconciled against the real Slice B driver `buildDSN`.
- Editors: §4 uses `@gorules/jdm-editor` (ADR-001/HLD §5) + React Flow (HLD §5), versions and peer
  ranges verified via `npm view`; both custom fields register with base `type: 'json'`.
- Validation gate: §5.3 enforces `/admin/flows/validate` as publish-blocking (HLD §6b, AC-13,
  slice-d §5, slice-f §2.6), with the create→validate(stored-mode by version)→publish ordering.
- Publish transform through the admin API only (never direct DB): §5.1–5.4 — wrapped envelopes
  carrying `env=""`, `Authorization: Bearer` (ADR-005, STATE.md admin plane, slice-f §2–3, task
  contract).
- `secret_ref` only, never a value: §3.3 + §5.2 + §5.5 (`[[config-and-secrets]]`, slice-d §2,
  slice-f §2.8, AC-20).
- Tests mock the admin API and assert wrapped payloads + bearer header + that validation failure
  blocks publish: §6 (task requirement 6).
- Exact-version pinning: §7 (task requirement 7).

---

## 10. Responses to the design review

### 10.1 Round 2 → round 3 (current revision)

Reviewed doc: `docs/.agents/tasks/strapi-cms/design-review.md` (round 2, verdict CHANGES_REQUESTED;
0 HIGH, 3 MEDIUM, 2 NIT). All 5 findings resolved; none backlogged. Each resolution was checked
against the **real** source in the worktree (`.worktrees/admin-api/docs/lld/slice-f-admin-api.md`
and `internal/connect/drivers/postgres.go`), and the stale `slice-f §3.x` section citations
inherited from round 2 were corrected to the real spec's `§2.x`/`§3`/`§9` numbering.

| # | Sev | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | MED | Validate `version` placement unspecified; strict decoder makes a wrong guess a hard 400 | **Fixed.** New §5.1.1 pins the publish gate to `slice-f` §2.6 **stored mode**: body `{ env, flowId, version:N }` as **top-level siblings**, no `flow` wrapper (the inline `flow` is ignored in stored mode). §5.1 table, §5.3 step 2, §5.4 step 5 all updated to the stored-mode shape; §6.3 happy-path asserts the body verbatim. (Note vs the reviewer's suggested `{env, flow, version}`: the real spec keys stored-mode marking off `flowId`+`version` and ignores inline `flow`, so the design pins the spec's actual stored-mode shape, which is also decoder-safe.) |
| 2 | MED | Connection settings diverge from an example (`schema`, flat `poolMax`) without reconciling | **Fixed.** §3.3.1 adds an explicit statement: the CMS follows the **driver** shape (verified against `postgres.go` — `host/port/database/user/sslmode` + nested `pool.{maxConns,minConns}`), deliberately omits a flat `poolMax` (inert — driver reads nested `pool.*`) and a `schema` key (schema qualification lives in the SQL, as the seed's `fmc_order.order_status` does), and notes that supporting either is a driver change first. |
| 3 | MED | Denylist adds `pwd` but is described as "mirroring" the engine guard | **Fixed.** §3.3.1 now states the CMS denylist is a **strict superset** of the engine edge guard (`slice-f` §2.8: `password/secret/token/apiKey` + blanket `dsn`), explicitly calling out `pwd` as CMS-only and dropping the "mirror" framing; §5.2 reworded from "mirroring" to "re-applying the superset denylist". |
| 4 | NIT | Write-back source keys not pinned (`version` on create vs `activeVersion` on publish) | **Fixed.** §6.3 happy-path now asserts distinct source keys: `engineVersion` from `createFlow().version` and `createJdm().version`; the active-pointer read from `publishFlow().activeVersion`. |
| 5 | NIT | `listConnections` settings diff underspecified — spurious diff could defeat churn-avoidance | **Fixed.** §5.4 presence-check paragraph specifies: compare only CMS-authored settings keys (filling the same documented driver defaults before compare, ignoring response-only defaulted keys), normalize resilience ns→ms, and err toward a harmless re-create when inconclusive. §6.3 reconcile test asserts a defaulted/ns response does **not** trigger a re-create. |

**Round-3 correction (not a review finding, caught while verifying source) — R3-C (audit route).**
The round-2 design claimed `GET /admin/audit/{type}/{id}` was **not** mounted in `slice-f`. Re-reading
`slice-f` §2.1 (mount block) and §2.8 shows the route **is** mounted. The design is corrected (§5.1,
§8, §9): the CMS exposes a typed `audit()` client method against the real route and defers only the
in-product audit **viewer** as editorial scope — the earlier `NotImplemented` stub framing is
removed. Flagged here for traceability since it contradicts a round-2 "fixed" claim.

### 10.2 Round 1 → round 2 (prior revision)

Reviewed doc (round 1): verdict CHANGES_REQUESTED; 4 HIGH, 5 MEDIUM, 4 NIT. All 13 findings addressed;
none backlogged.

| # | Sev | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | HIGH | Bodies must be wrapped envelopes with `env`; engine uses `DisallowUnknownFields` | **Fixed.** §5.1/§5.2 emit `{env, flow}` / `{env, connection}` / `{env, jdmId, version, doc}`; §6.1 asserts no stray fields. |
| 2 | HIGH | Payload `env` must be `""`, not the Environment name; cross-env is by base URL | **Fixed.** §3.4 adds `payloadEnv` (default `""`), `name` selects base URL/token only; §5.1/§5.4 send `env=payloadEnv`. |
| 3 | HIGH | Seed connections embed credentials in `settings.dsn`; collides with denylist; CT has no field for the real shape | **Fixed.** §3.3/§3.3.1 reshape Connection to the real driver's discrete `host/port/database/...` + `secretRef`; `dsn` is a blanket denylist reject; §6.1 uses credential-free fixtures and asserts a `dsn` connection is rejected; seed DSNs declared a dev-only artifact the CMS does not reproduce. |
| 4 | HIGH | Operator header unnamed; `ADMIN_API_OPERATOR_TOKEN` conflated with engine's hash list | **Fixed.** §5.1 specifies `Authorization: Bearer <ADMIN_API_OPERATOR_TOKEN>` (CMS plaintext bearer) and documents the engine's separate `ADMIN_OPERATOR_TOKENS` (sha256 hashes); the CMS never configures the engine var. |
| 5 | MED | Sequence validates inline before create; publish then 409s | **Fixed.** §5.4 reorders create → validate(by `{flowId, version:N}`) → publish; §5.3/§5.5 add 409 handling. |
| 6 | MED | JDM create also activates; no "after publish" step | **Fixed.** §3.2/§5.1/§5.4 state `createJdm` creates+activates in one txn; `engineVersion` written from the create response; JDM live on create (called out). |
| 7 | MED | JDM payload omits `version` | **Fixed.** §5.2 emits `version: 0`; §6.1 asserts it is present. |
| 8 | MED | Resilience sub-shape + connection create response unpinned | **Fixed.** §3.3.2 pins the request shape `{timeoutMs, retry:{maxAttempts}}` and the GET ns-response shape; §5.1 states create response `{key, version}`; §6.1 asserts emission. |
| 9 | MED | Seed fixtures assert nothing; `mocks`/`expect` glossed | **Fixed.** §3.1.1 makes `mocks`/`expect` nullable; §6.3 uses an asserting fixture for the blocking test. |
| 10 | NIT | Mixed operation key casing (`Kind`/`Payload`/`Required` vs camelCase) | **Fixed.** §4.2 pins the casing; §6.2 adds a casing assertion. |
| 11 | NIT | `listConnections` listed but unused; version churn | **Fixed.** §5.4 step 1 wires `listConnections` + presence/diff check (ns→ms normalized) and skips unchanged keys. |
| 12 | NIT | audit-read endpoint | Addressed; **corrected in round 3** — see §10.1 finding R3-C: the route IS mounted (`slice-f` §2.1/§2.8); the CMS exposes a typed `audit()` client method and defers the in-product audit viewer as scope. |
| 13 | NIT | Custom-field base type not stated | **Fixed.** §4/§4.1/§4.2 state both fields register with base `type: 'json'`. |

Verified-assumption items from the review that needed independent confirmation this round:
- **npm versions (review "UNVERIFIED").** Re-verified via `npm view` 2026-10-03: Strapi 5.56.0
  (`node >=20 <=26`, `react ^18`), jdm-editor 1.52.0 **bundles `reactflow@11.11.4`** and
  `@gorules/zen-engine-wasm@0.23.1` and peers `react >= 18`, design-system 2.2.4 peers `react ^18` +
  `@strapi/icons ^2` (added `@strapi/icons@2.2.4` to the pins), react 19 exists so pinning 18.3.1 is a
  real choice. All design pins confirmed accurate.
- **Engine connection settings schema (review "UNVERIFIED", feeds findings 3/8).** Read
  `internal/connect/drivers/postgres.go`: `buildDSN` accepts a `dsn` OR discrete
  `host/port/database/user/sslmode` + `pool`, with the password injected from the resolved secret.
  The CMS authors the discrete, credential-free shape (§3.3.1).
- **`GET /admin/audit` route (review "UNVERIFIED", finding 12) — re-checked round 3.** The round-2
  claim ("route not mounted") was **wrong**: `slice-f` §2.1's mount block registers
  `GET /admin/audit/{type}/{id}` (handler `auditTrail`) and §2.8 documents its request/response. The
  route **is** in the contract. Corrected in §5.1 and §8: the CMS ships a typed `audit()` client
  method and defers only the in-product audit **viewer** (editorial scope), not because the endpoint
  is missing.
