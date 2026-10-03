// rule-engine plugin — server entry (design §4, §5.3/§5.4). Registers:
//   * the two custom fields on the server (design §4, finding 13) — BOTH with
//     base type:'json' — so Strapi's server validates Flow.tree/Jdm.doc using
//     the custom field uid `plugin::rule-engine.flow-canvas`/`.jdm-editor`;
//   * the publish controller + route;
//   * the Flow `beforePublish` lifecycle so the publish-blocking gate
//     (validation.ts) runs on every Draft & Publish "publish" regardless of
//     entry point.
//
// A custom field must be registered on BOTH the server and the admin panel
// (the admin half lives in admin/src/index.tsx). The server registration here
// is what makes the `type:'customField'` attribute in the content-type schemas
// valid at load time.

import publishController from './controllers/publish';
import routes from './routes';
import { flowPublishLifecycle } from './lifecycles/flow';

const PLUGIN_ID = 'rule-engine';

export default {
  register({ strapi }: { strapi: any }) {
    // Register both custom fields server-side with base type:'json' (finding 13).
    strapi.customFields.register([
      { name: 'flow-canvas', plugin: PLUGIN_ID, type: 'json' },
      { name: 'jdm-editor', plugin: PLUGIN_ID, type: 'json' },
    ]);

    // Subscribe the beforePublish gate onto the Flow content type. The lifecycle
    // delegates to the same runPublishSequence the controller uses, so the gate
    // holds identically from either path.
    const lifecycle = flowPublishLifecycle(strapi);
    strapi.db.lifecycles.subscribe({
      models: ['api::flow.flow'],
      beforePublish: lifecycle.beforePublish,
    });
  },

  bootstrap() {},

  controllers: {
    publish: publishController,
  },

  routes,

  services: {},
};
