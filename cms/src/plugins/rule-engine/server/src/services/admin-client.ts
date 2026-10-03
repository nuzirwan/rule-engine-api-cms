// admin-client.ts (design §5.1) — a thin, typed native-fetch wrapper over the
// engine admin HTTP API (/admin/*). Every request:
//   * carries `Authorization: Bearer <token>` (token read AT CALL TIME from env
//     or the per-Environment override; never persisted, never logged);
//   * sends FLAT bodies with `env` = payloadEnv (default "") as a top-level
//     sibling — no `flow`/`connection` wrapper;
//   * treats a 200 with `ok:false` on validate as BLOCKED, and maps non-2xx
//     HTTP statuses to typed AdminApiError per the §5.5 table.
//
// Stored-mode validate sends exactly `{ env, flowId, version }` (§5.1.1).

import type {
  CreateConnectionRequest,
  CreateConnectionResponse,
  CreateFlowRequest,
  CreateFlowResponse,
  CreateJDMRequest,
  CreateJDMResponse,
  DryRunFlowRequest,
  ListConnectionsResponse,
  SetActiveResponse,
  ValidateFlowRequest,
  ValidateFlowResponse,
} from '../../../../../../../types/engine';

/** Resolved per-call configuration (base URL + token + payload env). */
export interface AdminClientConfig {
  baseUrl: string;
  token: string;
  /** The value sent in the body's `env` field (Environment.payloadEnv, default ""). */
  payloadEnv?: string;
}

/** A typed error carrying the HTTP status so callers can key recovery off §5.5. */
export class AdminApiError extends Error {
  readonly status: number;
  readonly body: unknown;
  /** false for author-fixable / mis-provisioned errors (4xx except transport). */
  readonly recoverable: boolean;

  constructor(message: string, status: number, body: unknown, recoverable: boolean) {
    super(message);
    this.name = 'AdminApiError';
    this.status = status;
    this.body = body;
    this.recoverable = recoverable;
  }
}

type FetchLike = typeof fetch;

export class AdminClient {
  private readonly baseUrl: string;
  private readonly token: string;
  private readonly env: string;
  private readonly fetchImpl: FetchLike;

  constructor(config: AdminClientConfig, fetchImpl: FetchLike = fetch) {
    this.baseUrl = config.baseUrl.replace(/\/+$/, '');
    this.token = config.token;
    this.env = config.payloadEnv ?? '';
    this.fetchImpl = fetchImpl;
  }

  // --- low-level request helpers -------------------------------------------

