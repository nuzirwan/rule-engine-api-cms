// validation.ts (design §5.3/§5.4) — the ordered, publish-blocking sequence that
// orchestrates the admin client + the pure transforms. Kept free of Strapi I/O
// (it takes an AdminClient + plain entries + a write-back callback) so the whole
// gate is unit-testable with nock (§6.3).
//
// Ordered sequence (§5.4):
//   1. listConnections -> createConnection for only missing/changed keys (reconcile)
//   2. createJdm per referenced jdm (create == activate), write back engineVersion
//   3. createFlow (validated=false), write back engineVersion = N
//   4. validateFlow STORED {env,flowId,version:N} — transport/5xx OR ok:false OR 404 => BLOCK
//   5. on ok:true, publishFlow {env,version:N}
//
// The transform's secret-denylist throw aborts BEFORE any admin call (step 1
// builds payloads first).

import type {
  CreateConnectionRequest,
  EngineConnectionDef,
  ValidateFlowResponse,
} from '../../../../../../types/engine';
import { AdminApiError, AdminClient } from './admin-client';
import {
  ConnectionEntry,
  FlowEntry,
  JdmEntry,
  connectionToEnginePayload,
  flowToEnginePayload,
  jdmToEnginePayload,
} from './publish-transform';

const NS_TO_MS = 1_000_000;

/** CMS bookkeeping the publish writes back onto the Flow entry. */
export type PublishStatus = 'none' | 'validated' | 'published' | 'failed';

export interface PublishWriteBack {
  flowEngineVersion?: number;
  jdmEngineVersions?: Record<string, number>;
  activeVersion?: number;
  lastPublishStatus?: PublishStatus;
  lastValidation?: ValidateFlowResponse | null;
}

export interface PublishInput {
  flow: FlowEntry;
  jdms: JdmEntry[];
  connections: ConnectionEntry[];
}

export class PublishBlockedError extends Error {
  readonly stage: string;
  readonly recoverable: boolean;
  readonly status?: number;

  constructor(message: string, stage: string, recoverable: boolean, status?: number) {
    super(message);
    this.name = 'PublishBlockedError';
    this.stage = stage;
    this.recoverable = recoverable;
    this.status = status;
  }
}

// ----------------------------------------------------------------------------
// Reconcile helpers (§5.4 step 1) — avoid spurious connection re-creation.
// ----------------------------------------------------------------------------

/**
 * Decide whether an authored connection payload meaningfully differs from the
 * engine's current def. Returns true => (re)create. Deliberately conservative:
 * compares ONLY the keys the CMS authored (filling the same documented driver
 * defaults on both sides), normalizes resilience ns->ms, and errs toward
 * re-create when the comparison is inconclusive (§5.4).
 */
export function connectionNeedsCreate(
  authored: CreateConnectionRequest,
  current: EngineConnectionDef | undefined
): boolean {
  if (!current) return true;

  // Resilience: engine Timeout is ns; compare ns directly (authored was already
  // converted to ns by the transform). MaxAttempts compared directly.
  const authTimeoutNs = authored.resilience?.Timeout ?? 0;
  const curTimeoutNs = current.resilience?.Timeout ?? 0;
  if (authTimeoutNs !== curTimeoutNs) return true;

  const authMax = authored.resilience?.Retry?.MaxAttempts ?? 1;
  const curMax = current.resilience?.Retry?.MaxAttempts ?? 1;
  if (authMax !== curMax) return true;

  if (authored.type !== current.type) return true;
  if ((authored.secretRef ?? '') !== (current.secretRef ?? '')) return true;

  // Settings: compare only the keys the CMS authored against the response. The
  // response may carry driver-defaulted / reordered keys the author never set;
  // those are ignored so a default-only difference does not force a re-create.
  const authSettings = authored.settings ?? {};
  const curSettings = (current.settings ?? {}) as Record<string, unknown>;
  for (const [key, value] of Object.entries(authSettings)) {
    if (!deepEqual(value, curSettings[key])) return true;
  }

  return false;
}

/** Normalize an engine ns Timeout to ms (used when surfacing/reconciling). */
export function timeoutNsToMs(ns: number): number {
  return Math.round(ns / NS_TO_MS);
}

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b) return false;
  if (a === null || b === null) return a === b;
  if (typeof a !== 'object') return false;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    return a.every((v, i) => deepEqual(v, b[i]));
  }
  const ao = a as Record<string, unknown>;
  const bo = b as Record<string, unknown>;
  const ak = Object.keys(ao);
  const bk = Object.keys(bo);
  if (ak.length !== bk.length) return false;
  return ak.every((k) => deepEqual(ao[k], bo[k]));
}

