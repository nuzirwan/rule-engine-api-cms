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

/** POST /admin/connections body — createConnectionRequest. Flat; secretRef only. */
export interface CreateConnectionRequest {
  env: string;
  key: string;
  type: string;
  settings: Record<string, unknown>;
  secretRef: string;
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
  secretRef: string;
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
