# Analysis: Audit-Viewer UI for Strapi CMS

## Task Restatement

Implement an audit viewer UI component in the CMS admin panel that:
1. Shows audit trails for Flows, JDMs, and Connections
2. Displays version history with timestamps, actors, and changes
3. Integrates with the existing `AdminClient.audit(type, id)` method
4. Follows Strapi 5 admin UI patterns already established in the codebase

## Requirements

### Functional
- Display audit history for three entity types: `flow`, `jdm`, `connection`
- Show each audit entry with:
  - Action (e.g., `create_version`, `publish`, `rollback`)
  - Actor (who made the change, e.g., `op:alice`)
  - Timestamp (`at`)
  - Version info (`fromVersion`, `toVersion` — nullable)
  - Reason (optional)
- Entries are ordered newest-first (server returns them that way)

### Non-Functional
- Must pass `npm run build`
- Must pass `npm run test` (vitest)
- Follow Strapi 5 admin UI patterns (use `@strapi/design-system` components)

## Affected Code

### Existing Files to Leverage

1. **`src/plugins/rule-engine/server/src/services/admin-client.ts`**
   - Contains `AdminClient.audit(type, id)` method (lines 181-188)
   - Returns `Promise<unknown>` — needs typed response
   - Endpoint: `GET /admin/audit/{type}/{id}`

2. **`src/plugins/rule-engine/admin/src/index.tsx`**
   - Plugin admin entry point
   - Registers custom fields via `app.customFields.register()`
   - Currently only registers `flow-canvas` and `jdm-editor` fields

3. **`src/plugins/rule-engine/admin/src/components/ValidationPanel/index.tsx`**
   - Existing read-only panel displaying validation results
   - Good pattern reference for the audit viewer:
     - Uses `@strapi/design-system` components (Box, Flex, Typography)
     - Handles nullable/missing data gracefully
     - Renders structured data (arrays of entries)

4. **`src/plugins/rule-engine/server/src/routes/index.ts`**
   - Plugin server routes (currently only publish endpoint)
   - May need an audit proxy route if fetching from admin panel

### Files to Create

1. **`src/plugins/rule-engine/admin/src/components/AuditViewer/index.tsx`**
   - The main UI component
   - Should accept `type` ('flow' | 'jdm' | 'connection') and `id` props
   - Fetches via AdminClient or a CMS proxy route
   - Displays the audit trail entries

2. **Types for audit response** (in `types/engine.ts` or local):
   ```typescript
   interface AuditEntry {
     action: string;
     fromVersion: number | null;
     toVersion: number | null;
     actor: string;
     at: string; // ISO timestamp
     reason: string;
   }
   
   interface AuditTrailResponse {
     objectType: 'flow' | 'jdm' | 'connection';
     objectId: string;
     entries: AuditEntry[];
   }
   ```

## Engine Audit API Contract

From `engine/internal/httpapi/admin_handlers.go` (lines 266-296):

**Endpoint:** `GET /admin/audit/{type}/{id}`
- `{type}` ∈ `{flow, jdm, connection}` (400 if invalid)
- Requires Bearer token auth (like all admin endpoints)

**Response (200):**
```json
{
  "objectType": "flow",
  "objectId": "orders",
  "entries": [
    {
      "action": "publish",
      "fromVersion": 6,
      "toVersion": 7,
      "actor": "op:alice",
      "at": "2026-10-03T...Z",
      "reason": ""
    },
    {
      "action": "create_version",
      "fromVersion": null,
      "toVersion": 7,
      "actor": "op:alice",
      "at": "...",
      "reason": ""
    }
  ]
}
```

Entries are **newest-first** (store orders by `at DESC, id DESC`).

## Integration Pattern

The existing `ValidationPanel` is a **read-only surface** that displays data stored on the Flow entry's `lastValidation` JSON field. It does NOT call the engine directly.

However, the audit viewer needs to **fetch live data from the engine** because:
- Audit entries accumulate across publishes
- The CMS doesn't store audit trails locally
- Fresh data is required each time the panel is viewed

### Options for Fetching

**Option A: Client-side fetch from admin panel**
- Requires exposing `AdminClient` or a wrapper to the admin frontend
- Strapi 5 admin plugins can use `useFetchClient()` from `@strapi/admin/strapi-admin`
- Would need a CMS proxy route that:
  1. Receives requests from admin panel
  2. Calls `AdminClient.audit()` 
  3. Returns the response

**Option B: Embed audit data in content-type (like ValidationPanel)**
- Store a snapshot on each publish
- Loses the "live from engine" aspect
- Simpler but incomplete

**Recommended: Option A** — Add a server route that proxies the audit call, then fetch from the admin panel component.

## Files Touched

