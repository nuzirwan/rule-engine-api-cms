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
      // --- Environment routes (FEAT-001/FEAT-002 multi-env-ui) ---
      {
        // GET /environments — list all Environment entries.
        method: 'GET',
        path: '/environments',
        handler: 'environment.list',
        config: {
          policies: [],
        },
      },
      {
        // POST /environments — create a new Environment entry.
        method: 'POST',
        path: '/environments',
        handler: 'environment.create',
        config: {
          policies: [],
        },
      },
      {
        // PUT /environments/:id — update an existing Environment entry.
        method: 'PUT',
        path: '/environments/:id',
        handler: 'environment.update',
        config: {
          policies: [],
        },
      },
      {
        // DELETE /environments/:id — delete an Environment entry.
        method: 'DELETE',
        path: '/environments/:id',
        handler: 'environment.remove',
        config: {
          policies: [],
        },
      },
      {
        // POST /environments/:id/test — test connection to an environment's engine.
        method: 'POST',
        path: '/environments/:id/test',
        handler: 'environment.testConnection',
        config: {
          policies: [],
        },
      },
      {
        // POST /environments/:id/validate-flow/:flowId — validate a flow against a specific environment's engine.
        // This allows validating the same flow definition against different environments.
        method: 'POST',
        path: '/environments/:id/validate-flow/:flowId',
        handler: 'environment.validateFlow',
        config: {
          policies: [],
        },
      },
      // --- Flow versioning routes (FEAT-001 flow-versioning-ui) ---
      {
        // GET /flows/:id/versions — list all versions for a flow.
        method: 'GET',
        path: '/flows/:id/versions',
        handler: 'flow.getVersions',
        config: {
          policies: [],
        },
      },
      {
        // POST /flows/:id/rollback — rollback to a specific version.
        method: 'POST',
        path: '/flows/:id/rollback',
        handler: 'flow.rollback',
        config: {
          policies: [],
        },
      },
      // --- Template routes (TASK-006 flow-templates) ---
      {
        // GET /templates — list all available templates.
        method: 'GET',
        path: '/templates',
        handler: 'template.listTemplates',
        config: {
          policies: [],
        },
      },
      {
        // GET /templates/:id — get a single template by ID.
        method: 'GET',
        path: '/templates/:id',
        handler: 'template.getTemplate',
        config: {
          policies: [],
        },
      },
      {
        // POST /templates/:id/preview — preview substituted flows.
        method: 'POST',
        path: '/templates/:id/preview',
        handler: 'template.previewTemplate',
        config: {
          policies: [],
        },
      },
      {
        // POST /templates/:id/instantiate — create flows from template.
        method: 'POST',
        path: '/templates/:id/instantiate',
        handler: 'template.instantiateTemplate',
        config: {
          policies: [],
        },
      },
    ],
  },
};
