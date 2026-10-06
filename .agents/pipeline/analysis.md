# Analysis: Migrate from reactflow 11 to @xyflow/react v12

## Task Summary

Migrate the CMS flow canvas from `reactflow` v11.11.4 to `@xyflow/react` v12. This is a package rename and API update — the library was rebranded from `reactflow` to `@xyflow/react` with the v12 release.

## Requirements

1. **Package update**: Replace `reactflow` → `@xyflow/react` in `cms/package.json`
2. **Import updates**: Change all imports from `reactflow` to `@xyflow/react`
3. **CSS import update**: Change `reactflow/dist/style.css` → `@xyflow/react/dist/style.css`
4. **API changes**: Handle v12 breaking changes in type signatures and component APIs
5. **Verification**: Build passes, tests pass, flow canvas still works (drag, drop, connect, save)

## Acceptance Criteria

- `npm run build` succeeds in cms/
- `npm run test` passes (serialize.test.ts, smoke.test.ts, server tests)
- Flow canvas renders, nodes draggable, edges connectable, spec editable, serialization works

---

## Affected Code

### Primary file: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/index.tsx`

**Current imports (lines 18-31):**
```typescript
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type Edge,
  type Node,
  type NodeChange,
  type EdgeChange,
} from 'reactflow';
import 'reactflow/dist/style.css';
```

**Usage of reactflow types/components:**
- `Node<T>` type — used for node state: `useState<Node[]>`
- `Edge<T>` type — used for edge state: `useState<Edge[]>`
- `NodeChange` type — in `onNodesChange` callback
- `EdgeChange` type — in `onEdgesChange` callback
- `Connection` type — in `onConnect` callback
- `ReactFlow` component — the main canvas (default import in v11, named in v12)
- `Background`, `Controls`, `MiniMap` — built-in plugins
- `applyNodeChanges`, `applyEdgeChanges`, `addEdge` — helper functions

### Secondary files (comments/documentation only, no code changes needed):

