// Shared TS types for the engine config shapes + the FLAT admin wire bodies.
//
// Every type here is reconciled to the AS-BUILT merged Go structs (commit
// df547be), verified by reading internal/httpapi/admin_handlers.go,
// admin_validate.go and internal/connect/connect.go. The hard contract:
//   * All request bodies are FLAT — `env` is a top-level sibling, there is NO
//     `flow`/`connection` wrapper. The Go handlers decode with
//     DisallowUnknownFields, so a wrapper or a stray field is a hard 400.
//   * `resilience` on the wire is the TAG-LESS connect.ResiliencePolicy, so it
//     serializes Go-cased with `Timeout` as a NANOSECOND integer and
//     `Retry.MaxAttempts` (design §3.3.2).
//
// See design §4.2/§5.1/§5.2 and context.json "key_patterns".

// ----------------------------------------------------------------------------
// Engine Node tree (Slice A) — the recursive flow tree shape (seed.json).
// ----------------------------------------------------------------------------

/**
 * A recursive engine flow node. `spec` is intentionally an open map because its
 * keys are node-type specific AND mixed-case: an `action` node's `operation`
 * sub-object uses capitalized Go-struct keys (`Kind`/`Payload`/`Required`),
 * while every other spec key is camelCase (design §4.2, finding 10).
 */
export interface EngineNode {
  id: string;
  type: string;
  spec: Record<string, unknown>;
  children?: EngineNode[];
}

/**
 * TriggerInput declares input extraction and validation for a flow trigger.
 * Used in the trigger node's spec.input field.
 */
export interface TriggerInput {
  params?: string[];
  query?: string[];
  headers?: string[];
  body?: boolean;
  /** JSON Schema for input validation. When present, all extracted input is validated. */
  schema?: unknown;
}

// ----------------------------------------------------------------------------
// Resilience — Go-cased, nanosecond Timeout (tag-less connect.ResiliencePolicy)
// ----------------------------------------------------------------------------

/** The retry sub-struct as the engine marshals it (Go-cased). */
export interface EngineRetry {
  MaxAttempts: number;
  // The engine also emits BaseBackoff/MaxBackoff on the GET response (zero when
  // unset); the CMS only sends MaxAttempts and only reads MaxAttempts back.
  BaseBackoff?: number;
  MaxBackoff?: number;
}

/** The breaker sub-struct the engine may emit on a GET response (read-only). */
export interface EngineBreaker {
  FailureThreshold?: number;
  FailureRatio?: number;
  OpenTimeout?: number;
}

/**
 * resilience as it rides on the wire — Go-cased, `Timeout` in NANOSECONDS.
 * The CMS SENDS `{ Timeout, Retry: { MaxAttempts } }`; it may READ BACK the
 * fuller shape with Breaker/backoff fields from GET /admin/connections.
 */
export interface EngineResilience {
  Timeout: number; // nanoseconds
  Retry: EngineRetry;
  Breaker?: EngineBreaker;
}

// ----------------------------------------------------------------------------
// Flow fixtures (slice-d §5) — mocks/expect omitted when absent (finding 9)
// ----------------------------------------------------------------------------

export interface EngineFixture {
  name: string;
  input: unknown;
  mocks?: unknown;
  expect?: unknown;
}

// ----------------------------------------------------------------------------
// FLAT admin request bodies (verbatim keys from the Go request structs)
// ----------------------------------------------------------------------------

/** POST /admin/flows body — createFlowRequest. Flat; no `flow` wrapper. */
export interface CreateFlowRequest {
  env: string;
  flowId: string;
  method: string;
  path: string;
  tree: EngineNode;
  fixtures?: EngineFixture[];
  note?: string;
}

/** POST /admin/flows/{id}/publish and /rollback body — setActiveRequest. */
export interface SetActiveRequest {
  env: string;
  version: number;
}

