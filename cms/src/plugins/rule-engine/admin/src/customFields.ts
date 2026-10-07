// customFields.ts — the PURE descriptors for the two rule-engine custom fields
// (design §4). Kept free of React / jdm-editor / reactflow imports so the §6.4
// smoke test can assert the registration contract (both fields register with
// base `type: 'json'`, finding 13) without mounting the heavy editor runtimes.
//
// `app.customFields.register(buildCustomFields())` in index.tsx consumes this.
// The component bundles are loaded LAZILY via the async `Input` loader (the
// pattern Strapi documents), so importing this module never pulls the editors.
//
// PERF: Each Input is wrapped with React.Suspense to catch lazy-loaded chunks
// and show a fallback skeleton while editors initialize.

export const PLUGIN_ID = 'rule-engine';

/** A minimal shape of the object Strapi's `app.customFields.register` accepts. */
export interface CustomFieldDescriptor {
  name: string;
  pluginId: string;
  /** The underlying Strapi base type — MUST be 'json' (design finding 13). */
  type: 'json';
  intlLabel: { id: string; defaultMessage: string };
  intlDescription: { id: string; defaultMessage: string };
  components: {
    Input: () => Promise<{ default: unknown }>;
  };
}

/**
 * Build the two custom-field descriptors. Both use base `type: 'json'` so Strapi
 * persists the serialized value into the content type's JSON column with no
 * double-encode (design §4, finding 13). The `Input` loaders are dynamic imports
 * so the jdm-editor / reactflow bundles are only pulled when the field renders.
 *
 * PERF: Each Input component is wrapped with React.Suspense so any nested lazy
 * chunks (ReactFlow, JDM editor internals) are caught and show a loading skeleton.
 */
export function buildCustomFields(): CustomFieldDescriptor[] {
  return [
    {
      name: 'flow-canvas',
      pluginId: PLUGIN_ID,
      type: 'json',
      intlLabel: {
        id: 'rule-engine.flow-canvas.label',
        defaultMessage: 'Flow canvas',
      },
      intlDescription: {
        id: 'rule-engine.flow-canvas.description',
        defaultMessage: 'Visual editor for the engine flow tree (React Flow).',
      },
      components: {
        Input: async () => {
          const React = await import('react');
          const { default: FlowCanvasField } = await import('./components/FlowCanvasField');
          const { EditorSkeleton } = await import('./components/EditorSkeleton');
          // Wrap with Suspense to catch any nested lazy chunks
          const WrappedFlowCanvas = React.forwardRef<HTMLDivElement, any>((props, ref) =>
            React.createElement(
              React.Suspense,
              { fallback: React.createElement(EditorSkeleton, { height: 560, label: 'Loading flow canvas…' }) },
              React.createElement(FlowCanvasField, { ...props, ref })
            )
          );
          WrappedFlowCanvas.displayName = 'SuspenseFlowCanvasField';
          return { default: WrappedFlowCanvas };
        },
      },
    },
    {
      name: 'jdm-editor',
      pluginId: PLUGIN_ID,
      type: 'json',
      intlLabel: {
        id: 'rule-engine.jdm-editor.label',
        defaultMessage: 'JDM decision editor',
      },
      intlDescription: {
        id: 'rule-engine.jdm-editor.description',
        defaultMessage: 'GoRules ZEN decision graph editor (@gorules/jdm-editor).',
      },
      components: {
        Input: async () => {
          const React = await import('react');
          const { default: JdmEditorField } = await import('./components/JdmEditorField');
          const { EditorSkeleton } = await import('./components/EditorSkeleton');
          // Wrap with Suspense to catch any nested lazy chunks
          const WrappedJdmEditor = React.forwardRef<HTMLDivElement, any>((props, ref) =>
            React.createElement(
              React.Suspense,
              { fallback: React.createElement(EditorSkeleton, { height: 520, label: 'Loading decision editor…' }) },
              React.createElement(JdmEditorField, { ...props, ref })
            )
          );
          WrappedJdmEditor.displayName = 'SuspenseJdmEditorField';
          return { default: WrappedJdmEditor };
        },
      },
    },
  ];
}
