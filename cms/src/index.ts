import type { Core } from '@strapi/strapi';
import { buildPublishMiddleware } from './publish-middleware';

// The two rule-engine custom fields (flow-canvas, jdm-editor) are ALSO registered
// here at the app level — not only inside the local rule-engine plugin.
//
// Why: Strapi converts the content-type schemas' `type:'customField'` attributes
// (api::flow.flow `tree` -> plugin::rule-engine.flow-canvas, api::jdm.jdm `doc` ->
// plugin::rule-engine.jdm-editor) during core registration. A LOCAL plugin's own
// register() can run after that conversion, so relying on it alone fails to boot
// with "Could not find Custom Field: plugin::rule-engine.flow-canvas". Registering
// here (app register runs early) guarantees the fields exist before any schema
// that references them is converted. The plugin's server entry keeps its own
// register for the admin-panel half; strapi.customFields.register is idempotent
// on the same uid.

const PLUGIN_ID = 'rule-engine';

export default {
  register({ strapi }: { strapi: Core.Strapi }) {
    strapi.customFields.register([
      { name: 'flow-canvas', plugin: PLUGIN_ID, type: 'json' },
      { name: 'jdm-editor', plugin: PLUGIN_ID, type: 'json' },
    ]);
  },

  // The publish interception middleware MUST be registered here, at the app
  // level. A Document Service middleware registered from the local plugin's
  // register() does NOT fire on the content-manager publish path (proven live);
  // the app-level bootstrap registration DOES see context.action === 'publish'.
  bootstrap({ strapi }: { strapi: Core.Strapi }) {
    strapi.documents.use(buildPublishMiddleware(strapi));
  },
};
