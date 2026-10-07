// environment.ts — controller for environment management.
// Provides endpoints:
//   * GET /environments — list all Environment content type entries
//   * POST /environments — create a new Environment
//   * PUT /environments/:id — update an existing Environment
//   * DELETE /environments/:id — delete an Environment
//   * POST /environments/:id/test — test connection to an environment's engine
//   * POST /environments/:id/validate-flow/:flowId — validate a flow against an environment's engine

import { AdminClient, resolveAdminConfig, AdminApiError } from '../services/admin-client';

/**
 * Environment controller factory.
 * Returns handlers for environment-related endpoints.
 */
export default function environmentController({ strapi }: { strapi: any }) {
  const ENV_UID = 'api::environment.environment';
  const FLOW_UID = 'api::flow.flow';

  /**
   * Map database record to API response shape.
   */
  function mapEnvironment(r: any) {
    return {
      documentId: r.documentId,
      name: r.name,
      adminApiBaseUrl: r.adminApiBaseUrl ?? null,
      operatorTokenRef: r.operatorTokenRef ?? null,
      payloadEnv: r.payloadEnv ?? '',
    };
  }

  return {
    /**
     * GET /environments — list all Environment entries from the CMS.
     * Returns { environments: Environment[] }.
     */
    async list(ctx: any) {
      const results = await strapi.documents(ENV_UID).findMany({
        fields: ['name', 'adminApiBaseUrl', 'operatorTokenRef', 'payloadEnv'],
      });

      ctx.body = {
        environments: results.map(mapEnvironment),
      };
    },

    /**
     * POST /environments — create a new Environment entry.
     * Body: { name, adminApiBaseUrl?, operatorTokenRef?, payloadEnv? }
     * Returns the created environment.
     */
    async create(ctx: any) {
      const { name, adminApiBaseUrl, operatorTokenRef, payloadEnv } = ctx.request.body;

      // Validate required field
      if (!name || typeof name !== 'string' || !name.trim()) {
        ctx.status = 400;
        ctx.body = { error: 'Name is required' };
        return;
      }

      // Check for duplicate name
      const existing = await strapi.documents(ENV_UID).findMany({
        filters: { name: { $eqi: name.trim() } },
        limit: 1,
      });

      if (existing.length > 0) {
        ctx.status = 409;
        ctx.body = { error: 'An environment with this name already exists' };
        return;
      }

      try {
        const created = await strapi.documents(ENV_UID).create({
          data: {
            name: name.trim(),
            adminApiBaseUrl: adminApiBaseUrl || null,
            operatorTokenRef: operatorTokenRef || null,
            payloadEnv: payloadEnv || '',
          },
        });

        ctx.status = 201;
        ctx.body = mapEnvironment(created);
      } catch (err: unknown) {
        strapi.log.error('Failed to create environment:', err);
        ctx.status = 500;
        ctx.body = { error: 'Failed to create environment' };
      }
    },

    /**
     * PUT /environments/:id — update an existing Environment entry.
     * Body: { name?, adminApiBaseUrl?, operatorTokenRef?, payloadEnv? }
     * Returns the updated environment.
     */
    async update(ctx: any) {
      const { id } = ctx.params;
      const { adminApiBaseUrl, operatorTokenRef, payloadEnv } = ctx.request.body;

      // Find existing
      const existing = await strapi.documents(ENV_UID).findOne({
        documentId: id,
      });

      if (!existing) {
        ctx.status = 404;
        ctx.body = { error: 'Environment not found' };
        return;
      }

      try {
        const updated = await strapi.documents(ENV_UID).update({
          documentId: id,
          data: {
            // Name cannot be changed (uid field)
            adminApiBaseUrl: adminApiBaseUrl !== undefined ? (adminApiBaseUrl || null) : existing.adminApiBaseUrl,
            operatorTokenRef: operatorTokenRef !== undefined ? (operatorTokenRef || null) : existing.operatorTokenRef,
            payloadEnv: payloadEnv !== undefined ? (payloadEnv || '') : existing.payloadEnv,
          },
        });

        ctx.body = mapEnvironment(updated);
      } catch (err: unknown) {
        strapi.log.error('Failed to update environment:', err);
        ctx.status = 500;
        ctx.body = { error: 'Failed to update environment' };
      }
    },

    /**
     * DELETE /environments/:id — delete an Environment entry.
     * Returns { success: true } on success.
     */
    async remove(ctx: any) {
      const { id } = ctx.params;

      // Find existing
      const existing = await strapi.documents(ENV_UID).findOne({
        documentId: id,
      });

      if (!existing) {
        ctx.status = 404;
        ctx.body = { error: 'Environment not found' };
        return;
      }

      try {
        await strapi.documents(ENV_UID).delete({
          documentId: id,
        });

        ctx.body = { success: true };
      } catch (err: unknown) {
        strapi.log.error('Failed to delete environment:', err);
        ctx.status = 500;
        ctx.body = { error: 'Failed to delete environment' };
      }
    },

    /**
     * POST /environments/:id/test — test connection to an environment's engine.
     * Creates an AdminClient from the environment's config and calls GET /livez
     * (or /readyz) to verify reachability.
     * Returns { success: boolean, message: string, responseTimeMs?: number }.
     */
    async testConnection(ctx: any) {
      const { id } = ctx.params;

      // Find the environment
      const env = await strapi.documents(ENV_UID).findOne({
        documentId: id,
      });

      if (!env) {
        ctx.status = 404;
        ctx.body = { error: 'Environment not found' };
        return;
      }

      try {
        // Resolve admin config from environment
        const config = resolveAdminConfig({
          adminApiBaseUrl: env.adminApiBaseUrl,
          operatorTokenRef: env.operatorTokenRef,
          payloadEnv: env.payloadEnv,
        });

        const client = new AdminClient(config);
        const startTime = Date.now();

        // Try to list flows as a health check (GET /admin/flows)
        // This verifies both connectivity and authentication
        await client.listFlows();

        const responseTimeMs = Date.now() - startTime;

        ctx.body = {
          success: true,
          message: `Connected successfully to ${config.baseUrl}`,
          responseTimeMs,
        };
      } catch (err: unknown) {
        if (err instanceof AdminApiError) {
          let message = `Connection failed: ${err.message}`;
          if (err.status === 401 || err.status === 403) {
            message = 'Authentication failed: invalid or expired token';
          } else if (err.status === 0) {
            message = 'Connection failed: unable to reach the engine';
          }

          ctx.body = {
            success: false,
            message,
          };
        } else {
          const message = err instanceof Error ? err.message : 'Unknown error';
          ctx.body = {
            success: false,
            message: `Configuration error: ${message}`,
          };
        }
      }
    },

    /**
     * POST /environments/:id/validate-flow/:flowId — validate a flow against a specific environment's engine.
     * 
     * This endpoint allows validating a flow definition against a particular environment,
     * which is useful for testing flows against staging/production environments before
     * actually publishing to them.
     * 
     * Body: { version?: number } — optional version to validate (defaults to latest)
     * Returns validation result from the engine.
     */
    async validateFlow(ctx: any) {
      const { id: envId, flowId } = ctx.params;
      const { version } = ctx.request.body || {};

      // Find the environment
      const env = await strapi.documents(ENV_UID).findOne({
        documentId: envId,
      });

      if (!env) {
        ctx.status = 404;
        ctx.body = { error: 'Environment not found' };
        return;
      }

      // Find the flow in CMS
      const flows = await strapi.documents(FLOW_UID).findMany({
        filters: { flowId: { $eq: flowId } },
        limit: 1,
      });

      if (flows.length === 0) {
        ctx.status = 404;
        ctx.body = { error: 'Flow not found in CMS' };
        return;
      }

      try {
        // Resolve admin config from environment
        const config = resolveAdminConfig({
          adminApiBaseUrl: env.adminApiBaseUrl,
          operatorTokenRef: env.operatorTokenRef,
          payloadEnv: env.payloadEnv,
        });

        const client = new AdminClient(config);

        // If version not specified, we need to get the flow's current version from the engine
        // The validateFlow endpoint requires both flowId and version
        let targetVersion = version;
        
        if (targetVersion === undefined) {
          // Try to get the latest version from the engine
          try {
            const flowVersions = await client.listFlowVersions(flowId);
            if (flowVersions.versions.length === 0) {
              ctx.status = 404;
              ctx.body = { 
                error: 'Flow has no versions in the engine. Publish the flow first.',
                recoverable: false,
              };
              return;
            }
            // Get the latest version
            targetVersion = Math.max(...flowVersions.versions.map(v => v.version));
          } catch (err) {
            if (err instanceof AdminApiError && err.status === 404) {
              ctx.status = 404;
              ctx.body = { 
                error: 'Flow not found in engine. Publish the flow first.',
                recoverable: false,
              };
              return;
            }
            throw err;
          }
        }

        // Validate the flow against the environment's engine
        const result = await client.validateFlow(flowId, targetVersion);

        ctx.body = {
          success: true,
          flowId,
          version: targetVersion,
          environment: {
            id: envId,
            name: env.name,
          },
          validation: result,
        };
      } catch (err: unknown) {
        if (err instanceof AdminApiError) {
          const recoverable = err.recoverable;
          ctx.status = recoverable ? 503 : (err.status || 500);
          ctx.body = {
            error: err.message,
            recoverable,
            flowId,
            environment: {
              id: envId,
              name: env.name,
            },
          };
        } else {
          const message = err instanceof Error ? err.message : 'Unknown error';
          ctx.status = 500;
          ctx.body = {
            error: `Configuration error: ${message}`,
            recoverable: false,
          };
        }
      }
    },
  };
}
