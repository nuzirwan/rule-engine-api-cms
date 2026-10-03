// customFields.ts — the PURE descriptors for the two rule-engine custom fields
// (design §4). Kept free of React / jdm-editor / reactflow imports so the §6.4
// smoke test can assert the registration contract (both fields register with
// base `type: 'json'`, finding 13) without mounting the heavy editor runtimes.
//
// `app.customFields.register(buildCustomFields())` in index.tsx consumes this.
// The component bundles are loaded LAZILY via the async `Input` loader (the
// pattern Strapi documents), so importing this module never pulls the editors.

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
        Input: () =>
          import('./components/FlowCanvasField').then((m) => ({ default: m.default })),
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
        Input: () =>
          import('./components/JdmEditorField').then((m) => ({ default: m.default })),
      },
    },
  ];
}