/** POST /admin/jdms body — createJDMRequest. `version` always 0 (auto-assign). */
export interface CreateJDMRequest {
  env: string;
  jdmId: string;
  doc: unknown;
  version: number;
}

/** POST /admin/connections body — createConnectionRequest. Flat; secretRef/secretRefs. */
export interface CreateConnectionRequest {
  env: string;
  key: string;
  type: string;
  settings: Record<string, unknown>;
  secretRef?: string; // legacy single secret ref (backward compat)
  secretRefs?: Record<string, string>; // multi-secret refs keyed by role (e.g. {"password": "env:X", "apiKey": "env:Y"})
  resilience: EngineResilience;
}

/** POST /admin/flows/validate body — STORED mode only (flat siblings). */
export interface ValidateFlowRequest {
  env: string;
  flowId: string;
  version: number;
}

/** POST /admin/flows/dry-run body (optional preview; not a gate). */
export interface DryRunFlowRequest {
  env: string;
  flowId: string;
  version?: number;
  input: unknown;
  mocks?: unknown;
}

// ----------------------------------------------------------------------------
// Candidate-mode validate/dry-run (inline flow tree, no storage required)
// ----------------------------------------------------------------------------

/** Candidate flow body for inline validate/dry-run (no flowId+version lookup). */
export interface CandidateFlowBody {
  flowId: string;
  method: string;
  path: string;
  tree: EngineNode;
  fixtures?: EngineFixture[];
}

/** POST /admin/flows/validate body — CANDIDATE mode (inline flow object). */
export interface ValidateFlowCandidateRequest {
  env: string;
  flow: CandidateFlowBody;
}

/** Input shape for dry-run: mirrors the data-plane edge request structure. */
export interface DryRunInput {
  method?: string;
  path?: string;
  params?: Record<string, unknown>;
  body?: Record<string, unknown>;
  headers?: Record<string, string>;
}

/** POST /admin/flows/dry-run body — CANDIDATE mode (inline flow + input). */
export interface DryRunFlowCandidateRequest {
  env: string;
  flow: CandidateFlowBody;
  input: DryRunInput;
  mocks?: Record<string, Record<string, unknown>>;
}

/** 200 from POST /admin/flows/dry-run. */
export interface DryRunFlowResponse {
  trace: unknown[];
  response: unknown;
  errors: string[];
}

// ----------------------------------------------------------------------------
// Admin response bodies (keys as the handlers emit them)
// ----------------------------------------------------------------------------

/** 201 from POST /admin/flows. */
export interface CreateFlowResponse {
  flowId: string;
  version: number;
  validated: boolean;
}

/** 201 from POST /admin/jdms. */
export interface CreateJDMResponse {
  jdmId: string;
  version: number;
}

/** 201 from POST /admin/connections. */
export interface CreateConnectionResponse {
  key: string;
  version: number;
}

/** 200 from POST /admin/flows/{id}/publish and /rollback. */
export interface SetActiveResponse {
  flowId: string;
  activeVersion: number;
  action: string;
}

/** A single fixture result inside a validate response. */
export interface ValidateFixtureResult {
  name?: string;
  passed: boolean;
  diff?: unknown;
}

/** 200 from POST /admin/flows/validate. `ok:false` means BLOCKED. */
export interface ValidateFlowResponse {
  ok: boolean;
  structural: unknown[];
  fixtures: ValidateFixtureResult[];
}

/**
 * One entry in the GET /admin/connections response list. Top-level keys are
 * camelCase (explicit handler map); the nested `resilience` is Go-cased ns
 * (tag-less struct) — finding 5.
 */
export interface EngineConnectionDef {
  key: string;
  type: string;
  settings: Record<string, unknown>;
  secretRef: string; // legacy single secret ref
  secretRefs?: Record<string, string> | null; // multi-secret refs (may be null)
  resilience: EngineResilience;
}

/** 200 from GET /admin/connections. */
export interface ListConnectionsResponse {
  connections: EngineConnectionDef[];
}

