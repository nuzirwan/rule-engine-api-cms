// publish-transform.ts (design §5.2) — PURE functions that convert a CMS entry
// into the FLAT engine admin wire body. No I/O: directly unit-testable (§6.1).
//
// Hard contract (verified against the as-built Go structs, context.json):
//   * Every body is FLAT — `env` is a top-level sibling, NO `flow`/`connection`
//     wrapper, no stray top-level fields (DisallowUnknownFields => 400).
//   * jdm bodies ALWAYS carry `version: 0` (auto-assign).
//   * connection `resilience` is Go-cased ns (`{ Timeout:<ns>, Retry:{ MaxAttempts } }`):
//     authored `timeoutMs` (ms) -> `Timeout` (ns); `retry.maxAttempts` -> `Retry.MaxAttempts`.
//   * connectionToEnginePayload THROWS a TransformError if any settings key
//     matches the secret denylist (password, pwd, secret, token, apikey, dsn).

import type {
  CreateConnectionRequest,
  CreateFlowRequest,
  CreateJDMRequest,
  EngineFixture,
  EngineNode,
  EngineResilience,
  EngineResourceLimits,
  GroupPutRequest,
  ScalingConfig,
  ScalingMode,
  WebhookProvider,
} from '../../../../../../types/engine';

/** Case-insensitive secret denylist (design §3.3.1) — CMS superset of the engine edge guard. */
export const SECRET_DENYLIST = [
  'password',
  'pwd',
  'secret',
  'token',
  'apikey',
  'dsn',
] as const;

const DENIED = new Set<string>(SECRET_DENYLIST);

/** Thrown by connectionToEnginePayload when a secret value appears in settings. */
export class TransformError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'TransformError';
  }
}

// ----------------------------------------------------------------------------
// Loose CMS entry shapes — the transform reads only the fields it maps.
// ----------------------------------------------------------------------------

export interface FlowFixtureEntry {
  name: string;
  input: unknown;
  mocks?: unknown;
  expect?: unknown;
}

export interface FlowEntry {
  flowId: string;
  method: string;
  path: string;
  tree: EngineNode;
  fixtures?: FlowFixtureEntry[] | null;
  note?: string | null;
}

export interface JdmEntry {
  jdmId: string;
  doc: unknown;
}

export interface RetryEntry {
  maxAttempts?: number | null;
}

export interface ResilienceEntry {
  timeoutMs?: number | null;
  retry?: RetryEntry | null;
}

export interface ConnectionEntry {
  key: string;
  type: string;
  settings?: Record<string, unknown> | null;
  secretRef?: string | null;
  resilience?: ResilienceEntry | null;
}

const MS_TO_NS = 1_000_000;

/**
 * Walks a settings value (nested object/array) and returns the first key whose
 * name matches the denylist (case-insensitive), or null if clean. Matches on
 * the exact lower-cased key so `apiKey` -> `apikey` is caught while unrelated
 * keys like `apikeyId` are not.
 */
export function findDeniedSecretKey(settings: unknown): string | null {
  const walk = (value: unknown): string | null => {
    if (Array.isArray(value)) {
      for (const item of value) {
        const hit = walk(item);
        if (hit) return hit;
      }
      return null;
    }
    if (value !== null && typeof value === 'object') {
      for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
        if (DENIED.has(key.toLowerCase())) {
          return key;
        }
        const hit = walk(child);
        if (hit) return hit;
      }
    }
    return null;
  };
  return walk(settings);
}

/**
 * flowToEnginePayload — flat createFlowRequest. `tree` passes through (the
 * canvas already serialized the engine Node shape with mixed casing preserved,
 * §4.2); fixtures map to the wire shape OMITTING absent mocks/expect; note is
 * included only when present. No `flow` wrapper, no stray top-level fields.
 */
export function flowToEnginePayload(entry: FlowEntry, env: string): CreateFlowRequest {
  const body: CreateFlowRequest = {
    env,
    flowId: entry.flowId,
    method: entry.method,
    path: entry.path,
    tree: entry.tree,
  };

  if (entry.fixtures && entry.fixtures.length > 0) {
    body.fixtures = entry.fixtures.map((f): EngineFixture => {
      const fixture: EngineFixture = { name: f.name, input: f.input };
      if (f.mocks != null) fixture.mocks = f.mocks;
      if (f.expect != null) fixture.expect = f.expect;
      return fixture;
    });
  }

  if (entry.note != null && entry.note !== '') {
    body.note = entry.note;
  }

  return body;
}

