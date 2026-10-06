# Design: Audit-Viewer UI for Strapi CMS

## Overview

Add an audit viewer UI component to the CMS admin panel that displays version history (audit trail) for Flows, JDMs, and Connections. The viewer fetches live data from the engine via a CMS proxy route and displays it using Strapi 5 design-system components.

## Standards Applied

| Standard | Application |
|----------|-------------|
| **simplicity-and-design** | Small, single-purpose components; explicit dependencies via props; no speculative generality (v1 shows all entries, no pagination) |
| **error-classification** | Classify errors: 5xx/transport → recoverable (503); 4xx → author-fixable; display user-friendly messages matching existing ValidationPanel pattern |
| **low-level-design** | Single-responsibility components; clear interfaces (AuditViewerProps); modules with high cohesion (types.ts, component, controller) |
| **ports & adapters** | The CMS server route proxies to AdminClient; the admin UI fetches from the CMS route, never the engine directly |

## Architecture

### Data Flow

```
┌─────────────────────┐    GET /rule-engine/audit/:type/:id    ┌──────────────────┐
│  Admin Panel        │ ────────────────────────────────────▶  │  CMS Plugin      │
│  (AuditViewer)      │ ◀────────────────────────────────────  │  (audit route)   │
└─────────────────────┘         { entries: [...] }             └──────────────────┘
                                                                        │
                                                                        │ AdminClient.audit()
                                                                        ▼
                                                               ┌──────────────────┐
                                                               │  Engine          │
                                                               │  GET /admin/audit│
                                                               └──────────────────┘
```

### System Boundaries

1. **CMS Admin Panel** → **CMS Plugin Server Route** (Strapi admin auth)
2. **CMS Plugin Server** → **Engine Admin API** (Bearer token auth via AdminClient)

## Files to Create

### 1. `src/plugins/rule-engine/admin/src/components/AuditViewer/types.ts`

Type definitions for the audit response:

```typescript
export interface AuditEntry {
  action: string;
  fromVersion: number | null;
  toVersion: number | null;
  actor: string;
  at: string; // ISO timestamp
  reason: string;
}

export interface AuditTrailResponse {
  objectType: 'flow' | 'jdm' | 'connection';
  objectId: string;
  entries: AuditEntry[];
}

export type AuditObjectType = 'flow' | 'jdm' | 'connection';
```

### 2. `src/plugins/rule-engine/admin/src/components/AuditViewer/index.tsx`

The main UI component:

```typescript
interface AuditViewerProps {
  type: AuditObjectType;
  id: string;
}
```

Component structure:
- Fetch-on-mount using Strapi's `useFetchClient()` hook
- Loading state: skeleton/spinner
- Error state: user-friendly message (recoverable vs permanent)
- Empty state: "No audit history"
- Timeline rendering: newest-first list of entries

UI elements (per ValidationPanel pattern):
- `Box` container with `background="neutral100"` and `hasRadius`
- `Flex` layout with `direction="column"` and `gap`
- `Typography` for headings (`variant="delta"`), labels (`variant="sigma"`), and content (`variant="pi"`)
- Entry cards with timestamp, action badge, actor, version delta

### 3. `src/plugins/rule-engine/server/src/controllers/audit.ts`

Thin controller that proxies the audit call:

```typescript
export default ({ strapi }: { strapi: any }) => ({
  async audit(ctx: any) {
    const { type, id } = ctx.params;
    
    // Validate type
    if (!['flow', 'jdm', 'connection'].includes(type)) {
      ctx.badRequest('invalid audit type');
      return;
    }
    
    // Resolve Environment for config (use global env vars as fallback)
    const config = resolveAdminConfig();
    const client = new AdminClient(config);
    
    try {
      const result = await client.audit(type, id);
      ctx.body = result;
    } catch (err) {
      // Map AdminApiError to HTTP status per error-classification
      if (err instanceof AdminApiError) {
        ctx.status = err.recoverable ? 503 : (err.status || 500);
        ctx.body = { error: err.message, recoverable: err.recoverable };
        return;
      }
      throw err;
    }
  },
});
```

## Files to Modify

### 1. `src/plugins/rule-engine/server/src/routes/index.ts`

Add the audit GET route:

```typescript
export default {
  admin: {
    type: 'admin',
    routes: [
      // ... existing publish route ...
      {
        method: 'GET',
        path: '/audit/:type/:id',
        handler: 'audit.audit',
        config: {
          policies: [],
        },
      },
    ],
  },
};
```

### 2. `src/plugins/rule-engine/server/src/index.ts`

Register the audit controller:

```typescript
import auditController from './controllers/audit';
// ...
controllers: {
  publish: publishController,
  audit: auditController,
},
```

### 3. `types/engine.ts`

Add typed audit response interfaces:

