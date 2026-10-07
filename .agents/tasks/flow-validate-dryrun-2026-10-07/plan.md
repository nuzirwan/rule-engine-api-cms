# Implementation Plan: Add Validate and Dry Run Buttons to Flow Editor

## Overview

Add "Validate" and "Dry Run" buttons to the flow canvas toolbar so users can test flows before publishing.

## Pre-Investigation Summary

**Engine layer (ALREADY EXISTS)**:
- `POST /admin/flows/validate` — implemented in `engine/internal/httpapi/admin_validate.go`
- `POST /admin/flows/dry-run` — implemented in `engine/internal/httpapi/admin_dryrun.go`
- Both mounted in `engine/internal/httpapi/admin.go` lines 90-91

**CMS AdminClient (ALREADY EXISTS)**:
- `validateFlow(flowId, version)` — `cms/src/plugins/rule-engine/server/src/services/admin-client.ts` line 147
- `dryRunFlow(req)` — same file, line 172

**CMS routes/controllers (NEEDS CREATION)**:
- Routes for the admin UI to call the engine via the CMS plugin
- Controller handlers to proxy requests through AdminClient

**Admin UI (NEEDS CREATION)**:
- Validate button + success/error feedback
- Dry Run button + modal for input payload + trace viewer

---

## Implementation Steps

- [ ] 1. Add CMS plugin controller for validate and dry-run endpoints.
      Create a new controller `validate.ts` that handles:
      - `POST /rule-engine/validate` — validates candidate flow tree (inline body, no version)
      - `POST /rule-engine/dry-run` — runs candidate flow with test payload, returns trace
      Both endpoints accept the flow tree inline (not from DB) so the editor can validate/test the current canvas state before any save.
      Files: `cms/src/plugins/rule-engine/server/src/controllers/validate.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/cms && npx tsc --noEmit` passes.

- [ ] 2. Register the validate controller and routes in the CMS plugin.
      Add the new controller to the plugin's controller registry and add two routes:
      - `POST /validate` → `validate.validate`
      - `POST /dry-run` → `validate.dryRun`
      Files: `cms/src/plugins/rule-engine/server/src/index.ts`, `cms/src/plugins/rule-engine/server/src/routes/index.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/cms && npx tsc --noEmit` passes.

- [ ] 3. Extend AdminClient to support candidate-mode validate and dry-run.
      The existing `validateFlow` and `dryRunFlow` methods use stored-mode (flowId+version). Add new methods:
      - `validateFlowCandidate(flow)` — sends inline flow tree for validation
      - `dryRunFlowCandidate(tree, input, mocks?)` — sends inline flow tree with test input
      Also add the corresponding TypeScript types in `cms/types/engine.ts`.
      Files: `cms/src/plugins/rule-engine/server/src/services/admin-client.ts`, `cms/types/engine.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/cms && npx tsc --noEmit` passes.

- [ ] 4. Add Validate button to FlowCanvasField with loading and feedback states.
      Add a "Validate" button to the toolbar (next to Import/Export). On click:
      - Show loading state on the button
      - Call `POST /rule-engine/validate` with the current canvas tree
      - On success (`ok:true`): show success toast/message
      - On failure (`ok:false` or errors): show validation errors inline in the palette panel
      Follow the existing pattern: `useFetchClient` from `@strapi/admin/strapi-admin`.
      Files: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/cms && npx tsc --noEmit` passes.

- [ ] 5. Add Dry Run button with modal for test payload input.
      Add a "Dry Run" button to the toolbar. On click:
      - Open a modal/panel with a JSON textarea for the test payload (method, path, params, body, headers)
      - "Run" button in modal calls `POST /rule-engine/dry-run` with current tree + input
      - Display the trace result (which nodes executed, final context, errors) in a collapsible tree or list view
      - Provide a "Close" button to dismiss results
      Files: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/cms && npx tsc --noEmit` passes.

- [ ] 6. Add unit tests for the validate controller.
      Test the controller handlers with mocked AdminClient:
      - `validate` returns validation results correctly
      - `dryRun` returns trace + response correctly
      - Error cases (missing tree, engine errors) return appropriate status codes
      Files: `cms/src/plugins/rule-engine/server/src/controllers/__tests__/validate.test.ts`
      Verify: `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/cms && npm run test -- --run` passes.

- [ ] 7. Verify full integration: build engine and CMS, run manual test.
      Compile the engine (no changes expected, but verify build is green).
      Build the CMS and verify the new buttons appear and function correctly.
      Files: (none)
      Verify: 
      - `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/engine && go build ./...` passes
      - `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/cms && npx tsc --noEmit` passes
      - `cd /home/nuzirwan/project/rule-engine-api/.worktrees/flow-validate-dryrun/cms && npm run test -- --run` passes

---

## Design Decisions

1. **Candidate-mode vs stored-mode**: The engine's `/admin/flows/validate` supports both modes — stored (flowId+version) and candidate (inline flow object). For the canvas editor, we use candidate mode so users can validate their current canvas state before saving. This matches the engine's `validateFlowRequest.Flow` field.

2. **No engine changes needed**: The engine already supports both validate and dry-run for inline flow trees. The `candidateFlowBody` structure in `admin_validate.go` accepts `{ flowId, method, path, tree, fixtures }` directly.

3. **UI placement**: Buttons go in the existing toolbar section with Import/Export, maintaining visual consistency. The "Validate" button shows inline feedback in the palette panel (where node spec editing already happens). The "Dry Run" button opens a modal because it needs both input and output display.

4. **Error display**: Validation errors are displayed as a list of issues with their node IDs highlighted if possible. Dry-run traces show a step-by-step execution path with timing and branch decisions.

---

## File Summary

| File | Action |
|------|--------|
| `cms/src/plugins/rule-engine/server/src/controllers/validate.ts` | Create |
| `cms/src/plugins/rule-engine/server/src/index.ts` | Modify |
| `cms/src/plugins/rule-engine/server/src/routes/index.ts` | Modify |
| `cms/src/plugins/rule-engine/server/src/services/admin-client.ts` | Modify |
| `cms/types/engine.ts` | Modify |
| `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx` | Modify |
| `cms/src/plugins/rule-engine/server/src/controllers/__tests__/validate.test.ts` | Create |