// ----------------------------------------------------------------------------
// Audit trail — GET /admin/audit/{type}/{id}
// ----------------------------------------------------------------------------

/** Valid object types for the audit endpoint. */
export type AuditObjectType = 'flow' | 'jdm' | 'connection';

/** One entry in the audit trail (newest-first). */
export interface AuditEntry {
  action: string;
  fromVersion: number | null;
  toVersion: number | null;
  actor: string;
  /** ISO 8601 timestamp. */
  at: string;
  reason: string;
}

/** 200 from GET /admin/audit/{type}/{id}. */
export interface AuditTrailResponse {
  objectType: AuditObjectType;
  objectId: string;
  entries: AuditEntry[];
}

// ----------------------------------------------------------------------------
// Read endpoints for sync — GET /admin/flows, /admin/jdms, /admin/connections/:key
// ----------------------------------------------------------------------------

/** A flow summary returned by GET /admin/flows. */
export interface FlowSummary {
  id: string;
  method: string;
  path: string;
  activeVersion: number | null;
  updatedAt: string;
}

/** 200 from GET /admin/flows. */
export interface ListFlowsResponse {
  flows: FlowSummary[];
}

/** 200 from GET /admin/flows/{id} — the flow definition with its tree. */
export interface GetFlowResponse {
  flowId: string;
  version: number;
  method: string;
  path: string;
  tree: EngineNode;
  fixtures?: EngineFixture[];
}

/** A version summary for a flow. */
export interface VersionSummary {
  version: number;
  validated: boolean;
  createdAt: string;
  createdBy: string;
}

/** 200 from GET /admin/flows/{id}/versions. */
export interface ListFlowVersionsResponse {
  flowId: string;
  versions: VersionSummary[];
}

/** A JDM summary returned by GET /admin/jdms. */
export interface JDMSummary {
  id: string;
  updatedAt: string;
}

/** 200 from GET /admin/jdms. */
export interface ListJdmsResponse {
  jdms: JDMSummary[];
}

/** 200 from GET /admin/jdms/{id}. */
export interface GetJdmResponse {
  jdmId: string;
  version: number;
  doc: unknown;
}

/** 200 from GET /admin/connections/{key}. Same shape as EngineConnectionDef. */
export interface GetConnectionResponse {
  key: string;
  type: string;
  settings: Record<string, unknown>;
  secretRef: string; // legacy single secret ref
  secretRefs?: Record<string, string> | null; // multi-secret refs (may be null)
  resilience: EngineResilience;
}

// ----------------------------------------------------------------------------
// Webhook types (FEAT-004) — admin API for webhook configuration
// ----------------------------------------------------------------------------

/** Webhook provider types supported by the engine. */
export type WebhookProvider = 'stripe' | 'github' | 'generic';

/** A single mapping entry: extracts sourceJsonPath and writes to targetContextPath. */
export interface WebhookMappingEntry {
  sourceJsonPath: string;
  targetContextPath: string;
}

/** A single filter entry: extracts jsonPath and checks if value is in allowedValues. */
export interface WebhookFilterEntry {
  jsonPath: string;
  allowedValues: string[];
}

/** The webhook configuration shape as stored/transmitted to the engine. */
export interface WebhookConfig {
  id: string;
  name: string;
  secretRef?: string;
  provider: WebhookProvider;
  flowId: string;
  mapping?: WebhookMappingEntry[];
  filter?: WebhookFilterEntry[];
}

/**
 * POST /admin/webhooks body — createWebhookRequest.
 *
 * The engine Go struct (webhook_admin.go `createWebhookRequest`) uses a
 * top-level `id` (NOT `webhookId`) and MAP-shaped `mapping`/`filter`:
 *   mapping  map[string]string   — { [sourceJsonPath]: targetContextPath }
 *   filter   map[string][]string — { [jsonPath]: allowedValues }
 */
export interface CreateWebhookRequest {
  env: string;
  id: string;
  name: string;
  secretRef?: string;
  provider: WebhookProvider;
  flowId: string;
  mapping?: Record<string, string>;
  filter?: Record<string, string[]>;
}