```typescript
export interface AuditEntry {
  action: string;
  fromVersion: number | null;
  toVersion: number | null;
  actor: string;
  at: string;
  reason: string;
}

export interface AuditTrailResponse {
  objectType: 'flow' | 'jdm' | 'connection';
  objectId: string;
  entries: AuditEntry[];
}
```

### 4. `src/plugins/rule-engine/server/src/services/admin-client.ts`

Update `audit()` method return type:

```typescript
import type { AuditTrailResponse } from '../../../../../../types/engine';

audit(type: 'flow' | 'jdm' | 'connection', id: string): Promise<AuditTrailResponse> {
  return this.request<AuditTrailResponse>(
    'GET',
    `/admin/audit/${encodeURIComponent(type)}/${encodeURIComponent(id)}`
  );
}
```

## Design Decisions

### 1. Proxy Route vs Direct Engine Call

**Decision:** Use a CMS proxy route (Option A from analysis).

**Rationale (per ports & adapters):** The admin panel should not know about engine auth tokens or base URLs. The CMS server already has `AdminClient` and `resolveAdminConfig()` that handle token resolution from env vars. The proxy keeps the admin-panel fetching from a single origin (Strapi) with Strapi's admin auth.

### 2. Environment Resolution for Audit Calls

**Decision:** Use global env vars (ADMIN_API_BASE_URL, ADMIN_API_OPERATOR_TOKEN) as fallback.

**Rationale:** Audit is a read-only query that doesn't require a specific Environment context. The global env vars provide a sensible default. If a per-Environment override is needed later, the route can accept an optional `environment` query param.

**Gap flagged:** The wiki does not cover "which auth context to use for cross-entity queries". This uses well-established practice (global default for read-only admin queries).

### 3. No Pagination (v1)

**Decision:** Display all entries without pagination.

**Rationale (per simplicity-and-design / YAGNI):** The engine returns all entries newest-first. Audit trails are append-only and typically small (one entry per publish/rollback). Pagination adds complexity without proven need. The seam exists (the API can add pagination params later) but the UI doesn't implement it yet.

### 4. Read-Only Surface

**Decision:** The AuditViewer is a read-only display component (like ValidationPanel).

**Rationale:** Audit trails are append-only by design. There's no user action to take on an audit entry. The component fetches data and displays it; no mutations.

### 5. Timestamp Formatting

**Decision:** Display timestamps in locale-friendly relative format (e.g., "2 hours ago") with full ISO on hover.

**Rationale:** Relative times are easier to scan; the tooltip provides precision when needed. This matches common audit UI patterns.

## Test Plan

### Unit Tests

1. **`AuditViewer.test.tsx`** — Component tests:
   - Renders loading state while fetching
   - Renders error state on fetch failure (recoverable vs permanent message)
   - Renders empty state when entries array is empty
   - Renders entries correctly (action, actor, timestamp, version delta)
   - Handles null fromVersion/toVersion gracefully
   - Formats timestamps correctly

2. **`audit.controller.test.ts`** — Controller tests:
   - Returns 400 for invalid audit type
   - Returns audit trail on success
   - Returns 503 for recoverable AdminApiError
   - Returns 4xx status for non-recoverable AdminApiError

### Regression Set

Existing test suites that must continue to pass:
- `src/plugins/rule-engine/server/tests/publish-sequence.test.ts`
- `src/plugins/rule-engine/server/tests/publish-controller.test.ts`
- `src/plugins/rule-engine/admin/src/smoke.test.ts`
- All vitest tests via `npm run test`

### Manual Verification

- `npm run build` passes
- `npm run test` passes
- AuditViewer renders for flow entities
- AuditViewer renders for jdm entities
- AuditViewer renders for connection entities

## UI Structure

```
AuditViewer
├─ Header: "Audit Trail" (Typography variant="delta")
├─ Loading: Loader component
├─ Error: Box with error message and retry hint
├─ Empty: "No audit history for this {type}."
└─ Timeline
   ├─ Entry
   │  ├─ Timestamp (relative, full on hover)
   │  ├─ Action badge (colored by action type)
   │  ├─ Actor label
   │  ├─ Version delta: "v{from} → v{to}" or "→ v{to}" if from is null
   │  └─ Reason (if non-empty)
   ├─ Entry ...
   └─ Entry ...
```

## Open Questions (Deferred)

1. **Where to place the viewer in the UI?** — This design provides the component; integration into Flow/JDM/Connection edit views is a separate task (may require Strapi content-manager extension patterns).

2. **Filtering/searching audit entries?** — Deferred per YAGNI. The component can be extended with a search input if needed.

## Standards Gaps Identified

| Topic | Notes |
|-------|-------|
| Cross-entity auth context | No wiki page covers which auth context to use for read-only queries spanning entities. Used well-established practice: global env var fallback. |
| Admin UI component patterns | No `frontend-component` or `react-patterns` page in the wiki. Followed Strapi 5 design-system docs and existing ValidationPanel pattern. |
