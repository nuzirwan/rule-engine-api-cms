// FlowCanvasField (design §4.2) — the Strapi custom-field Input that authors the
// engine flow tree on an @xyflow/react v12 canvas. Registered with base type:'json'
// (finding 13) so Flow.tree persists the serialized engine Node tree into its
// JSON column with no double-encode.
//
// It REUSES the FEAT-003 pure serializer (./serialize) to convert between the
// canvas {nodes,edges}+layout and the engine recursive Node tree — it does NOT
// re-implement it. x/y positions are kept in a sidecar layout map so the engine
// tree stays pure (the engine ignores layout). A node palette offers the engine
// Node taxonomy, and a side-panel edits the selected node's `spec` (as JSON, so
// the mixed-casing contract — operation.{Kind,Payload,Required} capitalized, the
// rest camelCase — is preserved verbatim through the serializer).
//
// On a parse failure of the stored value, it degrades to the shared raw-JSON
// fallback rather than a blank/broken canvas (design §4).
//
// PERF: Loading state shown while ReactFlow initializes; drag/connect disabled
// until first user interaction to reduce initial render cost on large flows.

import * as React from 'react';
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  Controls,
  MiniMap,
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  useReactFlow,
  Handle,
  Position,
  type Connection,
  type Edge,
  type Node,
  type NodeChange,
  type EdgeChange,
  type NodeProps,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { Box, Field, Flex, Textarea } from '@strapi/design-system';

import { parseStoredJson, safeStringify } from '../../lib/parseStored';
import { RawJsonFallback } from '../RawJsonFallback';
import { EditorSkeleton } from '../EditorSkeleton';
import {
  canvasToTree,
  treeToCanvas,
  type CanvasEdge,
  type CanvasGraph,
  type CanvasNode,
  type EngineNode,
  type LayoutMap,
} from './serialize';

/**
 * Custom CSS to make ReactFlow dark-mode compatible. Injected once.
 * Targets the canvas background, controls, and minimap.
 */
const DARK_MODE_STYLES = `
  .react-flow {
    background: #212134 !important;
  }
  .react-flow__background {
    background: #212134 !important;
  }
  .react-flow__background pattern circle {
    fill: #4a4a6a !important;
  }
  .react-flow__controls {
    background: #32324d !important;
    border: 1px solid #4a4a6a !important;
    border-radius: 4px;
  }
  .react-flow__controls-button {
    background: #32324d !important;
    border-bottom: 1px solid #4a4a6a !important;
    fill: #ffffff !important;
  }
  .react-flow__controls-button:hover {
    background: #4945ff !important;
  }
  .react-flow__controls-button svg {
    fill: #ffffff !important;
  }
  .react-flow__minimap {
    background: #32324d !important;
    border: 1px solid #4a4a6a !important;
    border-radius: 4px;
  }
  .react-flow__minimap-mask {
    fill: #4945ff33 !important;
  }
  .react-flow__minimap-node {
    fill: #4945ff !important;
    stroke: none !important;
  }
  .react-flow__edge-path {
    stroke: #8e8ea9 !important;
  }
  .react-flow__edge.selected .react-flow__edge-path {
    stroke: #4945ff !important;
  }
  .react-flow__attribution {
    display: none !important;
  }
`;

// Inject dark mode styles once
if (typeof document !== 'undefined') {
  const styleId = 'flow-canvas-dark-mode';
  if (!document.getElementById(styleId)) {
    const style = document.createElement('style');
    style.id = styleId;
    style.textContent = DARK_MODE_STYLES;
    document.head.appendChild(style);
  }
}
/** The engine Node taxonomy the palette offers (design §4.2 / HLD §3). */
export const NODE_PALETTE = [
  'trigger',
  'messageTrigger',
  'action',
  'condition',
  'switch',
  'sequence',
  'parallel',
  'forEach',
  'decision',
  'set',
  'logger',
  'response',
  'filter',
  'find',
  'map',
  'reduce',
  'load',
] as const;

const EMPTY_TREE: EngineNode = { id: 'trigger', type: 'trigger', spec: {} };
const DEBOUNCE_MS = 300;

/** dataTransfer MIME type carrying the palette node type across a drag. */
const DND_MIME = 'application/x-rule-engine-node';

/** Height for the canvas — 75vh (3/4 of viewport height). */
const CANVAS_HEIGHT = 'calc(75vh - 120px)';

