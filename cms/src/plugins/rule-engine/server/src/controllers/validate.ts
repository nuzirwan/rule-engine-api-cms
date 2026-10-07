// validate.ts — controller for candidate-mode validate and dry-run. Provides endpoints:
//   * POST /rule-engine/validate — validates a candidate flow tree (inline, before save)
//   * POST /rule-engine/dry-run — runs a candidate flow tree with test input (before save)
//
// Both endpoints accept the flow tree inline (not from DB) so the editor can
// validate/test the current canvas state before any save.
//
// The engine's /admin/flows/validate supports candidate mode via the `flow` field.
// The engine's /admin/flows/dry-run only supports stored mode (flowId+version), so
// for dry-run we use the validate endpoint's mock registry + trace collector pattern
// by constructing a candidate validate request with mocks to simulate execution.
//
// Error classification follows the §5.5 table (same as other controllers):
//   * 5xx / transport (recoverable) → 503
//   * 4xx (non-recoverable) → echo the engine status
//   * Config missing → 503 (env not provisioned)

import { AdminApiError, AdminClient, resolveAdminConfig } from '../services/admin-client';
import type {
  EngineNode,
  EngineFixture,
  ValidateFlowResponse,
} from '../../../../../../types/engine';

/** The request body for POST /rule-engine/validate. */
interface ValidateCandidateRequest {
  /** Flow ID (for identification in results, not for lookup). */
  flowId?: string;
  /** HTTP method (GET/POST/etc) for the candidate flow. */
  method: string;
  /** Route path for the candidate flow. */
  path: string;
  /** The engine node tree to validate. */
  tree: EngineNode;
  /** Optional fixtures to run during validation. */
  fixtures?: EngineFixture[];
}

/** The request body for POST /rule-engine/dry-run. */
interface DryRunCandidateRequest {
  /** Flow ID (for identification in results, not for lookup). */
  flowId?: string;
  /** HTTP method for the candidate flow. */
  method: string;
  /** Route path for the candidate flow. */
  path: string;
  /** The engine node tree to dry-run. */
  tree: EngineNode;
  /** The test input to run the flow against. */
  input: {
    method?: string;
    path?: string;
    params?: Record<string, unknown>;
    body?: Record<string, unknown>;
    headers?: Record<string, string>;
  };
  /** Optional mocks for external calls (connections). */
  mocks?: Record<string, Record<string, unknown>>;
}

/** The response from POST /rule-engine/dry-run. */
interface DryRunCandidateResponse {
  /** The execution trace (steps executed). */
  trace: unknown[];
  /** The final response/context from the flow. */
  response: unknown;
  /** Any errors encountered during execution. */
  errors: string[];
}

/**
 * Validate controller factory. The optional `fetchImpl` parameter is a test seam:
 * it is passed through to AdminClient so tests can inject the nock-compatible
 * http-fetch shim.
 */
export default function validateController(
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
     * POST /rule-engine/validate — validate a candidate flow tree.
     *
     * Body: { flowId?, method, path, tree, fixtures? }
     * Returns: { ok, structural[], fixtures[] }
     */
    async validate(ctx: { request: { body: ValidateCandidateRequest }; status: number; body: unknown }) {
      const { flowId, method, path, tree, fixtures } = ctx.request.body;

      // Basic validation
      if (!tree || typeof tree !== 'object') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "tree" field' };
        return;
      }
      if (!method || typeof method !== 'string') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "method" field' };
        return;
      }
      if (!path || typeof path !== 'string') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "path" field' };
        return;
      }

      const client = createClient(ctx);
      if (!client) return;

      try {
        // Use candidate-mode validate: send the flow inline via the `flow` field
        const response = await client.validateFlowCandidate({
          flowId: flowId || 'candidate',
          method,
          path,
          tree,
          fixtures,
        });
        ctx.body = response;
      } catch (err) {
        handleApiError(ctx, err);
      }
    },

    /**
     * POST /rule-engine/dry-run — dry-run a candidate flow tree.
     *
     * Body: { flowId?, method, path, tree, input, mocks? }
     * Returns: { trace[], response, errors[] }
     */
    async dryRun(ctx: { request: { body: DryRunCandidateRequest }; status: number; body: unknown }) {
      const { flowId, method, path, tree, input, mocks } = ctx.request.body;

      // Basic validation
      if (!tree || typeof tree !== 'object') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "tree" field' };
        return;
      }
      if (!method || typeof method !== 'string') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "method" field' };
        return;
      }
      if (!path || typeof path !== 'string') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "path" field' };
        return;
      }
      if (!input || typeof input !== 'object') {
        ctx.status = 400;
        ctx.body = { error: 'Missing or invalid "input" field' };
        return;
      }

      const client = createClient(ctx);
      if (!client) return;

      try {
        // Use candidate-mode dry-run: send the flow inline
        const response = await client.dryRunFlowCandidate({
          flowId: flowId || 'candidate',
          method,
          path,
          tree,
          input,
          mocks,
        }) as DryRunCandidateResponse;
        ctx.body = response;
      } catch (err) {
        handleApiError(ctx, err);
      }
    },
  };
}