### New Files
| File | Purpose |
|------|---------|
| `src/plugins/rule-engine/admin/src/components/AuditViewer/index.tsx` | Main UI component |
| `src/plugins/rule-engine/admin/src/components/AuditViewer/types.ts` | TypeScript types for audit response |

### Modified Files
| File | Change |
|------|--------|
| `src/plugins/rule-engine/server/src/routes/index.ts` | Add audit GET route |
| `src/plugins/rule-engine/server/src/controllers/` | Add audit controller (or extend publish.ts) |
| `types/engine.ts` | Add `AuditEntry` and `AuditTrailResponse` types |

## Applicable Standards

**From engineering-standards wiki:**
- **simplicity-and-design**: Small, single-purpose components; explicit dependencies
- **error-classification**: Handle transport failures (AdminApiError) and display meaningful messages
- **rest-api patterns**: The audit endpoint follows REST conventions already

**Strapi 5 patterns observed in codebase:**
- Use `@strapi/design-system` components (Box, Flex, Typography, Button)
- Graceful fallback for missing/null data
- Debounce fetch on input changes (if filtering)
- Read-only components accept data as props, display structured output

## HLD Facts (scaled to task)

### System Boundaries
- **CMS admin panel** ↔ **CMS plugin server** ↔ **Engine admin API**
- Auth: Strapi admin auth guards the CMS route; Bearer token auth guards the engine call

### Data Flow
1. User opens audit viewer for entity (e.g., Flow "orders")
2. Admin panel calls `GET /rule-engine/audit/{type}/{id}` (CMS route)
3. CMS controller resolves Environment, builds AdminClient
4. AdminClient calls `GET /admin/audit/{type}/{id}` on engine
5. Engine returns audit entries (newest-first)
6. CMS returns entries to admin panel
7. UI renders the timeline

### Dependencies
- Depends on engine having audit entries (populated on creates/publishes)
- Depends on Environment config for adminApiBaseUrl + operatorTokenRef

### Failure Modes
- Engine unreachable → AdminApiError (recoverable, 503)
- Token invalid/expired → 403
- Unknown audit type → 400
- Object not found → 404 (or empty entries)

## LLD Facts

### Modules/Interfaces

**AuditViewer component:**
```tsx
interface AuditViewerProps {
  type: 'flow' | 'jdm' | 'connection';
  id: string;
}
```

**Audit controller (server-side):**
```ts
export default ({ strapi }) => ({
  async audit(ctx) {
    const { type, id } = ctx.params;
    // resolve AdminClient from request context or default environment
    // call client.audit(type, id)
    // return response or handle errors
  }
});
```

**Route:**
```ts
{
  method: 'GET',
  path: '/audit/:type/:id',
  handler: 'audit.audit', // or 'publish.audit' if combined
  config: { policies: [] }
}
```

### UI Structure
```
AuditViewer
├─ Loading state (spinner while fetching)
├─ Error state (if fetch fails)
├─ Empty state ("No audit history")
└─ Timeline
   ├─ Entry (action, actor, timestamp, version delta)
   ├─ Entry ...
   └─ Entry ...
```

### Design Patterns
- **Read-only panel pattern** (like ValidationPanel)
- **Fetch-on-mount** with loading/error states
- **List rendering** for entries

## Open Questions

1. **Where should the viewer be placed in the UI?**
   - As a tab/section in the Flow/JDM/Connection edit view?
   - As a standalone page accessible from a menu?
   - As a modal triggered by a button?
   
   **Tentative answer:** A collapsible section or tab within the entity's edit view, similar to how ValidationPanel integrates (if it does). Need to verify how Strapi 5 content-manager edit views allow injecting custom panels.

2. **How to resolve the Environment for the audit call?**
   - Flow entries have an `environment` relation
   - JDM/Connection entries may not
   - Fallback to env vars (ADMIN_API_BASE_URL, ADMIN_API_OPERATOR_TOKEN)?

3. **Should the viewer support filtering/pagination?**
   - Engine returns all entries newest-first; no pagination params exposed
   - For v1: display all entries, defer pagination to later if needed

4. **Error display granularity?**
   - Show raw error vs. user-friendly message?
   - Match existing CMS error patterns (ValidationPanel shows raw diffs)

## Verification Checklist

- [ ] `npm run build` passes
- [ ] `npm run test` passes
- [ ] AuditViewer renders for flow entities
- [ ] AuditViewer renders for jdm entities
- [ ] AuditViewer renders for connection entities
- [ ] Handles loading state
- [ ] Handles error state (engine unreachable)
- [ ] Handles empty state (no audit entries)
- [ ] Timestamps display in readable format
- [ ] Actor and action display correctly
- [ ] Version delta shows fromVersion → toVersion
