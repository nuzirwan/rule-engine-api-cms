# Design Review: Flow Templates (TASK-006)

_Revision 3 — reviewed against the actual worktree codebase_

---

## Summary

The design is well-structured and has clearly absorbed feedback from two prior review rounds. It is grounded in the real codebase in most respects — the Go struct shapes, error taxonomy, Strapi document service patterns, CMS controller factory style, and validation pipeline are all correctly matched against the actual code. Two HIGH findings block approval: the CRUD template skeleton specifies a wrong field name in response nodes (`"body"` instead of the real field `"bodyFrom"`), and it uses an invalid Postgres operation kind (`"sql"` instead of `"query"`/`"exec"`). Both are skeleton-level mistakes that would be faithfully copied into the JSON template files, producing flows that either fail validation at instantiation time or fail silently at runtime.

---

## Numbered Findings

### Finding 1 — HIGH

**Wrong response spec field: `"body"` vs `"bodyFrom"`**

Location: Section 4 parameterized flow skeleton; Section 4 descriptions for all CRUD, webhook, and data-sync template response nodes.

The design's CRUD skeleton shows:
```jsonc
{
  "id": "{{ENTITY}}-get-response",
  "type": "response",
  "spec": { "status": 200, "body": "{{data.row}}" },
  "children": []
}
```

The actual `ResponseSpec` in `engine/internal/flow/spec.go` is:
```go
type ResponseSpec struct {
    Status   int    `json:"status"`
    BodyFrom string `json:"bodyFrom,omitempty"`
}
```

`parseSpec[T]` (called by `ValidateTree` for every response node) uses `json.NewDecoder().DisallowUnknownFields()`. An unknown field `"body"` causes a `bad_spec` issue, so `ValidateTree` rejects the tree and `SubstituteTemplate` returns a `Validation` error. Every template that copies this skeleton will fail at instantiation time — flows are never created.

**All existing flows in `seed.json` use `"bodyFrom": ""`.** Non-empty `bodyFrom` navigation is declared in the spec comment ("otherwise it names a dotted path") but the `responseHandler.Exec` implementation does not yet act on a non-empty value — it validates the spec and returns `Stop:true`. Templates that want a usable response body today should use `"bodyFrom": ""`, which returns the full accumulated `Ctx.Response` as-is.

**Concrete fix:** Replace `"body"` with `"bodyFrom"` throughout all response node spec examples in Section 4. Use `"bodyFrom": ""` for all response nodes (matching the existing seed.json pattern). Drop `"{{data.row}}"` etc. — `BodyFrom` path navigation is not yet wired and would silently return an empty body.

---

### Finding 2 — HIGH

**Invalid Postgres operation kind `"sql"`**

Location: Section 4, CRUD-REST-Postgres skeleton; Section 4, data-sync template description.

The CRUD skeleton uses:
```jsonc
"operation": {
  "Kind": "sql",
  "Payload": { "sql": "SELECT ...", "params": [...] },
  "Required": true
}
```

The Postgres driver in `engine/internal/connect/drivers/postgres.go` only handles three kinds:
- `"query"` — SELECT; returns `[]map[string]any` rows
- `"exec"` — INSERT/UPDATE/DELETE; returns `{rowsAffected: N}`
- `"ping"` — health probe

The `"sql"` kind falls into the `default` case and returns `"unsupported operation kind for postgres"` at runtime. `ValidateTree` does **not** validate operation kind — so the substituted flow passes validation and Strapi creates the draft without error. The failure only appears when a user tries to execute the flow, making it a silent breakage.

`seed.json` confirms: every Postgres operation uses `"Kind": "query"` (not `"sql"`).

**Concrete fix:**

| Flow | SQL type | Correct Kind |
|---|---|---|
| `{{ENTITY}}-list` | SELECT | `"query"` |
| `{{ENTITY}}-get` | SELECT | `"query"` |
| `{{ENTITY}}-create` | INSERT | `"exec"` |
| `{{ENTITY}}-update` | UPDATE | `"exec"` |
| `{{ENTITY}}-delete` | DELETE | `"exec"` |
| `data-sync` read action | SELECT | `"query"` |
| `data-sync` write action | INSERT | `"exec"` |

---

### Finding 3 — MEDIUM

**Partial create success — `done` step UI rendering unspecified**

Location: Section 7, `TemplatesPage.tsx` state machine and `TemplatePreview/index.tsx`.

The controller section (§6) specifies that on partial failure (≥1 flow created, ≥1 failed) the endpoint returns HTTP 200 with `{ created: [...], errors: [...], count }`. But the UI state machine defines `done` as:
```typescript
| { name: 'done'; created: CreatedFlowRef[] }
```
There is no `errors` field in the `done` state, and Section 7 only describes the full-success rendering ("shows a link to each created flow"). The implementer has no guidance on whether partial failure transitions to `done` with a warning or stays on the `preview` step with an error.

