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
  AuditObjectType,
  AuditTrailResponse,
  CandidateFlowBody,
  CreateConnectionRequest,
  CreateConnectionResponse,
  CreateFlowRequest,
  CreateFlowResponse,
  CreateJDMRequest,
  CreateJDMResponse,
  CreateScheduleRequest,
  CreateWebhookRequest,
  CreateWebhookResponse,
  DryRunFlowCandidateRequest,
  DryRunFlowRequest,
  DryRunFlowResponse,
  DryRunInput,
  EngineGroup,
  GetConnectionResponse,
  GetFlowResponse,
  GetJdmResponse,
  GetScheduleResponse,
  GetWebhookResponse,
  GroupPutRequest,
  ListConnectionsResponse,
  ListFlowsResponse,
  ListFlowVersionsResponse,
  ListGroupsResponse,
  ListJdmsResponse,
  ListScheduleRunsResponse,
  ListSchedulesResponse,
  ListWebhooksResponse,
  PublishWebhookRequest,
  PublishWebhookResponse,
  SetActiveResponse,
  TestConnectionRequest,
  TestConnectionResponse,
  TriggerScheduleRunResponse,
  UpdateScheduleRequest,
  UpdateWebhookRequest,
  ValidateFlowCandidateRequest,
  ValidateFlowRequest,
  ValidateFlowResponse,
  WebhookLogsResponse,
} from '../../../../../../types/engine';

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

  /**
   * POST /admin/flows/validate — CANDIDATE mode: body `{ env, flow: { flowId, method, path, tree, fixtures? } }`.
   * Validates an inline flow tree without requiring it to be stored first.
   */
  validateFlowCandidate(flow: Omit<CandidateFlowBody, 'env'>): Promise<ValidateFlowResponse> {
    const body: ValidateFlowCandidateRequest = { env: this.env, flow };
    return this.request<ValidateFlowResponse>('POST', '/admin/flows/validate', body);
  }

  /**
   * POST /admin/flows/dry-run — CANDIDATE mode: body `{ env, flow: { ... }, input, mocks? }`.
   * Runs an inline flow tree with test input, writes suppressed. Returns trace and response.
   */
  dryRunFlowCandidate(req: {
    flowId: string;
    method: string;
    path: string;
    tree: CandidateFlowBody['tree'];
    input: DryRunInput;
    mocks?: Record<string, Record<string, unknown>>;
  }): Promise<DryRunFlowResponse> {
    const body: DryRunFlowCandidateRequest = {
      env: this.env,
      flow: {
        flowId: req.flowId,
        method: req.method,
        path: req.path,
        tree: req.tree,
      },
      input: req.input,
      mocks: req.mocks,
    };
    return this.request<DryRunFlowResponse>('POST', '/admin/flows/dry-run', body);
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

  /**
   * POST /admin/connections/test — test a connection with ephemeral credentials.
   * The secret is passed in the request body and is never stored.
   */
  testConnection(req: Omit<TestConnectionRequest, 'env'>): Promise<TestConnectionResponse> {
    return this.request<TestConnectionResponse>('POST', '/admin/connections/test', req);
  }

  /** POST /admin/flows/dry-run — trace preview, writes suppressed (not a gate). */
  dryRunFlow(req: Omit<DryRunFlowRequest, 'env'>): Promise<unknown> {
    const body: DryRunFlowRequest = { env: this.env, ...req };
    return this.request<unknown>('POST', '/admin/flows/dry-run', body);
  }

  /** GET /admin/audit/{type}/{id} — read audit trail for a flow, jdm, or connection. */
  audit(type: AuditObjectType, id: string): Promise<AuditTrailResponse> {
    return this.request<AuditTrailResponse>(
      'GET',
      `/admin/audit/${encodeURIComponent(type)}/${encodeURIComponent(id)}`
    );
  }

  // --- read endpoints for sync (FEAT-002) ----------------------------------

  /** GET /admin/flows — list all flows with their active version info. */
  listFlows(): Promise<ListFlowsResponse> {
    return this.request<ListFlowsResponse>('GET', '/admin/flows');
  }

  /** GET /admin/flows/{id} — get the active (or latest) version of a flow. */
  getFlow(id: string): Promise<GetFlowResponse> {
    return this.request<GetFlowResponse>(
      'GET',
      `/admin/flows/${encodeURIComponent(id)}`
    );
  }

  /** GET /admin/flows/{id}/versions — list all versions for a flow. */
  listFlowVersions(id: string): Promise<ListFlowVersionsResponse> {
    return this.request<ListFlowVersionsResponse>(
      'GET',
      `/admin/flows/${encodeURIComponent(id)}/versions`
    );
  }

  /** GET /admin/jdms — list all JDMs. */
  listJdms(): Promise<ListJdmsResponse> {
    return this.request<ListJdmsResponse>('GET', '/admin/jdms');
  }

  /** GET /admin/jdms/{id} — get the active version of a JDM. */
  getJdm(id: string): Promise<GetJdmResponse> {
    return this.request<GetJdmResponse>(
      'GET',
      `/admin/jdms/${encodeURIComponent(id)}`
    );
  }

  /** GET /admin/connections/{key} — get a single connection definition. */
  getConnection(key: string): Promise<GetConnectionResponse> {
    return this.request<GetConnectionResponse>(
      'GET',
      `/admin/connections/${encodeURIComponent(key)}`
    );
  }

  // --- webhook endpoints (FEAT-004) ----------------------------------------

  /** GET /admin/webhooks — list all webhooks. */
  listWebhooks(): Promise<ListWebhooksResponse> {
    return this.request<ListWebhooksResponse>('GET', '/admin/webhooks');
  }

  /** GET /admin/webhooks/{id} — get a single webhook configuration. */
  getWebhook(id: string): Promise<GetWebhookResponse> {
    return this.request<GetWebhookResponse>(
      'GET',
      `/admin/webhooks/${encodeURIComponent(id)}`
    );
  }

  /** POST /admin/webhooks — create a new webhook (returns {id,version}); does NOT activate. */
  createWebhook(
    webhook: Omit<CreateWebhookRequest, 'env'>
  ): Promise<CreateWebhookResponse> {
    const body: CreateWebhookRequest = { env: this.env, ...webhook };
    return this.request<CreateWebhookResponse>('POST', '/admin/webhooks', body);
  }

  /** POST /admin/webhooks/{id}/publish — activate a webhook version (two-step like flows). */
  publishWebhook(id: string, version: number): Promise<PublishWebhookResponse> {
    // Engine publishWebhookRequest accepts ONLY `version` (DisallowUnknownFields);
    // sending `env` here (unlike flow publish) is a hard 400.
    const body: PublishWebhookRequest = { version };
    return this.request<PublishWebhookResponse>(
      'POST',
      `/admin/webhooks/${encodeURIComponent(id)}/publish`,
      body
    );
  }

  /** PUT /admin/webhooks/{id} — update an existing webhook. */
  updateWebhook(
    id: string,
    update: Omit<UpdateWebhookRequest, 'env'>
  ): Promise<GetWebhookResponse> {
    const body: UpdateWebhookRequest = { env: this.env, ...update };
    return this.request<GetWebhookResponse>(
      'PUT',
      `/admin/webhooks/${encodeURIComponent(id)}`,
      body
    );
  }

  /** DELETE /admin/webhooks/{id} — delete a webhook. */
  deleteWebhook(id: string): Promise<void> {
    return this.request<void>(
      'DELETE',
      `/admin/webhooks/${encodeURIComponent(id)}`
    );
  }

  /** GET /admin/webhooks/{id}/logs — get webhook invocation logs. */
  getWebhookLogs(
    id: string,
    opts?: { limit?: number; offset?: number }
  ): Promise<WebhookLogsResponse> {
    const params = new URLSearchParams();
    if (opts?.limit !== undefined) params.set('limit', String(opts.limit));
    if (opts?.offset !== undefined) params.set('offset', String(opts.offset));
    const qs = params.toString();
    return this.request<WebhookLogsResponse>(
      'GET',
      `/admin/webhooks/${encodeURIComponent(id)}/logs${qs ? `?${qs}` : ''}`
    );
  }

  // --- schedule endpoints (TASK-008) ----------------------------------------

  /** GET /admin/schedules — list all schedules. */
  listSchedules(): Promise<ListSchedulesResponse> {
    return this.request<ListSchedulesResponse>('GET', '/admin/schedules');
  }

  /** GET /admin/schedules/{id} — get a single schedule configuration. */
  getSchedule(id: string): Promise<GetScheduleResponse> {
    return this.request<GetScheduleResponse>(
      'GET',
      `/admin/schedules/${encodeURIComponent(id)}`
    );
  }

  /** POST /admin/schedules — create a new schedule. */
  createSchedule(
    schedule: CreateScheduleRequest
  ): Promise<GetScheduleResponse> {
    return this.request<GetScheduleResponse>('POST', '/admin/schedules', schedule);
  }

  /** PUT /admin/schedules/{id} — update an existing schedule. */
  updateSchedule(
    id: string,
    update: UpdateScheduleRequest
  ): Promise<GetScheduleResponse> {
    return this.request<GetScheduleResponse>(
      'PUT',
      `/admin/schedules/${encodeURIComponent(id)}`,
      update
    );
  }

  /** DELETE /admin/schedules/{id} — delete a schedule. */
  deleteSchedule(id: string): Promise<void> {
    return this.request<void>(
      'DELETE',
      `/admin/schedules/${encodeURIComponent(id)}`
    );
  }

  /** POST /admin/schedules/{id}/run — manually trigger a schedule run. */
  triggerScheduleRun(id: string): Promise<TriggerScheduleRunResponse> {
    return this.request<TriggerScheduleRunResponse>(
      'POST',
      `/admin/schedules/${encodeURIComponent(id)}/run`
    );
  }

  /** GET /admin/schedules/{id}/runs — get schedule execution history. */
  getScheduleRuns(
    id: string,
    opts?: { limit?: number }
  ): Promise<ListScheduleRunsResponse> {
    const params = new URLSearchParams();
    if (opts?.limit !== undefined) params.set('limit', String(opts.limit));
    const qs = params.toString();
    return this.request<ListScheduleRunsResponse>(
      'GET',
      `/admin/schedules/${encodeURIComponent(id)}/runs${qs ? `?${qs}` : ''}`
    );
  }

  // --- group endpoints (FEAT-001) ------------------------------------------

  /** GET /admin/groups — list all groups (summaries, no scaling). */
  listGroups(): Promise<ListGroupsResponse> {
    return this.request<ListGroupsResponse>('GET', '/admin/groups');
  }

  /** GET /admin/groups/{id} — get the full group (nested scaling, connections[]). */
  getGroup(id: string): Promise<EngineGroup> {
    return this.request<EngineGroup>(
      'GET',
      `/admin/groups/${encodeURIComponent(id)}`
    );
  }

  /** PUT /admin/groups/{id} — create or update a group. id from the URL path; env stamped into the body. */
  upsertGroup(
    groupId: string,
    group: Omit<GroupPutRequest, 'env'>
  ): Promise<EngineGroup> {
    const body: GroupPutRequest = { env: this.env, ...group };
    return this.request<EngineGroup>(
      'PUT',
      `/admin/groups/${encodeURIComponent(groupId)}`,
      body
    );
  }

  /** DELETE /admin/groups/{id} — delete a group. */
  deleteGroup(id: string): Promise<void> {
    return this.request<void>(
      'DELETE',
      `/admin/groups/${encodeURIComponent(id)}`
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
