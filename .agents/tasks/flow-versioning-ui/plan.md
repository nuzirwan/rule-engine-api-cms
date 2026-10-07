# Implementation Plan: Flow Versioning UI

## Overview
Add version history UI to the rule-engine CMS plugin (Strapi 5) so operators can view flow versions, compare changes, preview a version, and rollback. This is CMS-only — no engine changes. AdminClient already has `listFlowVersions()` and `rollbackFlow()` methods from the sync feature.

## Context
- Worktree: `/home/nuzirwan/project/rule-engine-api/.worktrees/flow-versioning-ui`
- CMS root: `/home/nuzirwan/project/rule-engine-api/.worktrees/flow-versioning-ui/cms`
- Plugin: `cms/src/plugins/rule-engine/`
- Build: `cd cms && npm run build`
- Test: `cd cms && npm run test`

## FEAT-001: Server-side Version History Endpoints

- [ ] 1. Create the flow controller at `cms/src/plugins/rule-engine/server/src/controllers/flow.ts`
      Factory pattern with strapi + fetchImpl injection (same as sync.ts). Implement:
      - `getVersions(ctx)`: calls `client.listFlowVersions(ctx.params.id)`, returns version list
      - `rollback(ctx)`: calls `client.rollbackFlow(ctx.params.id, ctx.request.body.version)`, returns new active version
      Include `createClient()` and `handleApiError()` helpers per sync.ts pattern.
      Files: `cms/src/plugins/rule-engine/server/src/controllers/flow.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-versioning-ui/cms && npm run test` — no import errors.

- [ ] 2. Register flow controller in server index
      Import flowController and add to controllers object.
      Files: `cms/src/plugins/rule-engine/server/src/index.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-versioning-ui/cms && npm run build` — builds without error.

- [ ] 3. Add routes for version history and rollback
      Add to admin routes: `GET /flows/:id/versions` → `flow.getVersions`, `POST /flows/:id/rollback` → `flow.rollback`.
      Files: `cms/src/plugins/rule-engine/server/src/routes/index.ts`
      Verify: Build succeeds.

- [ ] 4. Create unit tests for flow controller
      Follow sync.test.ts pattern with nock + httpFetch shim. Test: versions list returned, rollback calls engine, 404/5xx error handling.
      Files: `cms/src/plugins/rule-engine/server/tests/flow.test.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-versioning-ui/cms && npm run test` — all tests pass.

## FEAT-002: Admin UI Components

- [ ] 5. Create VersionHistory types
      Define VersionSummary, FlowVersionsResponse, RollbackResponse interfaces. Keep local to admin bundle (don't import from root types/).
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/types.ts`
      Verify: Build succeeds.

- [ ] 6. Create VersionItem component
      Single version row: version number, createdAt (relative time), createdBy, validation badge, active badge. Buttons: View, Compare checkbox, Rollback. Follow EntryCard pattern from AuditViewer.
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/VersionItem.tsx`
      Verify: Build succeeds.

- [ ] 7. Create VersionHistory panel component
      Follow SyncPanel pattern: FetchState union, useFetchClient for `GET /rule-engine/flows/:flowId/versions`. Collapsible Box, version count summary, Table with VersionItem rows. Pagination (10 per page, Load more). Track selectedVersions[] for comparison (max 2).
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/index.tsx`
      Verify: Build succeeds.

- [ ] 8. Create JSON diff utility
      Pure function `jsonDiff(a, b)` returning `{path, type:'added'|'removed'|'changed', oldValue?, newValue}[]`. Recursive tree comparison.
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionDiff/diff.ts`
      Verify: Build succeeds.

- [ ] 9. Create diff unit tests
      Test jsonDiff with: identical objects, added fields, removed fields, changed values, nested changes, array changes.
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionDiff/diff.test.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-versioning-ui/cms && npm run test` — diff tests pass.

- [ ] 10. Create VersionDiff modal component
      Accept two version trees. Use jsonDiff utility. Side-by-side view with syntax highlighting (green=added, red=removed, yellow=changed). Include fixtures diff section.
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionDiff/index.tsx`
      Verify: Build succeeds.

- [ ] 11. Create VersionPreview modal component
      Accept flowId and version. Render FlowCanvasField in read-only mode (disabled=true) with the version's flow tree. Show validation status.
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionPreview/index.tsx`
      Verify: Build succeeds.

- [ ] 12. Add rollback functionality to VersionHistory
      Rollback button triggers confirmation Modal. On confirm, POST to `/rule-engine/flows/:id/rollback`. Success/error toast. Refresh version list.
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/index.tsx` (update)
      Verify: Build succeeds.

- [ ] 13. Create VersionHistory unit tests
      Follow EnvironmentManager.test.ts pattern: test types shape, test comparison selection logic (max 2), test endpoint targeting.
      Files: `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/VersionHistory.test.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-versioning-ui/cms && npm run test` — all tests pass.

- [ ] 14. Create FlowDetailPage and register route
      Page accepting flowId param. Shows flow info header and embeds VersionHistory panel. Register in admin/src/index.tsx at `/flows/:flowId`.
      Files: `cms/src/plugins/rule-engine/admin/src/pages/FlowDetailPage.tsx`, `cms/src/plugins/rule-engine/admin/src/index.tsx`
      Verify: Build succeeds.

## Final Verification

- [ ] 15. Full build and test suite
      Run complete verification to ensure all components integrate correctly.
      Files: All created/modified files
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-versioning-ui/cms && npm run build && npm run test` — all pass.

## File Summary

### New Files
- `cms/src/plugins/rule-engine/server/src/controllers/flow.ts`
- `cms/src/plugins/rule-engine/server/tests/flow.test.ts`
- `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/types.ts`
- `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/VersionItem.tsx`
- `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/index.tsx`
- `cms/src/plugins/rule-engine/admin/src/components/VersionHistory/VersionHistory.test.ts`
- `cms/src/plugins/rule-engine/admin/src/components/VersionDiff/diff.ts`
- `cms/src/plugins/rule-engine/admin/src/components/VersionDiff/diff.test.ts`
- `cms/src/plugins/rule-engine/admin/src/components/VersionDiff/index.tsx`
- `cms/src/plugins/rule-engine/admin/src/components/VersionPreview/index.tsx`
- `cms/src/plugins/rule-engine/admin/src/pages/FlowDetailPage.tsx`

### Modified Files
- `cms/src/plugins/rule-engine/server/src/index.ts` — add flow controller
- `cms/src/plugins/rule-engine/server/src/routes/index.ts` — add version/rollback routes
- `cms/src/plugins/rule-engine/admin/src/index.tsx` — add FlowDetailPage route
