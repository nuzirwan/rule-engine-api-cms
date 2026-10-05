// Admin-scoped routes for the rule-engine plugin controllers. The routes are
// mounted on the plugin's admin router so they sit behind Strapi's admin auth +
// RBAC; handlers run through the engine admin HTTP API via AdminClient.

export default {
  admin: {
    type: 'admin',
    routes: [
      {
        method: 'POST',
        path: '/flows/:id/publish',
        handler: 'publish.publish',
        config: {
          // Guarded by Strapi admin auth; a dedicated RBAC action can be added
          // when the admin UI (FEAT-004) ships its permission wiring.
          policies: [],
        },
      },
      {
        // Proxy GET /admin/audit/{type}/{id} from the engine — returns audit
        // trail for flow, jdm, or connection entities.
        method: 'GET',
        path: '/audit/:type/:id',
        handler: 'audit.audit',
        config: {
          policies: [],
        },
      },
    ],
  },
};
