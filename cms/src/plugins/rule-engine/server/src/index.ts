// rule-engine plugin — server entry (design §4, §5.3/§5.4). Registers:
//   * the two custom fields on the server (base type:'json') so Strapi's server
//     validates Flow.tree/Jdm.doc under the custom field uids
//     plugin::rule-engine.flow-canvas / .jdm-editor;
//   * the publish controller + route.
//
// STRAPI 5 FIX: the publish interception is a Document Service middleware
// (strapi.documents.use). PROVEN LIVE during planning: registering that
// middleware from THIS plugin register() does NOT fire on the content-manager
// publish path, so it has been relocated to the APP-LEVEL cms/src/index.ts
// bootstrap({ strapi }) (see cms/src/publish-middleware.ts). This entry keeps
// only the custom-field registration, the publish controller, and routes.

import publishController from './controllers/publish';
import auditController from './controllers/audit';
import syncController from './controllers/sync';
import environmentController from './controllers/environment';
import flowController from './controllers/flow';
import templateController from './controllers/template';
import entityPublishController from './controllers/entity-publish';
import validateController from './controllers/validate';
import routes from './routes';

const PLUGIN_ID = 'rule-engine';

export default {
  register({ strapi }: { strapi: any }) {
    // Register both custom fields server-side with base type:'json'.
    strapi.customFields.register([
      { name: 'flow-canvas', plugin: PLUGIN_ID, type: 'json' },
      { name: 'jdm-editor', plugin: PLUGIN_ID, type: 'json' },
    ]);
  },

  bootstrap() {},

  controllers: {
    publish: publishController,
    audit: auditController,
    sync: syncController,
    environment: environmentController,
    flow: flowController,
    template: templateController,
    'entity-publish': entityPublishController,
    validate: validateController,
  },

  routes,

  services: {},
};
