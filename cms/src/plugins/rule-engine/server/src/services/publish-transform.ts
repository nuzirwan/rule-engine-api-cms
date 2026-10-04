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