  private headers(): Record<string, string> {
    // Authorization: Bearer on EVERY call. The token is never logged.
    return {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${this.token}`,
    };
  }

  private static recoverableFor(status: number): boolean {
    // 5xx and transport are retry-recoverable; 4xx are author-fixable /
    // mis-provisioned and NOT retried (§5.5). 404 on stored-mode validate and
    // 403 under-privileged are explicitly non-recoverable.
    return status >= 500;
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    let res: Response;
    try {
      res = await this.fetchImpl(`${this.baseUrl}${path}`, {
        method,
        headers: this.headers(),
        body: body === undefined ? undefined : JSON.stringify(body),
      });
    } catch (err) {
      // Transport failure (DNS/connection) — recoverable.
      throw new AdminApiError(
        `admin request ${method} ${path} failed: ${(err as Error).message}`,
        0,
        null,
        true
      );
    }

    const text = await res.text();
    let parsed: unknown = null;
    if (text) {
      try {
        parsed = JSON.parse(text);
      } catch {
        parsed = text;
      }
    }

    if (!res.ok) {
      throw new AdminApiError(
        `admin request ${method} ${path} returned ${res.status}`,
        res.status,
        parsed,
        AdminClient.recoverableFor(res.status)
      );
    }

    return parsed as T;
  }

  // --- typed methods (map to the as-built /admin/* contract) ---------------

  /** POST /admin/flows — create a flow version (validated=false); does NOT publish. */
  createFlow(flow: Omit<CreateFlowRequest, 'env'>): Promise<CreateFlowResponse> {
    const body: CreateFlowRequest = { env: this.env, ...flow };
    return this.request<CreateFlowResponse>('POST', '/admin/flows', body);
  }

  /** POST /admin/flows/validate — STORED mode: body `{ env, flowId, version }` only. */
  validateFlow(flowId: string, version: number): Promise<ValidateFlowResponse> {
    const body: ValidateFlowRequest = { env: this.env, flowId, version };
    return this.request<ValidateFlowResponse>('POST', '/admin/flows/validate', body);
  }

  /** POST /admin/flows/{id}/publish — advance the active pointer. 422 if un-validated. */
  publishFlow(flowId: string, version: number): Promise<SetActiveResponse> {
    const body = { env: this.env, version };
    return this.request<SetActiveResponse>(
      'POST',
      `/admin/flows/${encodeURIComponent(flowId)}/publish`,
      body
    );
  }

  /** POST /admin/flows/{id}/rollback — move the active pointer back. */
  rollbackFlow(flowId: string, version: number): Promise<SetActiveResponse> {
    const body = { env: this.env, version };
    return this.request<SetActiveResponse>(
      'POST',
      `/admin/flows/${encodeURIComponent(flowId)}/rollback`,
      body
    );
  }

  /** POST /admin/jdms — create AND activate a JDM version in one txn. */
  createJdm(jdm: Omit<CreateJDMRequest, 'env'>): Promise<CreateJDMResponse> {
    const body: CreateJDMRequest = { env: this.env, ...jdm };
    return this.request<CreateJDMResponse>('POST', '/admin/jdms', body);
  }

  /** POST /admin/connections — register+activate a connection def (secretRef only). */
  createConnection(
    conn: Omit<CreateConnectionRequest, 'env'>
  ): Promise<CreateConnectionResponse> {
    const body: CreateConnectionRequest = { env: this.env, ...conn };
    return this.request<CreateConnectionResponse>('POST', '/admin/connections', body);
  }

  /** GET /admin/connections — list active connection defs (Go-cased ns resilience). */
  listConnections(): Promise<ListConnectionsResponse> {
    return this.request<ListConnectionsResponse>('GET', '/admin/connections');
  }

  /** POST /admin/flows/dry-run — trace preview, writes suppressed (not a gate). */
  dryRunFlow(req: Omit<DryRunFlowRequest, 'env'>): Promise<unknown> {
    const body: DryRunFlowRequest = { env: this.env, ...req };
    return this.request<unknown>('POST', '/admin/flows/dry-run', body);
  }

  /** GET /admin/audit/{type}/{id} — read audit trail. No v1 publish-path caller. */
  audit(type: 'flow' | 'jdm' | 'connection', id: string): Promise<unknown> {
    return this.request<unknown>(
      'GET',
      `/admin/audit/${encodeURIComponent(type)}/${encodeURIComponent(id)}`
    );
  }
}

/**
 * Resolve the admin-client config from the global env defaults overlaid with an
 * optional per-Environment override. Token is read HERE (at call time) and
 * never persisted. Throws if base URL or token cannot be resolved.
 */
export function resolveAdminConfig(
  override?: {
    adminApiBaseUrl?: string | null;
    operatorTokenRef?: string | null;
    payloadEnv?: string | null;
  },
  envSource: NodeJS.ProcessEnv = process.env
): AdminClientConfig {
  const baseUrl = override?.adminApiBaseUrl || envSource.ADMIN_API_BASE_URL;
  if (!baseUrl) {
    throw new Error('ADMIN_API_BASE_URL (or Environment.adminApiBaseUrl) is required');
  }

  // operatorTokenRef names an env var holding the plaintext bearer; fall back to
  // the global ADMIN_API_OPERATOR_TOKEN. Resolved at call time, never stored.
  const token =
    (override?.operatorTokenRef ? envSource[override.operatorTokenRef] : undefined) ||
    envSource.ADMIN_API_OPERATOR_TOKEN;
  if (!token) {
    throw new Error(
      'ADMIN_API_OPERATOR_TOKEN (or the env var named by Environment.operatorTokenRef) is required'
    );
  }

  const payloadEnv = override?.payloadEnv ?? envSource.ADMIN_API_ENV ?? '';

  return { baseUrl, token, payloadEnv };
}
