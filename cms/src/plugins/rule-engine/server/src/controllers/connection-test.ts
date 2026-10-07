// connection-test.ts — controller for testing connection credentials. Provides:
//   * POST /rule-engine/connections/test — test a connection with ephemeral credentials
//
// This endpoint proxies to the engine's POST /admin/connections/test, passing the
// connection type, settings, and secret. The secret is used once to test the
// connection and is never stored.
//
// Error classification follows the §5.5 table:
//   * 5xx / transport (recoverable) → 503
//   * 4xx (non-recoverable) → echo the engine status
//   * Config missing → 503 (env not provisioned)

import { AdminApiError, AdminClient, resolveAdminConfig } from '../services/admin-client';
import type { TestConnectionResponse } from '../../../../../../types/engine';

/** The request body for POST /rule-engine/connections/test. */
interface TestConnectionRequestBody {
  /** Connection type (e.g., "postgres", "mysql", "valkey"). */
  type: string;
  /** Connection settings (host, port, database, user, etc.). */
  settings: Record<string, unknown>;
  /** The plaintext secret/password to test with (never stored). */
  secret: string;
}

/**
 * Connection test controller factory. The optional `fetchImpl` parameter is a test seam:
 * it is passed through to AdminClient so tests can inject the nock-compatible
 * http-fetch shim.
 */
export default function connectionTestController(
  { strapi }: { strapi: unknown },
  fetchImpl?: typeof fetch
) {
  /**
   * Create an AdminClient instance with resolved config.
   * Returns the client on success, or sets ctx.status/ctx.body and returns null on failure.
   */
  function createClient(ctx: { status: number; body: unknown }): AdminClient | null {
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
  function handleApiError(ctx: { status: number; body: unknown }, err: unknown): void {
    if (err instanceof AdminApiError) {
      ctx.status = err.recoverable ? 503 : (err.status || 500);
      ctx.body = { error: err.message, recoverable: err.recoverable };
    } else {
      throw err;
    }
  }

  return {
    /**
     * POST /rule-engine/connections/test — test a connection with ephemeral credentials.
     *
     * Body: { type, settings, secret }
     * Returns: { success, message?, error? }
     */
    async testConnection(ctx: {
      request: { body: TestConnectionRequestBody };
      status: number;
      body: unknown;
    }) {
      const { type, settings, secret } = ctx.request.body;

      // Validate required fields
      if (!type || typeof type !== 'string') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "type" field' };
        return;
      }
      if (!settings || typeof settings !== 'object') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "settings" field' };
        return;
      }
      if (secret === undefined || secret === null || typeof secret !== 'string') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "secret" field' };
        return;
      }

      const client = createClient(ctx);
      if (!client) return;

      try {
        const response: TestConnectionResponse = await client.testConnection({
          type,
          settings,
          secret,
        });
        ctx.body = response;
      } catch (err) {
        handleApiError(ctx, err);
      }
    },
  };
}
