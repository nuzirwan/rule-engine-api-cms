// FEAT-003 — flow-canvas drop reserialize round-trip. The test env is `node`
// (vitest.config.ts does NOT add jsdom), so there is no DOM render harness to
// simulate a real dragStart/drop. Instead this pins the SAME reserialize code
// path the onDrop handler drives: a dropped palette node (a fresh node + a
// connecting edge from the trigger) is appended to the canvas graph and must
// round-trip through canvasToTree/treeToCanvas into the expected engine tree,
// landing as a child of the trigger with the dropped node type and empty spec.

import { describe, expect, it } from 'vitest';

import {
  canvasToTree,
  treeToCanvas,
  type CanvasGraph,
  type EngineNode,
} from './serialize';

// Mirrors makeFlowNode() in index.tsx (minus the random id, pinned here so the
// assertion is deterministic): a palette node dropped at a canvas position.
function droppedNode(id: string, nodeType: string, position: { x: number; y: number }) {
  return {
    id,
    type: nodeType,
    position,
    data: { nodeType, spec: {} as Record<string, unknown> },
  };
}

describe('FEAT-003 drop reserialize', () => {
  it('a dropped node connected from the trigger round-trips into the engine tree', () => {
    // Canvas state after a palette "action" was dropped at (240, 160) and the
    // author connected trigger -> action (the same {nodes,edges} shape onDrop +
    // onConnect produce, which reserialize feeds to canvasToTree).
    const graph: CanvasGraph = {
      nodes: [
        {
          id: 'trigger',
          type: 'trigger',
          position: { x: 40, y: 40 },
          data: { nodeType: 'trigger', spec: {} },
        },
        droppedNode('action-ab12cd', 'action', { x: 240, y: 160 }),
      ],
      edges: [{ id: 'trigger->action-ab12cd', source: 'trigger', target: 'action-ab12cd' }],
    };

    const { tree, layout } = canvasToTree(graph);

    const expected: EngineNode = {
      id: 'trigger',
      type: 'trigger',
      spec: {},
      children: [{ id: 'action-ab12cd', type: 'action', spec: {} }],
    };
    expect(tree).toEqual(expected);

    // The dropped position is preserved in the sidecar layout, OUT of the tree.
    expect(tree.children?.[0]).not.toHaveProperty('position');
    expect(layout['action-ab12cd']).toEqual({ x: 240, y: 160 });
  });

  it('the dropped node survives a canvas rebuild (treeToCanvas inverse)', () => {
    // After reserialize emits the tree, the value-change effect may rebuild the
    // canvas from it. Confirm the dropped node is still present and re-serializes
    // back to the same tree, so a rebuild does not wipe the drop.
    const tree: EngineNode = {
      id: 'trigger',
      type: 'trigger',
      spec: {},
      children: [{ id: 'logger-xyz789', type: 'logger', spec: {} }],
    };

    const { graph } = treeToCanvas(tree, { 'logger-xyz789': { x: 300, y: 200 } });
    const dropped = graph.nodes.find((n) => n.id === 'logger-xyz789');
    expect(dropped?.data.nodeType).toBe('logger');
    expect(dropped?.position).toEqual({ x: 300, y: 200 });

    const back = canvasToTree(graph);
    expect(back.tree).toEqual(tree);
  });
});
