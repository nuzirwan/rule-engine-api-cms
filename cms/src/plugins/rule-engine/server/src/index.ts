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
import connectionTestController from './controllers/connection-test';
import routes from './routes';

export default {
  // NOTE: the two custom fields (flow-canvas, jdm-editor) are registered at the
  // APP level (cms/src/index.ts register()), which runs early enough that the
  // content-type schema conversion finds them. We must NOT re-register them here:
  // strapi.customFields.register throws "already registered" on a duplicate uid,
  // and this plugin server entry now loads (its routes/controllers are the whole
  // point), so a duplicate register() here would abort boot.
  register() {},

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
    'connection-test': connectionTestController,
  },

  routes,

  services: {},
};
