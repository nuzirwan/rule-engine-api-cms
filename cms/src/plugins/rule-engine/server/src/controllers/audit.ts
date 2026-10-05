// audit.ts — thin controller that proxies GET /admin/audit/{type}/{id} from the
// engine through the CMS plugin route. The admin panel fetches from the CMS
// route (Strapi admin auth); this controller resolves the AdminClient config
// from env vars and forwards the call.
//
// Error classification follows the §5.5 table:
//   * 5xx / transport (recoverable) → 503
//   * 4xx (author-fixable, non-recoverable) → echo the engine status
//   * Config missing (no base URL or token) → 503 (env not provisioned)

import { AdminApiError, AdminClient, resolveAdminConfig } from '../services/admin-client';
import type { AuditObjectType } from '../../../../../../types/engine';

const VALID_AUDIT_TYPES = new Set<string>(['flow', 'jdm', 'connection']);

/**
 * Audit controller factory. The optional `fetchImpl` parameter is a test seam:
 * it is passed through to AdminClient so tests can inject the nock-compatible
 * http-fetch shim and assert every request. Production callers pass no second
 * argument and get native fetch.
 */
export default function auditController(
  { strapi }: { strapi: any },
  fetchImpl?: typeof fetch
) {
  void strapi; // used by Strapi framework; no document service calls here
  return {
    async audit(ctx: any) {
      const { type, id } = ctx.params as { type: string; id: string };

      if (!VALID_AUDIT_TYPES.has(type)) {
        ctx.badRequest('invalid audit type: must be flow, jdm, or connection');
        return;
      }

      let config;
      try {
        config = resolveAdminConfig();
      } catch (err) {
        // Missing env vars — operator hasn't provisioned the environment.
        ctx.status = 503;
        ctx.body = { error: (err as Error).message, recoverable: false };
        return;
      }

      const client = fetchImpl
        ? new AdminClient(config, fetchImpl)
        : new AdminClient(config);

      try {
        const result = await client.audit(type as AuditObjectType, id);
        ctx.body = result;
      } catch (err) {
        if (err instanceof AdminApiError) {
          // Recoverable (5xx / transport) → 503; non-recoverable 4xx → echo status.
          ctx.status = err.recoverable ? 503 : (err.status || 500);
          ctx.body = { error: err.message, recoverable: err.recoverable };
          return;
        }
        throw err;
      }
    },
  };
}
