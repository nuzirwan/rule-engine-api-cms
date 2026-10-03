// rule-engine plugin — server entry (design §5.3/§5.4). Registers the publish
// controller + route and wires the Flow `beforePublish` lifecycle so the
// publish-blocking gate (validation.ts) runs on every Draft & Publish "publish"
// regardless of entry point.
//
// The plugin stays disabled in config/plugins.ts until FEAT-004 ships the admin
// UI; this entry completes the server wiring so the gate is live the instant the
// plugin is enabled.

import publishController from './controllers/publish';
import routes from './routes';
import { flowPublishLifecycle } from './lifecycles/flow';

export default {
  register({ strapi }: { strapi: any }) {
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
