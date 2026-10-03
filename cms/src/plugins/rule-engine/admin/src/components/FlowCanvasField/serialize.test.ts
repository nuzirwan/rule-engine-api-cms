// §6.2 — flow-canvas serializer round-trip. The serializer is the UI-side half
// of the publish-transform contract: canvas {nodes,edges}+layout <-> engine
// recursive Node tree. These tests pin:
//   * a lossless round-trip over the real seed trees (branch keys, nesting,
//     control-node children preserved; layout kept in a sidecar, OUT of the tree);
//   * the mixed casing contract (finding 10): operation.{Kind,Payload,Required}
//     stays CAPITALIZED while set/condition/decision/action keys stay camelCase.
//     A naive all-camelCase serializer FAILS the casing assertion below.

import { describe, expect, it } from 'vitest';

import {
  canvasToTree,
  treeToCanvas,
  type CanvasGraph,
  type EngineNode,
  type LayoutMap,
} from './serialize';

// Local copies of the seed trees (the admin side carries no server/test import).
const ordersExpediteTree: EngineNode = {
  id: 'trigger',
  type: 'trigger',
  spec: { method: 'GET', path: '/orders/{id}', input: { params: ['id'] } },
  children: [
    {
      id: 'read-order',
      type: 'action',
      spec: {
        connection: 'orders-pg',
        operation: {
          Kind: 'query',
          Payload: {
            sql: 'SELECT amount, status FROM orders WHERE id=$1',
            params: [{ value: '{{input.id}}', as: 'int' }],
          },
          Required: true,
        },
        saveAs: 'order',
      },
      children: [
        {
          id: 'classify',
          type: 'condition',
          spec: {
            jdmId: 'order',
            input: ['order.amount', 'order.status'],
            trueKey: 'expedited',
            falseKey: 'standard',
          },
          children: [
            {
              id: 'expedited',
              type: 'action',
              spec: {
                connection: 'ship-rest',
                operation: {
                  Kind: 'http',
                  Payload: { method: 'POST', path: '/expedite', body: { order: '{{order}}' } },
                  Required: true,
                },
                saveAs: 'decision',
              },
              children: [
                {
                  id: 'set-expedited',
                  type: 'set',
                  spec: { targetPath: 'shipping', from: 'decision.body.shipping' },
                  children: [
                    { id: 'respond-expedited', type: 'response', spec: { status: 200, bodyFrom: '' } },
                  ],
                },
              ],
            },
            {
              id: 'standard',
              type: 'action',
              spec: {
                connection: 'ship-rest',
                operation: {
                  Kind: 'http',
                  Payload: { method: 'POST', path: '/standard', body: { order: '{{order}}' } },
                  Required: true,
                },
                saveAs: 'decision',
              },
              children: [
                {
                  id: 'set-standard',
                  type: 'set',
                  spec: { targetPath: 'shipping', from: 'decision.body.shipping' },
                  children: [
                    { id: 'respond-standard', type: 'response', spec: { status: 200, bodyFrom: '' } },
                  ],
                },
              ],
            },
          ],
        },
      ],
    },
  ],
};

const fmcOrderByIdTree: EngineNode = {
  id: 'trigger',
  type: 'trigger',
  spec: { method: 'GET', path: '/order/{order_id}', input: { params: ['order_id'] } },
  children: [
    {
      id: 'read-order',
      type: 'action',
      spec: {
        connection: 'fmc-pg',
        operation: {
          Kind: 'query',
          Payload: {
            sql: 'SELECT order_id FROM fmc_order.order_status WHERE order_id = $1',
            params: ['{{input.order_id}}'],
          },
          Required: true,
        },
        saveAs: 'row',
      },
      children: [
        {
          id: 'echo-order_id',
          type: 'set',
          spec: { targetPath: 'order_id', from: 'row.order_id' },
          children: [
            {
              id: 'decide',
              type: 'decision',
              spec: { jdmId: 'fmc-payment', input: ['row.payment_status'], saveAs: 'dec' },
            },
          ],
        },
      ],
    },
  ],
};

/** Build a canvas graph from a tree WITHOUT reusing the serializer under test. */
function treeToCanvasManual(tree: EngineNode, layout: LayoutMap = {}): CanvasGraph {
  const nodes: CanvasGraph['nodes'] = [];
  const edges: CanvasGraph['edges'] = [];
  const walk = (node: EngineNode) => {
    const pos = layout[node.id] ?? { x: 0, y: 0 };
    nodes.push({
      id: node.id,
      type: node.type,
      position: pos,
      data: { nodeType: node.type, spec: node.spec },
    });
    for (const child of node.children ?? []) {
      edges.push({ id: `${node.id}->${child.id}`, source: node.id, target: child.id });
      walk(child);
    }
  };
  walk(tree);
  return { nodes, edges };
}