/** 201 from POST /admin/webhooks. */
export interface CreateWebhookResponse {
  id: string;
  version: number;
}

/**
 * POST /admin/webhooks/{id}/publish body — publishWebhookRequest.
 *
 * The engine Go struct (webhook_admin.go `publishWebhookRequest`) carries ONLY
 * `version`; the handler decodes with DisallowUnknownFields, so a top-level
 * `env` (unlike flow publish's setActiveRequest) is a hard 400. Do NOT add env.
 */
export interface PublishWebhookRequest {
  version: number;
}

/** 200 from POST /admin/webhooks/{id}/publish. */
export interface PublishWebhookResponse {
  webhookId: string;
  activeVersion: number;
  action: string;
}

/** A webhook summary returned by GET /admin/webhooks. */
export interface WebhookSummary {
  id: string;
  name: string;
  provider: WebhookProvider;
  flowId: string;
  updatedAt: string;
}

/** 200 from GET /admin/webhooks. */
export interface ListWebhooksResponse {
  webhooks: WebhookSummary[];
}

/** 200 from GET /admin/webhooks/{id}. */
export interface GetWebhookResponse {
  webhookId: string;
  name: string;
  secretRef?: string;
  provider: WebhookProvider;
  flowId: string;
  mapping?: WebhookMappingEntry[];
  filter?: WebhookFilterEntry[];
  version: number;
}

/** PUT /admin/webhooks/{id} body — updateWebhookRequest. */
export interface UpdateWebhookRequest {
  env: string;
  name?: string;
  secretRef?: string;
  provider?: WebhookProvider;
  flowId?: string;
  mapping?: WebhookMappingEntry[];
  filter?: WebhookFilterEntry[];
}

/** A single entry in the webhook invocation log. */
export interface WebhookLogEntry {
  timestamp: string;
  provider: WebhookProvider;
  eventType: string;
  status: 'received' | 'filtered' | 'processed' | 'failed';
  flowId?: string;
  error?: string;
  durationMs?: number;
}

/** 200 from GET /admin/webhooks/{id}/logs. */
export interface WebhookLogsResponse {
  webhookId: string;
  logs: WebhookLogEntry[];
  total: number;
}

// ============================================================================
// Group Types (FEAT-001) — worker group scaling policy + connection pool
// ============================================================================
//
// Reconciled to the engine Go structs (config/group.go, admin_groups.go):
//   * PUT /admin/groups/{id} body is FLAT (`env` top-level); id comes from the
//     URL path, NOT the body.
//   * `scaling` is NESTED (config.ScalingConfig): the field is `mode` (NOT
//     scalingMode) and durations (`scaleDownDelay`/`startupTimeout`) are Go
//     duration STRINGS (e.g. "5m", "30s"), not integers.

/** Scaling mode — mirrors engine config.ScalingMode. */
export type ScalingMode = 'static' | 'dynamic' | 'ephemeral';

/** Kubernetes resource requests/limits — mirrors engine config.Resources. */
export interface EngineResourceLimits {
  cpuRequest?: string;
  cpuLimit?: string;
  memoryRequest?: string;
  memoryLimit?: string;
}

/** Nested scaling config — mirrors engine config.ScalingConfig. */
export interface ScalingConfig {
  mode: ScalingMode;
  minReplicas: number;
  maxReplicas: number;
  /** Go duration string, e.g. "5m". */
  scaleDownDelay?: string;
  /** Go duration string, e.g. "30s". */
  startupTimeout?: string;
  resources?: EngineResourceLimits;
}

/** PUT /admin/groups/{id} body — putGroupRequest. Flat; id from the URL path. */
export interface GroupPutRequest {
  env?: string;
  name: string;
  description?: string;
  connections?: string[];
  scaling: ScalingConfig;
  enabled?: boolean;
  reason?: string;
}

