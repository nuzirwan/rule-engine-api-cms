// serialize.ts (design §4.2) — the PURE flow-canvas serializer: canvas
// {nodes,edges}+layout <-> engine recursive Node tree. No React import; this is
// the UI-side half of the publish-transform contract and is round-trip tested
// (§6.2). FEAT-004 reuses this module for the FlowCanvasField component.
//
// Casing contract (finding 10): an `action` node's `operation` sub-object uses
// CAPITALIZED Go-struct keys (Kind/Payload/Required); every other spec key is
// camelCase. The serializer PRESERVES whatever casing the engine tree carries on
// the way in, and emits the engine tree UNCHANGED on the way out (spec passes
// through), so a round-trip is lossless and a naive all-camelCase pass fails the
// casing assertion. x/y layout is kept in a SIDECAR map, never on the engine tree.

// ----------------------------------------------------------------------------
// Shared types (mirrors types/engine.ts EngineNode; duplicated here so the
// admin-side serializer carries no server import — the two sides are built
// separately).
// ----------------------------------------------------------------------------

export interface EngineNode {
  id: string;
  type: string;
  spec: Record<string, unknown>;
  children?: EngineNode[];
}

/** A React Flow canvas node (layout + the node's engine type/spec). */
export interface CanvasNode {
  id: string;
  type: string;
  position: { x: number; y: number };
  data: {
    nodeType: string;
    spec: Record<string, unknown>;
  };
}

/** A React Flow edge. For control nodes, `branchKey` names the branch slot. */
export interface CanvasEdge {
  id: string;
  source: string;
  target: string;
  /** e.g. "trueKey"/"falseKey" branch label; undefined for a plain child edge. */
  branchKey?: string;
}

export interface CanvasGraph {
  nodes: CanvasNode[];
  edges: CanvasEdge[];
}

/** Sidecar layout: node id -> x/y, kept OUT of the engine tree. */
export type LayoutMap = Record<string, { x: number; y: number }>;

export interface SerializeResult {
  tree: EngineNode;
  layout: LayoutMap;
}

export interface DeserializeResult {
  graph: CanvasGraph;
  layout: LayoutMap;
}

const TRIGGER_TYPE = 'trigger';

// ----------------------------------------------------------------------------
// canvas -> engine tree
// ----------------------------------------------------------------------------

/**
 * Walk the canvas DAG from the trigger root and emit the nested engine Node tree
 * `{ id, type, spec, children }`. spec passes through verbatim (mixed casing
 * preserved). Child ordering follows edge order so branch slots stay stable.
 * x/y positions are returned in a sidecar layout map.
 */
export function canvasToTree(graph: CanvasGraph): SerializeResult {
  const nodeById = new Map(graph.nodes.map((n) => [n.id, n]));
  const layout: LayoutMap = {};
  for (const n of graph.nodes) {
    layout[n.id] = { x: n.position.x, y: n.position.y };
  }

  // children adjacency in edge declaration order (stable).
  const childrenOf = new Map<string, string[]>();
  for (const n of graph.nodes) childrenOf.set(n.id, []);
  for (const e of graph.edges) {
    const list = childrenOf.get(e.source);
    if (list) list.push(e.target);
  }

  const root = graph.nodes.find((n) => n.data.nodeType === TRIGGER_TYPE);
  if (!root) {
    throw new Error('canvasToTree: no trigger root node found');
  }

  const build = (id: string): EngineNode => {
    const canvasNode = nodeById.get(id);
    if (!canvasNode) {
      throw new Error(`canvasToTree: dangling edge references unknown node "${id}"`);
    }
    const childIds = childrenOf.get(id) ?? [];
    const node: EngineNode = {
      id: canvasNode.id,
      type: canvasNode.data.nodeType,
      // spec passes through unchanged — casing is NOT normalized.
      spec: canvasNode.data.spec,
    };
    if (childIds.length > 0) {
      node.children = childIds.map(build);
    }
    return node;
  };

  return { tree: build(root.id), layout };
}

// ----------------------------------------------------------------------------
// engine tree -> canvas
// ----------------------------------------------------------------------------

/**
 * Flatten the engine Node tree back into a canvas {nodes,edges} graph, pulling
 * x/y from the sidecar layout map (defaulting to {0,0} for a node with no stored
 * position). spec passes through verbatim so a round-trip preserves mixed casing.
 */
export function treeToCanvas(tree: EngineNode, layout: LayoutMap = {}): DeserializeResult {
  const nodes: CanvasNode[] = [];
  const edges: CanvasEdge[] = [];

  const walk = (node: EngineNode) => {
    const pos = layout[node.id] ?? { x: 0, y: 0 };
    nodes.push({
      id: node.id,
      type: node.type,
      position: { x: pos.x, y: pos.y },
      data: { nodeType: node.type, spec: node.spec },
    });
    for (const child of node.children ?? []) {
      edges.push({ id: `${node.id}->${child.id}`, source: node.id, target: child.id });
      walk(child);
    }
  };
  walk(tree);

  return { graph: { nodes, edges }, layout };
}
