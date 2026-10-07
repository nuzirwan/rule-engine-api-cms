# Implementation Plan: Flow Import/Export

This plan adds Import and Export buttons to the flow editor so users can upload JSON files to visualize flows and download current flows as JSON.

## Context

- **Component**: `/cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
- **Serializer**: `./serialize.ts` — exports `treeToCanvas`, `canvasToTree`, `EngineNode` type
- **UI Framework**: `@strapi/design-system` (Button, Flex, Box), `@strapi/icons` (Upload, Download)
- **Flow format**: `EngineNode` — recursive tree with `{id, type, spec, children?}`
- **Existing patterns**:
  - `onChange` triggers via `emit(tree)` which calls `onChange({ target: { name, value: tree, type: 'json' } })`
  - `treeToCanvas(tree, layout)` converts engine tree → canvas graph
  - `toFlowNode(n)` / `toFlowEdge(e)` convert serializer types → ReactFlow types
  - Layout stored in `layoutRef.current` sidecar, separate from engine tree
  - Flow name not directly available in component — use fallback "flow-export.json"

## Implementation Steps

- [ ] 1. Add imports for Upload and Download icons from @strapi/icons, and Button from @strapi/design-system (Button already in scope via existing imports, just need icons).
      Files: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
      Verify: `npm run test` in cms directory — no import errors, existing tests pass.

- [ ] 2. Add hidden file input ref and export handler function inside FlowCanvasInner.
      - Create `fileInputRef = React.useRef<HTMLInputElement>(null)`
      - Add `handleExport` callback that:
        1. Gets current tree via `canvasToTree({ nodes: nodes.map(fromFlowNode), edges: edges.map(...) })`
        2. Formats as `JSON.stringify(tree, null, 2)`
        3. Creates blob, object URL, downloads as "flow-export.json"
        4. Revokes object URL after download
      Files: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
      Verify: `npm run test` in cms — no syntax errors.

- [ ] 3. Add import handler and validation logic.
      - Add `handleImportClick` that triggers `fileInputRef.current?.click()`
      - Add `handleFileChange` callback that:
        1. Reads selected file via FileReader
        2. Parses JSON (catches SyntaxError → shows error)
        3. Validates structure: must have `id` (string) and `type` (string) at minimum
        4. Validates root node type is 'trigger' (per canvasToTree requirement)
        5. On success: calls `treeToCanvas(importedTree, {})` to get new canvas
        6. Updates `layoutRef.current` with new positions
        7. Sets nodes/edges via `setNodes`/`setEdges`
        8. Calls `emit(importedTree)` to trigger onChange (marks form dirty)
        9. On error: shows alert or sets an error state (do NOT wipe canvas)
        10. Resets file input value so same file can be re-selected
      Files: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
      Verify: `npm run test` in cms — no syntax errors.

- [ ] 4. Add Import/Export buttons to the palette panel UI.
      - Place buttons at TOP of the palette panel (above "Palette (drag or click)" label)
      - Use a new section with label "Import / Export"
      - Two buttons in a row: Import (Upload icon) | Export (Download icon)
      - Import button triggers hidden file input
      - Export button calls handleExport
      - Both buttons disabled when `disabled` prop is true
      - Add hidden `<input type="file" accept=".json">` with ref
      - Style consistent with existing palette panel (dark mode compatible)
      Files: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
      Verify: `npm run test` in cms — tests pass; manual check that UI renders.

- [ ] 5. Add error state for import failures and display in UI.
      - Add `const [importError, setImportError] = React.useState<string | null>(null)`
      - Clear error on successful import or when starting new import
      - Display error below the Import/Export buttons if set
      - Style error text with red color to match design system error pattern
      Files: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`
      Verify: `npm run test` in cms — tests pass.

- [ ] 6. Write unit test for import validation logic.
      - Add test file or extend existing test to cover:
        - Valid tree imports successfully (nodes appear on canvas)
        - Missing `id` field rejects with error
        - Missing `type` field rejects with error
        - Non-trigger root rejects with error
        - Invalid JSON syntax rejects with error
      - Test the validation helper if extracted, or test via the handler if inline
      Files: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/import-export.test.ts` (new) or inline
      Verify: `npm run test` in cms — new tests pass.

## Decisions

1. **Button placement**: Import/Export section at TOP of palette panel, above the node palette. This keeps the buttons visible without scrolling and groups file operations separately from node manipulation.

2. **No flow name in component**: The component receives the tree value but not a "flowName" prop. Export filename will be "flow-export.json". If a name is needed later, it can be passed as a prop.

3. **Validation approach**: Minimal validation — check for required `id` and `type` fields, and that root node is type 'trigger'. This matches the existing `canvasToTree` contract which throws if no trigger root. The serializer itself handles the detailed structure.

4. **Error display**: Use inline error text below the buttons rather than modal/toast. Matches the pattern used for `specError` in the node spec editor.

5. **Icons**: Use `Upload` and `Download` from `@strapi/icons` (confirmed available in v2.2.4).

## File Changes Summary

| File | Action |
|------|--------|
| `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx` | Modify — add import/export logic and UI |
| `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/import-export.test.ts` | Create — validation tests |

## Verification Commands

```bash
cd /home/nuzirwan/project/rule-engine-api/cms && npm run test
```

All existing serialize.test.ts tests must continue to pass. New import-export tests should pass.