/** Width for the entire flow editor — 75vw (3/4 of viewport width). */
const EDITOR_WIDTH = 'calc(75vw)';

/**
 * Custom node component that displays the node type as a title/label.
 * ReactFlow's default node doesn't show labels, so we need a custom one.
 * Uses CSS variables for dark mode compatibility.
 */
function LabeledNode({ data, selected }: NodeProps) {
  const nodeData = data as { nodeType?: string; label?: string };
  const label = nodeData.label || nodeData.nodeType || 'node';
  return (
    <div
      style={{
        padding: '10px 16px',
        borderRadius: 6,
        border: selected ? '2px solid #4945ff' : '1px solid #666',
        background: selected ? '#4945ff22' : '#32324d',
        color: '#ffffff',
        fontSize: 12,
        fontWeight: 500,
        minWidth: 100,
        textAlign: 'center',
      }}
    >
      <Handle type="target" position={Position.Top} style={{ background: '#4945ff' }} />
      <div>{label}</div>
      <Handle type="source" position={Position.Bottom} style={{ background: '#4945ff' }} />
    </div>
  );
}

/** Map of custom node types for ReactFlow. */
const nodeTypes = { default: LabeledNode, labeled: LabeledNode };

/**
 * Draggable palette item — uses a plain <div> with native HTML5 drag instead of
 * Strapi's Button (which may interfere with drag events). Dark-mode compatible.
 */
function PaletteItem({
  nodeType,
  disabled,
  onDragStart,
  onClick,
}: {
  nodeType: string;
  disabled?: boolean;
  onDragStart: (e: React.DragEvent<HTMLDivElement>) => void;
  onClick: () => void;
}) {
  return (
    <div
      draggable={!disabled}
      onDragStart={onDragStart}
      onClick={disabled ? undefined : onClick}
      style={{
        padding: '6px 12px',
        borderRadius: 4,
        border: '1px solid #4945ff',
        background: disabled ? '#32324d' : '#212134',
        color: '#ffffff',
        fontSize: 12,
        cursor: disabled ? 'not-allowed' : 'grab',
        userSelect: 'none',
        opacity: disabled ? 0.5 : 1,
      }}
    >
      {nodeType}
    </div>
  );
}

/** Build a fresh @xyflow/react Node for a palette type at the given position. */
function makeFlowNode(nodeType: string, position: { x: number; y: number }): Node {
  const id = `${nodeType}-${Math.random().toString(36).slice(2, 8)}`;
  return {
    id,
    position,
    data: { nodeType, spec: {}, label: `${nodeType} · ${id}` },
    type: 'default',
  };
}

interface InputProps {
  name: string;
  value?: unknown;
  onChange: (e: { target: { name: string; value: unknown; type: string } }) => void;
  intlLabel?: { defaultMessage?: string };
  hint?: string;
  required?: boolean;
  error?: string;
  disabled?: boolean;
  attribute?: { type?: string };
}

/** Convert the serializer's CanvasNode into an @xyflow/react Node (spec rides in data). */
function toFlowNode(n: CanvasNode): Node {
  return {
    id: n.id,
    position: n.position,
    data: { nodeType: n.data.nodeType, spec: n.data.spec, label: `${n.data.nodeType} · ${n.id}` },
    type: 'default',
  };
}

/** Convert an @xyflow/react Node back into the serializer's CanvasNode shape. */
function fromFlowNode(n: Node): CanvasNode {
  const data = (n.data ?? {}) as { nodeType?: string; spec?: Record<string, unknown> };
  return {
    id: n.id,
    type: data.nodeType ?? 'action',
    position: { x: n.position.x, y: n.position.y },
    data: { nodeType: data.nodeType ?? 'action', spec: data.spec ?? {} },
  };
}

function toFlowEdge(e: CanvasEdge): Edge {
  return { id: e.id, source: e.source, target: e.target, label: e.branchKey };
}

/**
 * Inner canvas component. Rendered INSIDE <ReactFlowProvider> (see the wrapper
 * below) so useReactFlow()/screenToFlowPosition resolve against the active flow
 * instance — a drop maps screen coords to canvas coords through that hook.
 */
