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

import * as React from 'react';
import {
  ReactFlow,
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
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import { Box, Button, Field, Flex, Textarea, Typography } from '@strapi/design-system';

import { parseStoredJson, safeStringify } from '../../lib/parseStored';
import { RawJsonFallback } from '../RawJsonFallback';
import {
  canvasToTree,
  treeToCanvas,
  type CanvasEdge,
  type CanvasGraph,
  type CanvasNode,
  type EngineNode,
  type LayoutMap,
} from './serialize';

/** The engine Node taxonomy the palette offers (design §4.2 / HLD §3). */
export const NODE_PALETTE = [
  'trigger',
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
] as const;

const EMPTY_TREE: EngineNode = { id: 'trigger', type: 'trigger', spec: {} };
const DEBOUNCE_MS = 300;

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

const FlowCanvasField = React.forwardRef<HTMLDivElement, InputProps>((props, ref) => {
  const { name, value, onChange, intlLabel, hint, required, error, disabled } = props;

  const parsed = React.useMemo(() => parseStoredJson<EngineNode>(value, EMPTY_TREE), [value]);

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
      const id = `${nodeType}-${Math.random().toString(36).slice(2, 8)}`;
      const position = { x: 120 + nodes.length * 24, y: 80 + nodes.length * 24 };
      const node: Node = {
        id,
        position,
        data: { nodeType, spec: {}, label: `${nodeType} · ${id}` },
        type: 'default',
      };
      setNodes((cur) => {
        const next = [...cur, node];
        reserialize(next, edges);
        return next;
      });
    },
    [nodes.length, edges, reserialize]
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

  return (
    <Field.Root name={name} hint={hint} error={error} required={required}>
      <Field.Label>{label}</Field.Label>
      <Flex ref={ref} direction="row" alignItems="stretch" gap={2} style={{ width: '100%' }}>
        {/* @xyflow/react v12 needs its parent to have an explicit, non-zero MEASURED
            width AND height. In a flex row the default min-width is `auto`, so a
            `flex:1` child can collapse to the intrinsic (near-zero) width of the
            canvas and break the pointer/zoom math that drag relies on. `minWidth:0`
            plus a concrete `flexBasis` give the child a real measured width, and the
            inner div pins width/height to 100% so ReactFlow measures a non-zero box. */}
        <Box
          style={{ flex: '1 1 0%', minWidth: 0, height: 560 }}
          hasRadius
          borderColor="neutral200"
          borderWidth="1px"
        >
          <div style={{ width: '100%', height: '100%', minWidth: 480 }}>
            <ReactFlow
              nodes={nodes}
              edges={edges}
              onNodesChange={disabled ? undefined : onNodesChange}
              onEdgesChange={disabled ? undefined : onEdgesChange}
              onConnect={disabled ? undefined : onConnect}
              onSelectionChange={onSelectionChange}
              nodesDraggable={!disabled}
              nodesConnectable={!disabled}
              fitView
            >
              <Background />
              <Controls />
              <MiniMap />
            </ReactFlow>
          </div>
        </Box>
        <Box style={{ width: 280 }} padding={2} background="neutral100" hasRadius>
          <Flex direction="column" alignItems="stretch" gap={2}>
            <Typography variant="sigma">Palette</Typography>
            <Flex direction="row" wrap="wrap" gap={1}>
              {NODE_PALETTE.map((t) => (
                <Button
                  key={t}
                  size="S"
                  variant="tertiary"
                  disabled={disabled}
                  onClick={() => addNode(t)}
                >
                  {t}
                </Button>
              ))}
            </Flex>
            <Typography variant="sigma">
              {selectedId ? `Spec · ${selectedId}` : 'Select a node to edit its spec'}
            </Typography>
            {selectedId ? (
              <Field.Root error={specError ?? undefined}>
                <Textarea
                  name="node-spec"
                  value={specDraft}
                  disabled={disabled}
                  onChange={onSpecChange}
                  rows={14}
                />
                <Field.Error />
              </Field.Root>
            ) : null}
          </Flex>
        </Box>
      </Flex>
    </Field.Root>
  );
});

FlowCanvasField.displayName = 'FlowCanvasField';

export default FlowCanvasField;
