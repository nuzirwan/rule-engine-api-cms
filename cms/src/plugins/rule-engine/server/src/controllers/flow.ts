// flow.ts — controller for flow version history and rollback. Provides endpoints:
//   * GET  /flows/:id/versions  — list all versions of a flow
//   * POST /flows/:id/rollback  — rollback to a specific version
//
// Error classification follows the §5.5 table (same as sync.ts):
//   * 5xx / transport (recoverable) → 503
//   * 4xx (non-recoverable) → echo the engine status
//   * Config missing → 503 (env not provisioned)

import { AdminApiError, AdminClient, resolveAdminConfig } from '../services/admin-client';
import type { ListFlowVersionsResponse, SetActiveResponse } from '../../../../../../types/engine';

/**
 * Flow controller factory. The optional `fetchImpl` parameter is a test seam:
 * it is passed through to AdminClient so tests can inject the nock-compatible
 * http-fetch shim.
 */
export default function flowController(
  { strapi }: { strapi: any },
  fetchImpl?: typeof fetch
) {
  /**
   * Create an AdminClient instance with resolved config.
   * Returns the client on success, or sets ctx.status/ctx.body and returns null on failure.
   */
  function createClient(ctx: any): AdminClient | null {
    let config;
    try {
      config = resolveAdminConfig();
    } catch (err) {
      ctx.status = 503;
      ctx.body = { error: (err as Error).message, recoverable: false };
      return null;
    }
    return fetchImpl ? new AdminClient(config, fetchImpl) : new AdminClient(config);
  }

  /**
   * Handle AdminApiError: recoverable (5xx) → 503, non-recoverable → echo status.
   */
  function handleApiError(ctx: any, err: unknown): void {
    if (err instanceof AdminApiError) {
      ctx.status = err.recoverable ? 503 : (err.status || 500);
      ctx.body = { error: err.message, recoverable: err.recoverable };
    } else {
      throw err;
    }
  }

  return {
    /**
     * GET /flows/:id/versions — list all versions for a flow.
     */
    async getVersions(ctx: any) {
      const { id } = ctx.params as { id: string };

      const client = createClient(ctx);
      if (!client) return;

      try {
        const response: ListFlowVersionsResponse = await client.listFlowVersions(id);
        ctx.body = response;
      } catch (err) {
        handleApiError(ctx, err);
      }
    },

    /**
     * POST /flows/:id/rollback — rollback a flow to a specific version.
     * Body: { version: number }
     */
    async rollback(ctx: any) {
      const { id } = ctx.params as { id: string };
      const { version } = ctx.request.body as { version: number };

      const client = createClient(ctx);
      if (!client) return;

      try {
        const response: SetActiveResponse = await client.rollbackFlow(id, version);
        ctx.body = response;
      } catch (err) {
        handleApiError(ctx, err);
      }
    },
  };
}