const FlowCanvasInner = React.forwardRef<HTMLDivElement, InputProps>((props, ref) => {
  const { name, value, onChange, intlLabel, hint, required, error, disabled } = props;

  const parsed = React.useMemo(() => parseStoredJson<EngineNode>(value, EMPTY_TREE), [value]);

  // PERF: Loading state while ReactFlow initializes
  const [isReady, setIsReady] = React.useState(false);
  React.useEffect(() => {
    // Defer ready state to next frame to allow React to finish mounting
    const frame = requestAnimationFrame(() => setIsReady(true));
    return () => cancelAnimationFrame(frame);
  }, []);

  // The flow instance (from the surrounding ReactFlowProvider) — used by the
  // canvas drop handler to map screen coords to canvas coords.
  const { screenToFlowPosition } = useReactFlow();

  // Sidecar x/y layout, kept OUT of the engine tree. Positions a drag produces
  // live here so re-deriving the canvas from the (layout-free) engine tree does
  // NOT snap nodes back to their default auto-layout.
  const layoutRef = React.useRef<LayoutMap>({});

  // The last engine tree THIS component emitted. When the incoming `value`
  // matches it, the change is our own round-trip echo — we must NOT rebuild the
  // canvas from it (that would wipe an in-progress drag). We only rebuild when
  // the value changes from OUTSIDE (initial load / external edit).
  const lastEmittedRef = React.useRef<string | null>(null);

  // Build the canvas from the engine tree via the FEAT-003 serializer, applying
  // the sidecar layout so stored/dragged positions win over auto-layout.
  const buildCanvas = React.useCallback((tree: EngineNode | undefined): CanvasGraph => {
    if (!tree) return { nodes: [], edges: [] };
    try {
      return treeToCanvas(tree, layoutRef.current).graph;
    } catch {
      return { nodes: [], edges: [] };
    }
  }, []);

  const [nodes, setNodes] = React.useState<Node[]>(() =>
    buildCanvas(parsed.ok ? parsed.value : undefined).nodes.map(toFlowNode)
  );
  const [edges, setEdges] = React.useState<Edge[]>(() =>
    buildCanvas(parsed.ok ? parsed.value : undefined).edges.map(toFlowEdge)
  );
  const [selectedId, setSelectedId] = React.useState<string | null>(null);
  const [specDraft, setSpecDraft] = React.useState<string>('');
  const [specError, setSpecError] = React.useState<string | null>(null);

  // Seed the sidecar layout once from the first canvas build so the default
  // auto-layout positions are retained as the baseline for subsequent drags.
  React.useEffect(() => {
    const initial = buildCanvas(parsed.ok ? parsed.value : undefined);
    for (const n of initial.nodes) layoutRef.current[n.id] = n.position;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Rebuild the canvas ONLY on an external value change (not our own echo). An
  // echo is detected by comparing the incoming value against the last tree we
  // emitted; a drag/edge/spec edit flows through emit() and is skipped here so
  // the live node positions survive.
  // 
  // Use the RAW value (not parsed.value) for the dependency key, because
  // parsed.value may be the same EMPTY_TREE reference for both undefined and
  // the actual loaded data, which wouldn't trigger re-render.
  const valueKey = React.useMemo(() => safeStringify(value), [value]);
  React.useEffect(() => {
    if (lastEmittedRef.current !== null && valueKey === lastEmittedRef.current) {
      return; // our own round-trip — keep the live canvas
    }
    const g = buildCanvas(parsed.ok ? parsed.value : undefined);
    for (const n of g.nodes) layoutRef.current[n.id] = n.position;
    setNodes(g.nodes.map(toFlowNode));
    setEdges(g.edges.map(toFlowEdge));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [valueKey]);

  const emit = React.useCallback(
    (next: unknown) => onChange({ target: { name, value: next, type: 'json' } }),
    [name, onChange]
  );

  // Debounced re-serialize of the whole canvas to the engine tree.
  const timer = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  const reserialize = React.useCallback(
    (nextNodes: Node[], nextEdges: Edge[]) => {
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => {
        const graph: CanvasGraph = {
          nodes: nextNodes.map(fromFlowNode),
          edges: nextEdges.map((e) => ({
            id: e.id,
            source: e.source,
            target: e.target,
            branchKey: typeof e.label === 'string' ? e.label : undefined,
          })),
        };
        try {
          const { tree, layout } = canvasToTree(graph);
          layoutRef.current = layout; // sidecar — kept OUT of the engine tree
          // Record what we're emitting so the value-change effect recognises its
          // own echo and does NOT rebuild (which would reset drag positions).
          lastEmittedRef.current = safeStringify(tree);
          emit(tree);
        } catch {
          // No trigger root yet / dangling edge: skip the write until the canvas
          // forms a valid tree, rather than emitting a broken value.
        }
      }, DEBOUNCE_MS);
    },
    [emit]
  );
  React.useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  const onNodesChange = React.useCallback(
    (changes: NodeChange[]) => {
      setNodes((cur) => {
        const next = applyNodeChanges(changes, cur);
        reserialize(next, edges);
        return next;
      });
    },
    [edges, reserialize]
  );

  const onEdgesChange = React.useCallback(
    (changes: EdgeChange[]) => {
      setEdges((cur) => {
        const next = applyEdgeChanges(changes, cur);
        reserialize(nodes, next);
        return next;
      });
    },
    [nodes, reserialize]
  );

  const onConnect = React.useCallback(
    (connection: Connection) => {
      setEdges((cur) => {
        const next = addEdge(connection, cur);
        reserialize(nodes, next);
        return next;
      });
    },
    [nodes, reserialize]
  );

  const addNode = React.useCallback(
    (nodeType: string) => {
      const position = { x: 120 + nodes.length * 24, y: 80 + nodes.length * 24 };
      const node = makeFlowNode(nodeType, position);
      setNodes((cur) => {
        const next = [...cur, node];
        reserialize(next, edges);
        return next;
      });
    },
    [nodes.length, edges, reserialize]
  );

  // Palette drag source: stash the node type on the drag's dataTransfer so the
  // canvas drop handler knows what to create. Click-to-add (addNode) stays as a
  // fallback for environments without HTML5 drag-and-drop.
  const onPaletteDragStart = React.useCallback(
    (nodeType: string) => (e: React.DragEvent<HTMLElement>) => {
      e.dataTransfer.setData(DND_MIME, nodeType);
      e.dataTransfer.effectAllowed = 'move';
    },
    []
  );

  // Canvas drop target: allow the drop and show the move cursor.
  const onDragOver = React.useCallback((e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
  }, []);

  // Canvas drop: read the palette node type, map the pointer to canvas coords
  // via screenToFlowPosition, append the node and reserialize so the engine tree
  // (onChange value) gains the new node.
  const onDrop = React.useCallback(
    (e: React.DragEvent<HTMLDivElement>) => {
      e.preventDefault();
      if (disabled) return;
      const nodeType = e.dataTransfer.getData(DND_MIME);
      if (!nodeType) return;
      const position = screenToFlowPosition({ x: e.clientX, y: e.clientY });
      const node = makeFlowNode(nodeType, position);
      layoutRef.current[node.id] = position;
      setNodes((cur) => {
        const next = [...cur, node];
        reserialize(next, edges);
        return next;
      });
    },
    [disabled, screenToFlowPosition, edges, reserialize]
  );

  const onSelectionChange = React.useCallback((params: { nodes: Node[] }) => {
    const sel = params.nodes[0] ?? null;
    setSelectedId(sel?.id ?? null);
    setSpecError(null);
    if (sel) {
      const spec = (sel.data as { spec?: Record<string, unknown> })?.spec ?? {};
      setSpecDraft(safeStringify(spec));
    } else {
      setSpecDraft('');
    }
  }, []);

  // Side-panel: edit the selected node's spec as JSON. The spec passes through
  // the serializer verbatim, so mixed casing is preserved (finding 10).
  const onSpecChange = React.useCallback(
    (e: React.ChangeEvent<HTMLTextAreaElement>) => {
      const text = e.currentTarget.value;
      setSpecDraft(text);
      if (!selectedId) return;
      try {
        const spec = text.trim() === '' ? {} : JSON.parse(text);
        setSpecError(null);
        setNodes((cur) => {
          const next = cur.map((n) =>
            n.id === selectedId ? { ...n, data: { ...n.data, spec } } : n
          );
          reserialize(next, edges);
          return next;
        });
      } catch (err) {
        setSpecError((err as Error).message);
      }
    },
    [selectedId, edges, reserialize]
  );

  const label = intlLabel?.defaultMessage ?? name;

  if (!parsed.ok) {
    return (
      <Field.Root name={name} hint={hint} error={error ?? parsed.error} required={required}>
        <Field.Label>{label}</Field.Label>
        <RawJsonFallback
          ref={ref}
          initial={parsed.raw || safeStringify(EMPTY_TREE)}
          disabled={disabled}
          onCommit={(v) => emit(v)}
        />
        <Field.Error />
      </Field.Root>
    );
  }

  // Show skeleton while initializing
  if (!isReady) {
    return (
      <Field.Root name={name} hint={hint} error={error} required={required}>
        <Field.Label>{label}</Field.Label>
        <EditorSkeleton height={600} label="Initializing flow canvas…" />
      </Field.Root>
    );
  }

  return (
    <Field.Root name={name} hint={hint} error={error} required={required}>
      <Field.Label>{label}</Field.Label>
      <Flex
        ref={ref}
        direction="row"
        alignItems="stretch"
        gap={3}
        style={{ width: EDITOR_WIDTH, maxWidth: '100%' }}
      >
        {/* Canvas container: 75% of the editor width. ReactFlow needs explicit
            measured dimensions. position:relative contains the absolute-positioned
            Controls/MiniMap. overflow:hidden prevents canvas internals from bleeding
            into the palette panel. */}
        <Box
          style={{
            width: '75%',
            height: CANVAS_HEIGHT,
            minHeight: 500,
            position: 'relative',
            overflow: 'hidden',
          }}
          hasRadius
          borderColor="neutral200"
          borderWidth="1px"
          background="neutral0"
        >
          <div
            style={{ width: '100%', height: '100%' }}
            onDragOver={onDragOver}
            onDrop={onDrop}
          >
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              onNodesChange={disabled ? undefined : onNodesChange}
              onEdgesChange={disabled ? undefined : onEdgesChange}
              onConnect={disabled ? undefined : onConnect}
              onSelectionChange={onSelectionChange}
              nodesDraggable={!disabled}
              nodesConnectable={!disabled}
              deleteKeyCode={disabled ? null : ['Backspace', 'Delete']}
              fitView
            >
              <Background color="#4a4a6a" gap={16} />
              <Controls />
              <MiniMap nodeColor="#4945ff" maskColor="#4945ff33" />
            </ReactFlow>
          </div>
        </Box>

        {/* Palette panel: 25% of the editor width, with scrollable content. */}
        <Box
          style={{
            width: '25%',
            minWidth: 200,
            height: CANVAS_HEIGHT,
            minHeight: 500,
            overflowY: 'auto',
            background: '#212134',
          }}
          padding={3}
          hasRadius
        >
          <Flex direction="column" alignItems="stretch" gap={3}>
            <div style={{ color: '#ffffff', fontSize: 11, fontWeight: 600, textTransform: 'uppercase' }}>
              Palette (drag or click)
            </div>
            <Flex direction="row" wrap="wrap" gap={2}>
              {NODE_PALETTE.map((t) => (
                <PaletteItem
                  key={t}
                  nodeType={t}
                  disabled={disabled}
                  onDragStart={onPaletteDragStart(t)}
                  onClick={() => addNode(t)}
                />
              ))}
            </Flex>

            <Box paddingTop={2} style={{ borderTop: '1px solid #4a4a6a' }}>
              <div style={{ color: '#ffffff', fontSize: 11, fontWeight: 600, textTransform: 'uppercase' }}>
                {selectedId ? `Node Spec · ${selectedId}` : 'Select a node to edit'}
              </div>
            </Box>

            {selectedId ? (
              <Field.Root error={specError ?? undefined}>
                <Textarea
                  name="node-spec"
                  value={specDraft}
                  disabled={disabled}
                  onChange={onSpecChange}
                  rows={12}
                  style={{ fontFamily: 'monospace', fontSize: 12 }}
                />
                <Field.Error />
              </Field.Root>
            ) : (
              <div style={{ color: '#a5a5ba', fontSize: 12 }}>
                Click a node on the canvas to edit its spec JSON. Press Delete or Backspace to remove selected nodes.
              </div>
            )}
          </Flex>
        </Box>
      </Flex>
    </Field.Root>
  );
});

FlowCanvasInner.displayName = 'FlowCanvasInner';

/**
 * Public custom-field Input. Wraps the canvas in <ReactFlowProvider> so the
 * inner component's useReactFlow()/screenToFlowPosition resolve, enabling the
 * palette->canvas HTML5 drag-and-drop drop handler.
 */
const FlowCanvasField = React.forwardRef<HTMLDivElement, InputProps>((props, ref) => (
  <ReactFlowProvider>
    <FlowCanvasInner ref={ref} {...props} />
  </ReactFlowProvider>
));

FlowCanvasField.displayName = 'FlowCanvasField';

export default FlowCanvasField;