- `cms/src/plugins/rule-engine/admin/src/index.tsx` — comment references "reactflow"
- `cms/src/plugins/rule-engine/admin/src/components/JdmEditorField/index.tsx` — comment references "reactflow surface" (refers to @gorules/jdm-editor's internal usage, not our import)

### Serialize module: `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/serialize.ts`

No reactflow imports — uses its own `CanvasNode`/`CanvasEdge`/`CanvasGraph` interfaces. **No changes needed.**

### Test files:

1. `cms/src/plugins/rule-engine/admin/src/components/FlowCanvasField/serialize.test.ts` — tests the serializer, no reactflow imports. **No changes needed.**
2. `cms/src/plugins/rule-engine/admin/src/smoke.test.ts` — tests custom field registration and `parseStoredJson`. Comment references "reactflow React runtimes" but no import. **No changes needed.**

---

## v12 Breaking Changes Analysis

Source: [Official migration guide](https://reactflow.dev/learn/troubleshooting/migrate-to-v12) and [GitHub discussion #3764](https://github.com/xyflow/xyflow/discussions/3764)

### Changes that AFFECT this codebase:

1. **Package rename**: `reactflow` → `@xyflow/react`

2. **Named import for ReactFlow component**:
   - v11: `import ReactFlow from 'reactflow'`
   - v12: `import { ReactFlow } from '@xyflow/react'`

3. **CSS import path change**:
   - v11: `import 'reactflow/dist/style.css'`
   - v12: `import '@xyflow/react/dist/style.css'`

4. **Node dimensions stored in `measured`**:
   - v11: `node.width`, `node.height`
   - v12: `node.measured.width`, `node.measured.height`
   - **Impact**: Our code does NOT access `node.width`/`node.height` directly. We use `node.position` and `node.data`. **No changes needed.**

5. **`parentNode` → `parentId`**:
   - **Impact**: We don't use `parentNode`. **No changes needed.**

6. **Type generics simplified**:
   - v12 encourages defining a union type for all node types
   - **Impact**: We use `Node` without generics, then cast `n.data` as needed. Current pattern is compatible.

### Changes that DO NOT affect this codebase:

- `posX/posY` → `positionAbsoluteX/positionAbsoluteY` in NodeProps — we don't use these
- `nodeInternals` → `nodeLookup` — we don't access the store directly
- Immutable node/edge updates — we already use immutable patterns (`applyNodeChanges` returns new array)
- `getNodesBounds` API change — not used
- Handle status class names — not used
- `onDelete`/`onBeforeDelete` handlers — not used
- `useConnection` hook — not used

---

## HLD Facts (scaled to task)

**System boundaries**: This is a frontend-only change in the CMS admin panel. No backend changes.

**Data flow**: Unchanged — the FlowCanvasField converts between:
- Engine tree (recursive `{ id, type, spec, children }`) stored in Strapi JSON column
- Canvas graph (`{ nodes, edges }` + sidecar layout) for the reactflow/xyflow surface

**Dependencies**:
- Remove: `reactflow@11.11.4`
- Add: `@xyflow/react@12.x` (latest stable)
- @gorules/jdm-editor uses its own internal reactflow — no change needed there

**Non-functional**: No performance, security, or accessibility impact. This is an API surface migration, not a behavior change.

**Failure modes**: If migration breaks, the flow canvas editor won't work in the CMS. This is a development-time tool; no production runtime impact.

---

## LLD Facts

### Modules touched:

| Module | Change |
|--------|--------|
| `FlowCanvasField/index.tsx` | Update imports, change `ReactFlow` from default to named import |
| `package.json` | Replace `reactflow` with `@xyflow/react` |

### Interfaces/contracts:

The internal `CanvasNode`, `CanvasEdge`, `CanvasGraph` types in `serialize.ts` are our own abstractions — they mirror but don't import from reactflow. The actual reactflow `Node` and `Edge` types are used only in `FlowCanvasField/index.tsx` for local state and callbacks.

**Conversion functions** (no changes needed, they use our types):
- `toFlowNode(CanvasNode) → Node` — converts our node to reactflow node
- `fromFlowNode(Node) → CanvasNode` — converts reactflow node back to our node  
- `toFlowEdge(CanvasEdge) → Edge` — converts our edge to reactflow edge

### Functions affected:

| Function | Type signature | Change needed |
|----------|----------------|---------------|
| `toFlowNode` | `(CanvasNode) → Node` | None — `Node` type is still `Node` in v12 |
| `fromFlowNode` | `(Node) → CanvasNode` | None |
| `toFlowEdge` | `(CanvasEdge) → Edge` | None |
| `onNodesChange` | `(NodeChange[]) → void` | None — same signature |
| `onEdgesChange` | `(EdgeChange[]) → void` | None — same signature |
| `onConnect` | `(Connection) → void` | None — same signature |

---

## Open Questions

1. **@xyflow/react version**: Should we pin to a specific minor (e.g., `12.0.0`) or use caret (`^12.0.0`)? 
   - Recommendation: Use caret for minor updates, consistent with other deps.

2. **@gorules/jdm-editor compatibility**: The jdm-editor also uses reactflow internally. Need to verify it doesn't conflict with our @xyflow/react.
   - Check: jdm-editor bundles its own reactflow, so no conflict expected.

---

## Verification Plan

1. Create worktree: `git worktree add .worktrees/xyflow-v12 -b feat/xyflow-v12 mainline`
2. In worktree, update package.json
3. `npm install` to pull new package
4. Update imports in FlowCanvasField/index.tsx
5. `npm run build` — verify no TypeScript/build errors
6. `npm run test` — verify serialize.test.ts and smoke.test.ts pass
7. Manual verification if possible (spin up Strapi dev, test drag/drop/connect)
8. Rebase onto mainline, fast-forward merge

---

## Standards Applied

- **Simplicity**: Minimal changes — only update what's necessary for the migration
- **Testing**: Existing tests cover serialization round-trip; smoke test covers registration
- **DRY**: Serialize module uses its own types, not reactflow types directly — good separation

---

## Summary for DESIGN Stage

The migration is straightforward with minimal breaking changes affecting this codebase:

1. **package.json**: Replace `"reactflow": "11.11.4"` with `"@xyflow/react": "^12.0.0"`
2. **FlowCanvasField/index.tsx**:
   - Change import from `from 'reactflow'` to `from '@xyflow/react'`
   - Change `import ReactFlow, { ... }` to `import { ReactFlow, ... }` (named export)
   - Change CSS import from `'reactflow/dist/style.css'` to `'@xyflow/react/dist/style.css'`
3. No type signature changes needed — the v12 types are compatible with our usage
4. No test changes needed — tests don't import reactflow directly
