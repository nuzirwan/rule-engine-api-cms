// rule-engine plugin — admin entry (design §4). Registers BOTH visual editors as
// Strapi 5 custom fields, each with base `type: 'json'` (finding 13), so Flow.tree
// and Jdm.doc persist their serialized value into the content type's JSON column.
//
// The actual editor components (jdm-editor / reactflow) load lazily via the
// descriptors' async `Input` loaders (see customFields.ts), so this entry stays
// light and the admin bundle only pulls an editor when its field renders.

import { buildCustomFields, PLUGIN_ID } from './customFields';

export default {
  register(app: any) {
    for (const field of buildCustomFields()) {
      app.customFields.register(field);
    }
  },

  bootstrap() {},

  // Lazily load admin translations if/when they are added; none are shipped in
  // FEAT-004 (the intl messages fall back to their defaultMessage).
  async registerTrads() {
    return [];
  },
};

export { PLUGIN_ID };