/**
 * jdmToEnginePayload — flat createJDMRequest, ALWAYS `version: 0` so the engine
 * auto-assigns max+1 (finding 7). create == activate (one txn, finding 6).
 */
export function jdmToEnginePayload(entry: JdmEntry, env: string): CreateJDMRequest {
  return {
    env,
    jdmId: entry.jdmId,
    doc: entry.doc,
    version: 0,
  };
}

/**
 * Converts the author-facing resilience (ms + retry.maxAttempts) to the engine's
 * Go-cased ns shape: timeoutMs ms -> Timeout ns; retry.maxAttempts -> Retry.MaxAttempts.
 */
export function resilienceToEngine(resilience?: ResilienceEntry | null): EngineResilience {
  const timeoutMs = resilience?.timeoutMs ?? 0;
  const maxAttempts = resilience?.retry?.maxAttempts ?? 1;
  return {
    Timeout: timeoutMs * MS_TO_NS,
    Retry: { MaxAttempts: maxAttempts },
  };
}

/**
 * connectionToEnginePayload — flat createConnectionRequest. Passes discrete,
 * credential-free settings through, emits Go-cased ns resilience, and carries
 * secretRef only. THROWS a TransformError BEFORE returning if any settings key
 * matches the secret denylist (belt-and-suspenders over the content-type guard).
 */
export function connectionToEnginePayload(
  entry: ConnectionEntry,
  env: string
): CreateConnectionRequest {
  const settings = (entry.settings ?? {}) as Record<string, unknown>;

  const offending = findDeniedSecretKey(settings);
  if (offending) {
    throw new TransformError(
      `connection "${entry.key}" settings must not contain the secret key "${offending}"; ` +
        `use secretRef instead (denied keys, case-insensitive: ${SECRET_DENYLIST.join(', ')})`
    );
  }

  return {
    env,
    key: entry.key,
    type: entry.type,
    settings,
    secretRef: entry.secretRef ?? '',
    resilience: resilienceToEngine(entry.resilience),
  };
}

// ----------------------------------------------------------------------------
// Webhook (FEAT-001) — flat body with top-level `id` and MAP-shaped
// mapping/filter (engine createWebhookRequest: `id`, map[string]string,
// map[string][]string). The CMS stores mapping/filter as arrays of components;
// collapse them to maps keyed by the source path.
// ----------------------------------------------------------------------------

export interface WebhookMappingComponent {
  sourceJsonPath: string;
  targetContextPath: string;
}

export interface WebhookFilterComponent {
  jsonPath: string;
  allowedValues: string[];
}

export interface WebhookEntry {
  webhookId: string;
  name: string;
  secretRef?: string | null;
  provider: WebhookProvider;
  flowId: string;
  mapping?: WebhookMappingComponent[] | null;
  filter?: WebhookFilterComponent[] | null;
}

/** The engine create body MINUS `env` (the client stamps its own env). */
export interface WebhookEnginePayload {
  id: string;
  name: string;
  secretRef?: string;
  provider: WebhookProvider;
  flowId: string;
  mapping?: Record<string, string>;
  filter?: Record<string, string[]>;
}

/**
 * webhookToEnginePayload — collapses the CMS webhook into the engine create body.
 * `webhookId` -> `id`; `mapping[]` -> `{ [sourceJsonPath]: targetContextPath }`;
 * `filter[]` -> `{ [jsonPath]: allowedValues }`. Throws TransformError when
 * `flowId` is empty (the engine requires it).
 */
export function webhookToEnginePayload(entry: WebhookEntry): WebhookEnginePayload {
  if (!entry.flowId) {
    throw new TransformError(`webhook "${entry.webhookId}" has no flowId; link a flow before publishing`);
  }

  const payload: WebhookEnginePayload = {
    id: entry.webhookId,
    name: entry.name,
    provider: entry.provider,
    flowId: entry.flowId,
  };

  if (entry.secretRef != null && entry.secretRef !== '') {
    payload.secretRef = entry.secretRef;
  }

  if (entry.mapping && entry.mapping.length > 0) {
    const mapping: Record<string, string> = {};
    for (const m of entry.mapping) {
      mapping[m.sourceJsonPath] = m.targetContextPath;
    }
    payload.mapping = mapping;
  }

  if (entry.filter && entry.filter.length > 0) {
    const filter: Record<string, string[]> = {};
    for (const f of entry.filter) {
      filter[f.jsonPath] = f.allowedValues ?? [];
    }
    payload.filter = filter;
  }

  return payload;
}

