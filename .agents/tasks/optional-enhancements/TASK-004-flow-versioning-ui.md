# TASK-004: Flow Versioning UI

## Summary
Add version history UI to the CMS so operators can view flow versions, compare changes, and rollback from the CMS interface.

## Context
- Engine already stores flow versions with audit trail
- Admin API already has `GET /admin/flows/{id}/versions` (added in sync feature)
- Admin API already has `POST /admin/flows/{id}/rollback`
- CMS can list versions via sync API
- Missing: UI to view version history, compare versions, trigger rollback

## Requirements

### 1. Version History Panel
- Show in flow detail/edit view as collapsible panel
- List all versions: version number, created_at, actor, status (active/validated/draft)
- Highlight currently active version
- Paginate if many versions

### 2. Version Comparison
- Select two versions to compare
- Show diff of flow tree (JSON diff with visual highlighting)
- Show diff of fixtures
- Side-by-side or unified view toggle

### 3. Version Detail View
- Click version to see full flow tree at that version
- Read-only preview of the flow canvas
- Show validation status and errors (if any)

### 4. Rollback Action
- "Rollback to this version" button on non-active versions
- Confirmation modal: "Rollback flow [name] to version [n]?"
- Calls `POST /admin/flows/{id}/rollback` with version
- Refresh version list after rollback
- Show success/error toast

### 5. Version Notes (optional enhancement)
- Allow adding notes when publishing (commit message style)
- Display notes in version history
- Requires engine API change to accept `note` on publish

## Files to Modify/Create

### CMS Admin (`cms/src/plugins/rule-engine/admin/`)
- `src/components/VersionHistory/index.tsx` — version list component
- `src/components/VersionHistory/VersionItem.tsx` — single version row
- `src/components/VersionDiff/index.tsx` — diff viewer component
- `src/components/VersionPreview/index.tsx` — read-only flow preview
- `src/pages/FlowDetailPage.tsx` — integrate version history panel

### CMS Server (`cms/src/plugins/rule-engine/server/`)
- `src/services/admin-client.ts` — add `rollbackFlow(id, version)` method
- `src/controllers/flow.ts` — add rollback endpoint

## Acceptance Criteria
- [ ] Version history panel shows all versions for a flow
- [ ] Active version is highlighted
- [ ] Can compare two versions with diff view
- [ ] Can preview flow tree at any version
- [ ] Rollback button triggers rollback and refreshes list
- [ ] Error handling for rollback failures

## Testing
- Unit tests for VersionHistory component
- Unit tests for diff generation
- Manual test: create versions, compare, rollback, verify active version changed