/** The full group shape returned by GET /admin/groups/{id} — mirrors config.Group. */
export interface EngineGroup {
  id: string;
  name: string;
  description?: string;
  version: number;
  connections: string[];
  scaling: ScalingConfig;
  enabled: boolean;
}

/** A group summary row returned by GET /admin/groups — mirrors config.GroupSummary. */
export interface GroupSummary {
  id: string;
  name: string;
  enabled: boolean;
  version: number;
  updatedAt: string;
}

/** 200 from GET /admin/groups. */
export interface ListGroupsResponse {
  groups: GroupSummary[];
}

// ============================================================================
// Schedule Types (TASK-008 - Scheduled Triggers)
// ============================================================================

/** The schedule configuration shape as stored/transmitted to the engine. */
export interface ScheduleConfig {
  id: string;
  name: string;
  schedule: string; // cron expr, @alias, or @every
  timezone: string; // IANA timezone
  flowId: string;
  input?: Record<string, unknown>;
  enabled: boolean;
}

/** POST /admin/schedules body — createScheduleRequest. */
export interface CreateScheduleRequest {
  id: string;
  name: string;
  schedule: string;
  timezone?: string;
  flowId: string;
  input?: Record<string, unknown>;
  enabled?: boolean;
}

/** PUT /admin/schedules/{id} body — updateScheduleRequest. */
export interface UpdateScheduleRequest {
  name?: string;
  schedule?: string;
  timezone?: string;
  flowId?: string;
  input?: Record<string, unknown>;
  enabled?: boolean;
}

/** A schedule summary as returned by the engine. */
export interface ScheduleSummary {
  id: string;
  name: string;
  schedule: string;
  timezone: string;
  flowId: string;
  input: Record<string, unknown>;
  enabled: boolean;
  lastRun?: string;
  nextRun?: string;
  createdAt: string;
  updatedAt: string;
}

/** 200 from GET /admin/schedules. */
export interface ListSchedulesResponse {
  schedules: ScheduleSummary[];
}

/** 200/201 from GET/POST /admin/schedules/{id}. */
export interface GetScheduleResponse extends ScheduleSummary {}

/** A single entry in the schedule run history. */
export interface ScheduleRunEntry {
  id: number;
  scheduleId: string;
  scheduledTime: string;
  startedAt: string;
  finishedAt?: string;
  durationMs?: number;
  status: 'success' | 'failure' | 'timeout' | 'skipped';
  error?: string;
  response?: unknown;
}

/** 200 from GET /admin/schedules/{id}/runs. */
export interface ListScheduleRunsResponse {
  runs: ScheduleRunEntry[];
}

/** 200 from POST /admin/schedules/{id}/run (manual trigger). */
export interface TriggerScheduleRunResponse {
  runId: number;
  status: 'success' | 'failure' | 'timeout';
  durationMs: number;
  error?: string;
}

// ============================================================================
// Connector Schema Types (multi-secret refs)
// ============================================================================

/** A single secret field definition for a connector's schema. */
export interface SecretField {
  name: string;
  required: boolean;
  label: string;
}

/** Schema for a connector's secrets (null means dynamic, e.g. REST). */
export interface ConnectorSchema {
  secrets: SecretField[] | null;
}

/** 200 from GET /admin/connectors/schema. */
export interface ConnectorSchemaResponse {
  connectors: Record<string, ConnectorSchema>;
}

// ============================================================================
// Test Connection Types (test-connection feature)
// ============================================================================

/** POST /admin/connections/test body — testConnectionRequest. */
export interface TestConnectionRequest {
  type: string;
  settings: Record<string, unknown>;
  secret?: string; // legacy single secret (backward compat)
  secrets?: Record<string, string>; // multi-secret map keyed by role (e.g. {"password": "p", "username": "u"})
}

/** 200 from POST /admin/connections/test. */
export interface TestConnectionResponse {
  success: boolean;
  message?: string;
  error?: string;
}