**Concrete fix:** Update the `done` step union to:
```typescript
| { name: 'done'; created: CreatedFlowRef[]; errors?: Array<{ flowId: string; message: string }> }
```
Add to Section 7: "When `instantiateTemplate` returns 200 with a non-empty `errors` array, the page transitions to `done` with both `created` and `errors` populated. The `done` step renders created flow links as normal, plus a warning callout below listing the flows that failed and their error messages. The operator can delete the created drafts and retry."

---

### Finding 4 — NIT

**Webhook and data-sync response node `bodyFrom` value never specified**

Location: Section 4, webhook-handler and data-sync template descriptions.

Both templates are described with "response 200" or "response 200 with `result`" but neither provides the full `ResponseSpec` JSON for the response node. After Finding 1 (wrong field name), an implementer who fixes `"body"` → `"bodyFrom"` still doesn't know what value to put for the webhook's response or the sync result.

**Concrete fix:** Add explicit response node specs:
- Webhook handler: `"spec": { "status": 200, "bodyFrom": "" }` (returns full engine response)
- Data-sync read action: `"saveAs": "source"` and write action `"saveAs": "result"`, then response: `"spec": { "status": 200, "bodyFrom": "" }` (returns `Ctx.Response` accumulated from Set nodes, or full Data map until `BodyFrom` navigation is wired)

---

## Verified Assumptions

All of the following were spot-checked against source files:

| Assumption | File checked | Result |
|---|---|---|
| `flow.ValidateTree` accepts nil RefResolver (uses `nopRefs{}`) | `engine/internal/flow/validate.go` | ✓ Confirmed |
| `//go:embed` resolves relative to package dir — templates at `engine/internal/config/templates/` | `engine/internal/config/*.go` structure | ✓ Confirmed |
| `strapi.documents('api::flow.flow').create({ data: {...} })` available | `cms/.../controllers/sync.ts` | ✓ Confirmed |
| `resolveJsonModule: true` in all relevant tsconfigs | `cms/tsconfig.json`, `cms/src/admin/tsconfig.json` | ✓ Confirmed |
| `useEnvironment()` returns `{ selectedEnv }` with `selectedEnv.documentId` | `admin/src/contexts/EnvironmentContext.tsx`, `contexts/types.ts` | ✓ Confirmed |
| Route config shape `config: { policies: [] }` | `server/src/routes/index.ts` | ✓ Confirmed |
| `TriggerSpec` has `method`, `path`, `input` fields | `engine/internal/flow/spec.go` | ✓ Confirmed |
| `post()` from `useFetchClient` accepts body object directly | `admin/src/components/SyncPanel/index.tsx`, `EnvironmentManager/index.tsx` | ✓ Confirmed |
| `parseSpec` uses `DisallowUnknownFields` — unknown spec fields cause `bad_spec` | `engine/internal/flow/spec.go` | ✓ Confirmed |
| `FlowVersion` struct shape (FlowID, Version, Method, Path, Tree, Fixtures) | `engine/internal/config/store.go` | ✓ Confirmed |
| `environment` on `api::flow.flow` is a Strapi relation (not a plain string) | `cms/.../controllers/publish.ts` (`populate: ['environment']`) | ✓ Confirmed |
| `Strapi.documents().create()` relation uses `connect: [{ documentId }]` syntax | Design reasoning consistent with sync.ts pattern | ✓ Plausible |
| Controller factory pattern — `export default function xxxController({ strapi })` | `cms/.../controllers/flow.ts`, `sync.ts` | ✓ Confirmed |
| `WebhooksPage` exists as a UI reference for gallery style | `admin/src/pages/WebhooksPage.tsx` | ✓ Confirmed |

---

## Unverified / Wrong Assumptions

| Assumption | Status | Detail |
|---|---|---|
| `"body"` is a valid JSON field in `ResponseSpec` | **WRONG** | Actual field is `"bodyFrom"`. `"body"` causes `bad_spec` via `DisallowUnknownFields` (Finding 1). |
| `"Kind": "sql"` is a valid Postgres operation kind | **WRONG** | Valid kinds: `"query"`, `"exec"`, `"ping"`. `"sql"` fails at runtime (Finding 2). |
| `ResponseSpec.bodyFrom` path navigation is wired in the response handler | **NOT CONFIRMED** | `responseHandler.Exec` validates spec and stops walk — it does not navigate a non-empty `bodyFrom` path. All existing flows use `"bodyFrom": ""`. Non-empty path navigation appears to be planned but not yet implemented. |
| Duplicate `flowId` Strapi error shape | **ACKNOWLEDGED UNVERIFIED** | Design correctly flags this as unverified. The controller's fallback (surface raw message) is a reasonable approach. |

---

## Verdict

CHANGES_REQUESTED — 2 HIGH findings, 1 MEDIUM finding.

The two HIGHs (wrong response spec field, invalid Postgres kind) mean the CRUD template will fail either at instantiation time (`bad_spec` from `ValidateTree`) or silently at runtime (unsupported kind). Neither is a minor editorial issue — the JSON template files cannot be authored correctly from the skeleton as written. Both fixes are surgical and don't require restructuring the design.