// ----------------------------------------------------------------------------
// Schedule (FEAT-001) — mutable CRUD body (no publish step). Self-contained cron
// + IANA-timezone validation so a bad schedule is rejected BEFORE any admin call
// (parity with the engine's scheduler.ParseSchedule + time.LoadLocation).
// ----------------------------------------------------------------------------

/** Recognized `@`-alias schedule expressions (engine scheduler aliases). */
const CRON_ALIASES = new Set([
  '@hourly',
  '@daily',
  '@weekly',
  '@monthly',
  '@yearly',
  '@annually',
  '@midnight',
]);

// A standard 5-field cron entry: a star, a step (star slash n), a range a-b, a
// list a,b, a step on a range, or a bare number.
const CRON_FIELD = /^(\*|\d+)(-(\d+))?(\/\d+)?(,(\*|\d+)(-(\d+))?(\/\d+)?)*$/;

/**
 * isValidCron — self-contained validator matching the engine's accepted forms:
 *   * a 5-field cron (minute hour day-of-month month day-of-week), each field a
 *     `*`, number, range, list, or step;
 *   * an `@alias` (@hourly/@daily/@weekly/@monthly/@yearly/@annually/@midnight);
 *   * `@every <duration>` with a Go-style duration (e.g. `@every 1h30m`).
 */
export function isValidCron(expr: string): boolean {
  if (!expr) return false;
  const trimmed = expr.trim();
  if (!trimmed) return false;

  if (CRON_ALIASES.has(trimmed)) return true;

  if (trimmed.startsWith('@every ')) {
    const dur = trimmed.slice('@every '.length).trim();
    return isGoDuration(dur);
  }
  // Any other @-expression is unsupported.
  if (trimmed.startsWith('@')) return false;

  const fields = trimmed.split(/\s+/);
  if (fields.length !== 5) return false;
  return fields.every((f) => CRON_FIELD.test(f));
}

/** Matches a Go duration string like `30s`, `5m`, `1h30m`, `100ms`. */
const GO_DURATION = /^\d+(\.\d+)?(ns|us|µs|ms|s|m|h)([0-9.]+(ns|us|µs|ms|s|m|h))*$/;

function isGoDuration(d: string): boolean {
  if (!d) return false;
  return GO_DURATION.test(d);
}

/**
 * A basic IANA timezone validator (no new dependency): accepts `UTC`, and the
 * `Region/City` (optionally `Region/City/Sub`) form the engine's
 * time.LoadLocation resolves (e.g. `Asia/Jakarta`, `America/Argentina/Salta`).
 */
export function isValidTimezone(tz: string): boolean {
  if (!tz) return false;
  const t = tz.trim();
  if (t === 'UTC') return true;
  // Region/City, each segment a letter-led token allowing +,-,_ and digits.
  return /^[A-Za-z]+(?:[A-Za-z0-9+_-]*)(?:\/[A-Za-z0-9+_-]+){1,2}$/.test(t);
}

export interface ScheduleEntry {
  scheduleId: string;
  name: string;
  schedule: string;
  timezone?: string | null;
  flowId: string;
  input?: Record<string, unknown> | null;
  enabled?: boolean | null;
}

/** The engine create/update body for a schedule (no publish step). */
export interface ScheduleEnginePayload {
  id: string;
  name: string;
  schedule: string;
  timezone?: string;
  flowId: string;
  input?: Record<string, unknown>;
  enabled?: boolean;
}

/**
 * scheduleToEnginePayload — maps the CMS schedule to the engine body, validating
 * the cron expression and the IANA timezone first. Throws TransformError on an
 * empty flowId, an invalid cron, or a non-IANA timezone (so no admin call fires).
 */
