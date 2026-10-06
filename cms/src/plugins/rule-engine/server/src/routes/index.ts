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
      // --- Sync routes (FEAT-002) ---
      {
        // GET /sync/status — compare CMS vs engine, return diff status.
        method: 'GET',
        path: '/sync/status',
        handler: 'sync.syncStatus',
        config: {
          policies: [],
        },
      },
      {
        // POST /sync/import — pull all engine config and create/update CMS content.
        method: 'POST',
        path: '/sync/import',
        handler: 'sync.importAll',
        config: {
          policies: [],
        },
      },
      {
        // POST /sync/import/:type/:id — pull a single item from engine.
        method: 'POST',
        path: '/sync/import/:type/:id',
        handler: 'sync.importOne',
        config: {
          policies: [],
        },
      },
      // --- Environment routes (FEAT-001 multi-env-ui) ---
      {
        // GET /environments — list all Environment entries.
        method: 'GET',
        path: '/environments',
        handler: 'environment.list',
        config: {
          policies: [],
        },
      },
    ],
  },
};