describe('§6.2 canvas <-> engine tree round-trip', () => {
  it('round-trips orders-expedite preserving branch children, nesting and casing', () => {
    const graph = treeToCanvasManual(ordersExpediteTree);
    const { tree, layout } = canvasToTree(graph);
    expect(tree).toEqual(ordersExpediteTree);
    // layout is a sidecar — never on the engine tree.
    expect(tree).not.toHaveProperty('position');
    expect(Object.keys(layout).sort()).toContain('read-order');
  });

  it('round-trips fmc-order-by-id preserving the deep set chain + decision', () => {
    const graph = treeToCanvasManual(fmcOrderByIdTree);
    const { tree } = canvasToTree(graph);
    expect(tree).toEqual(fmcOrderByIdTree);
  });

  it('tree -> canvas -> tree is lossless (treeToCanvas inverse)', () => {
    const { graph, layout } = treeToCanvas(ordersExpediteTree, {
      'read-order': { x: 10, y: 20 },
    });
    const back = canvasToTree(graph);
    expect(back.tree).toEqual(ordersExpediteTree);
    // the sidecar layout survives the trip.
    expect(layout['read-order']).toEqual({ x: 10, y: 20 });
    expect(back.layout['read-order']).toEqual({ x: 10, y: 20 });
  });

  it('keeps x/y in the sidecar layout map, OUT of the engine tree', () => {
    const graph = treeToCanvasManual(ordersExpediteTree, {
      trigger: { x: 5, y: 6 },
      'read-order': { x: 7, y: 8 },
    });
    const { tree, layout } = canvasToTree(graph);
    const walk = (n: EngineNode) => {
      expect(n).not.toHaveProperty('position');
      expect(n.spec).not.toHaveProperty('x');
      expect(n.spec).not.toHaveProperty('y');
      (n.children ?? []).forEach(walk);
    };
    walk(tree);
    expect(layout.trigger).toEqual({ x: 5, y: 6 });
    expect(layout['read-order']).toEqual({ x: 7, y: 8 });
  });

  it('emits operation.{Kind,Payload,Required} CAPITALIZED (a naive all-camelCase serializer fails)', () => {
    const graph = treeToCanvasManual(ordersExpediteTree);
    const { tree } = canvasToTree(graph);
    const action = (tree.children ?? [])[0];
    const operation = action.spec.operation as Record<string, unknown>;
    expect(operation).toHaveProperty('Kind');
    expect(operation).toHaveProperty('Payload');
    expect(operation).toHaveProperty('Required');
    // the naive-bug keys must NOT appear.
    expect(operation).not.toHaveProperty('kind');
    expect(operation).not.toHaveProperty('payload');
    expect(operation).not.toHaveProperty('required');
  });

  it('keeps control/leaf spec keys camelCase (set/condition/decision/action)', () => {
    const graph = treeToCanvasManual(ordersExpediteTree);
    const { tree } = canvasToTree(graph);
    const action = (tree.children ?? [])[0];
    // action: connection/saveAs camelCase.
    expect(action.spec).toHaveProperty('connection');
    expect(action.spec).toHaveProperty('saveAs');
    // condition: trueKey/falseKey camelCase.
    const condition = (action.children ?? [])[0];
    expect(condition.spec).toHaveProperty('trueKey');
    expect(condition.spec).toHaveProperty('falseKey');
    // set: targetPath/from camelCase.
    const setNode = ((condition.children ?? [])[0].children ?? [])[0];
    expect(setNode.type).toBe('set');
    expect(setNode.spec).toHaveProperty('targetPath');
    expect(setNode.spec).toHaveProperty('from');
  });

  it('decision node keeps jdmId/input/saveAs camelCase', () => {
    const graph = treeToCanvasManual(fmcOrderByIdTree);
    const { tree } = canvasToTree(graph);
    const decision = ((tree.children ?? [])[0].children ?? [])[0].children?.[0];
    expect(decision?.type).toBe('decision');
    expect(decision?.spec).toHaveProperty('jdmId');
    expect(decision?.spec).toHaveProperty('input');
    expect(decision?.spec).toHaveProperty('saveAs');
  });

  it('throws when no trigger root is present', () => {
    const graph: CanvasGraph = {
      nodes: [{ id: 'x', type: 'set', position: { x: 0, y: 0 }, data: { nodeType: 'set', spec: {} } }],
      edges: [],
    };
    expect(() => canvasToTree(graph)).toThrow(/trigger root/);
  });
});