export function scheduleToEnginePayload(entry: ScheduleEntry): ScheduleEnginePayload {
  if (!entry.flowId) {
    throw new TransformError(`schedule "${entry.scheduleId}" has no flowId; link a flow before publishing`);
  }
  if (!isValidCron(entry.schedule)) {
    throw new TransformError(
      `schedule "${entry.scheduleId}" has an invalid cron expression: "${entry.schedule}"`
    );
  }
  const timezone = entry.timezone == null || entry.timezone === '' ? 'UTC' : entry.timezone;
  if (!isValidTimezone(timezone)) {
    throw new TransformError(
      `schedule "${entry.scheduleId}" has an invalid IANA timezone: "${timezone}"`
    );
  }

  const payload: ScheduleEnginePayload = {
    id: entry.scheduleId,
    name: entry.name,
    schedule: entry.schedule,
    timezone,
    flowId: entry.flowId,
  };
  if (entry.input != null) payload.input = entry.input;
  if (entry.enabled != null) payload.enabled = entry.enabled;
  return payload;
}

// ----------------------------------------------------------------------------
// Group (FEAT-001) — flat CMS fields -> nested engine PUT body. The engine's
// `scaling` is NESTED and `mode` (not scalingMode); durations are Go duration
// STRINGS (`${seconds}s`). The content-type validates maxReplicas>=minReplicas
// and static=>minReplicas>0; mirror both here so a bad group never calls admin.
// ----------------------------------------------------------------------------

export interface GroupResourcesComponent extends EngineResourceLimits {
  id?: number;
  __component?: string;
}

export interface GroupConnectionRelation {
  key: string;
}

export interface GroupEntry {
  groupId: string;
  name: string;
  description?: string | null;
  enabled?: boolean | null;
  scalingMode: ScalingMode;
  minReplicas: number;
  maxReplicas: number;
  scaleDownDelaySeconds?: number | null;
  startupTimeoutSeconds?: number | null;
  resources?: GroupResourcesComponent | null;
  connections?: GroupConnectionRelation[] | null;
}

/** Strip Strapi's `id`/`__component` bookkeeping from a resources component. */
function stripResourceLimits(resources: GroupResourcesComponent): EngineResourceLimits {
  const out: EngineResourceLimits = {};
  if (resources.cpuRequest != null) out.cpuRequest = resources.cpuRequest;
  if (resources.cpuLimit != null) out.cpuLimit = resources.cpuLimit;
  if (resources.memoryRequest != null) out.memoryRequest = resources.memoryRequest;
  if (resources.memoryLimit != null) out.memoryLimit = resources.memoryLimit;
  return out;
}

/**
 * groupToEnginePayload — flattens the CMS group into `{ groupId, body }` for
 * PUT /admin/groups/{id}. Seconds -> Go duration strings (`${n}s`); the resources
 * component is id-stripped; the connections relation collapses to a string[] of
 * `.key`. Throws TransformError when maxReplicas<minReplicas or (static mode and
 * minReplicas<1) — parity with the engine's ValidateGroup.
 */
export function groupToEnginePayload(entry: GroupEntry): {
  groupId: string;
  body: Omit<GroupPutRequest, 'env'>;
} {
  if (entry.maxReplicas < entry.minReplicas) {
    throw new TransformError(
      `group "${entry.groupId}" has maxReplicas (${entry.maxReplicas}) < minReplicas (${entry.minReplicas})`
    );
  }
  if (entry.scalingMode === 'static' && entry.minReplicas < 1) {
    throw new TransformError(
      `group "${entry.groupId}" uses static scaling but minReplicas (${entry.minReplicas}) < 1`
    );
  }

  const scaling: ScalingConfig = {
    mode: entry.scalingMode,
    minReplicas: entry.minReplicas,
    maxReplicas: entry.maxReplicas,
  };
  if (entry.scaleDownDelaySeconds != null) {
    scaling.scaleDownDelay = `${entry.scaleDownDelaySeconds}s`;
  }
  if (entry.startupTimeoutSeconds != null) {
    scaling.startupTimeout = `${entry.startupTimeoutSeconds}s`;
  }
  if (entry.resources) {
    scaling.resources = stripResourceLimits(entry.resources);
  }

  const body: Omit<GroupPutRequest, 'env'> = {
    name: entry.name,
    scaling,
  };
  if (entry.description != null && entry.description !== '') {
    body.description = entry.description;
  }
  if (entry.enabled != null) {
    body.enabled = entry.enabled;
  }
  if (entry.connections && entry.connections.length > 0) {
    body.connections = entry.connections.map((c) => c.key);
  }

  return { groupId: entry.groupId, body };
}