// ----------------------------------------------------------------------------
// The ordered publish sequence (§5.4).
// ----------------------------------------------------------------------------

/**
 * Run the full publish sequence against the engine admin API. Mutates nothing in
 * Strapi directly: it returns the write-backs the caller (controller/lifecycle)
 * persists. Throws PublishBlockedError on any blocking gate so a flow can NEVER
 * be published with ok:false, an unreachable validator, a 404, or a secret.
 */
export async function runPublishSequence(
  client: AdminClient,
  input: PublishInput
): Promise<PublishWriteBack> {
  const writeBack: PublishWriteBack = { jdmEngineVersions: {} };

  // Build all connection payloads FIRST so a secret-denylist throw aborts BEFORE
  // any admin HTTP call is made (§5.3 / §6.3 "secret" gate: zero admin calls).
  const connPayloads = input.connections.map((c) => connectionToEnginePayload(c, ''));

  // --- step 1: reconcile connections ---------------------------------------
  let present: EngineConnectionDef[];
  try {
    const listed = await client.listConnections();
    present = listed.connections ?? [];
  } catch (err) {
    writeBack.lastPublishStatus = 'failed';
    throw toBlocked(err, 'listConnections');
  }
  const byKey = new Map(present.map((d) => [d.key, d]));

  for (const payload of connPayloads) {
    if (connectionNeedsCreate(payload, byKey.get(payload.key))) {
      try {
        // Strip the transform's env (""); the client re-stamps its own env.
        const { env: _env, ...rest } = payload;
        await client.createConnection(rest);
      } catch (err) {
        writeBack.lastPublishStatus = 'failed';
        throw toBlocked(err, 'createConnection');
      }
    }
  }

  // --- step 2: create (==activate) each referenced JDM ---------------------
  for (const jdm of input.jdms) {
    const payload = jdmToEnginePayload(jdm, '');
    try {
      const { env: _env, ...rest } = payload;
      const res = await client.createJdm(rest);
      writeBack.jdmEngineVersions![jdm.jdmId] = res.version;
    } catch (err) {
      writeBack.lastPublishStatus = 'failed';
      throw toBlocked(err, 'createJdm');
    }
  }

  // --- step 3: create the flow version (validated=false) -------------------
  const flowPayload = flowToEnginePayload(input.flow, '');
  let version: number;
  try {
    const { env: _env, ...rest } = flowPayload;
    const res = await client.createFlow(rest);
    version = res.version;
    writeBack.flowEngineVersion = version;
  } catch (err) {
    writeBack.lastPublishStatus = 'failed';
    throw toBlocked(err, 'createFlow');
  }

  // --- step 4: STORED-mode validate; BLOCK on transport/5xx, ok:false, 404 --
  let validation: ValidateFlowResponse;
  try {
    validation = await client.validateFlow(input.flow.flowId, version);
  } catch (err) {
    writeBack.lastPublishStatus = 'failed';
    if (err instanceof AdminApiError && err.status === 404) {
      throw new PublishBlockedError(
        'created version not found; re-create and retry',
        'validateFlow',
        false,
        404
      );
    }
    throw toBlocked(err, 'validateFlow');
  }

  writeBack.lastValidation = validation;
  if (!validation.ok) {
    writeBack.lastPublishStatus = 'failed';
    throw new PublishBlockedError(
      'flow validation failed; fix the flow and retry',
      'validateFlow',
      true
    );
  }

  // --- step 5: publish ------------------------------------------------------
  try {
    const res = await client.publishFlow(input.flow.flowId, version);
    writeBack.activeVersion = res.activeVersion;
  } catch (err) {
    writeBack.lastPublishStatus = 'failed';
    throw toBlocked(err, 'publishFlow');
  }

  writeBack.lastPublishStatus = 'published';
  writeBack.lastValidation = null;
  return writeBack;
}

function toBlocked(err: unknown, stage: string): PublishBlockedError {
  if (err instanceof AdminApiError) {
    return new PublishBlockedError(err.message, stage, err.recoverable, err.status);
  }
  if (err instanceof PublishBlockedError) return err;
  return new PublishBlockedError((err as Error).message, stage, false);
}
