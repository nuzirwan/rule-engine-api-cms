// publish-core.ts — the single implementation of "push a published Flow (and its
// referenced JDMs + connections) to the engine admin HTTP API". Both entry points
// call this:
//   * the Document Service middleware on api::flow.flow `publish` (server/index.ts)
//     — fires on Strapi 5's native Draft & Publish "Publish" action;
//   * the explicit admin controller POST /rule-engine/flows/:id/publish.
//
// Strapi 5 note: the publish interception MUST be a Document Service middleware,
// NOT a `strapi.db.lifecycles.subscribe({ beforePublish })` DB hook — the latter
// is a v4 pattern that the v5 Document Service publish action does not invoke.
//
// All engine integration goes through admin-client.ts (HTTP, secretRef only); the
// CMS never touches the engine config store directly. The CMS is the active
// pusher; the engine is the passive receiver.

import { AdminApiError, AdminClient, resolveAdminConfig } from './admin-client';
import {
  PublishWriteBack,
  connectionNeedsCreate,
  runPublishSequence,
} from './validation';
import {
  GroupEntry,
  ScheduleEntry,
  WebhookEntry,
  connectionToEnginePayload,
  groupToEnginePayload,
  jdmToEnginePayload,
  scheduleToEnginePayload,
  webhookToEnginePayload,
} from './publish-transform';
import {
  collectReferences,
  toConnectionEntry,
  toFlowEntry,
  toJdmEntry,
} from '../controllers/publish';
import type { EngineConnectionDef } from '../../../../../../types/engine';

export interface PublishCoreResult {
  writeBack: PublishWriteBack;
  jdmDocs: any[];
}

/**
 * Load the Flow document, its referenced JDMs/connections, and its target
 * Environment, then run the ordered publish sequence against the engine admin
 * API. Returns the write-back (engine versions + validation) and the loaded JDM
 * docs so the caller can persist per-JDM engineVersion. Throws TransformError /
 * PublishBlockedError / AdminApiError on failure (the caller maps these).
 *
 * It does NOT persist write-backs — the caller owns Strapi writes, since the
 * middleware and the controller persist them slightly differently.
 */
export async function runFlowPublish(
  strapi: any,
  documentId: string
): Promise<PublishCoreResult> {
  const flowDoc = await strapi
    .documents('api::flow.flow')
    .findOne({ documentId, populate: ['fixtures', 'environment'] });
  if (!flowDoc) {
    throw new Error(`flow document ${documentId} not found`);
  }

  const { jdmIds, connectionKeys } = collectReferences(flowDoc.tree);

  const jdmDocs = jdmIds.length
    ? await strapi
        .documents('api::jdm.jdm')
        .findMany({ filters: { jdmId: { $in: jdmIds } } })
    : [];
  const connDocs = connectionKeys.length
    ? await strapi
        .documents('api::connection.connection')
        .findMany({
          filters: { key: { $in: connectionKeys } },
          populate: ['settings', 'resilience'],
        })
    : [];

  const config = resolveAdminConfig({
    adminApiBaseUrl: flowDoc.environment?.adminApiBaseUrl ?? null,
    operatorTokenRef: flowDoc.environment?.operatorTokenRef ?? null,
    payloadEnv: flowDoc.environment?.payloadEnv ?? null,
  });
  const client = new AdminClient(config);

  const writeBack = await runPublishSequence(client, {
    flow: toFlowEntry(flowDoc),
    jdms: jdmDocs.map(toJdmEntry),
    connections: connDocs.map(toConnectionEntry),
  });

  return { writeBack, jdmDocs };
}

/** Persist engine write-backs onto the Flow doc and each referenced JDM doc. */
export async function persistFlowWriteBack(
  strapi: any,
  documentId: string,
  writeBack: PublishWriteBack | undefined,
  jdmDocs: any[] = []
): Promise<void> {
  if (!writeBack) return;

  const data: Record<string, unknown> = {};
  if (writeBack.flowEngineVersion != null) data.engineVersion = writeBack.flowEngineVersion;
  if (writeBack.lastPublishStatus) data.lastPublishStatus = writeBack.lastPublishStatus;
  if (writeBack.lastValidation !== undefined) data.lastValidation = writeBack.lastValidation;

  if (Object.keys(data).length) {
    await strapi.documents('api::flow.flow').update({ documentId, data });
  }

  for (const jdmDoc of jdmDocs) {
    const v = writeBack.jdmEngineVersions?.[jdmDoc.jdmId];
    if (v != null) {
      await strapi
        .documents('api::jdm.jdm')
        .update({ documentId: jdmDoc.documentId, data: { engineVersion: v } });
    }
  }
}

// ----------------------------------------------------------------------------
// Standalone JDM / Connection publish.
//
// JDMs and connections are first-class, independently-versioned engine objects
// (own tables, own /admin/* endpoints). Publishing one on its own pushes just
// that object to the engine — it no longer has to ride a Flow publish. This
// closes the gap where publishing a Jdm/Connection entry silently did nothing.
// ----------------------------------------------------------------------------

/** Build an AdminClient from an entry's optional `environment` relation (env-var fallback). */
function clientForEntry(doc: any): AdminClient {
  const config = resolveAdminConfig({
    adminApiBaseUrl: doc.environment?.adminApiBaseUrl ?? null,
    operatorTokenRef: doc.environment?.operatorTokenRef ?? null,
    payloadEnv: doc.environment?.payloadEnv ?? null,
  });
  return new AdminClient(config);
}

/**
 * Push a single published JDM to the engine via POST /admin/jdms (create ==
 * activate). Returns the engine-assigned version. Throws AdminApiError /
 * TransformError on failure (the caller maps/surfaces it).
 */
export async function runJdmPublish(
  strapi: any,
  documentId: string
): Promise<{ engineVersion: number }> {
  const jdmDoc = await strapi
    .documents('api::jdm.jdm')
    .findOne({ documentId, populate: ['environment'] });
  if (!jdmDoc) {
    throw new Error(`jdm document ${documentId} not found`);
  }

  const client = clientForEntry(jdmDoc);
  const payload = jdmToEnginePayload(toJdmEntry(jdmDoc), '');
  const { env: _env, ...rest } = payload;
  const res = await client.createJdm(rest);

  // Write back the engine-assigned version.
  await strapi
    .documents('api::jdm.jdm')
    .update({ documentId, data: { engineVersion: res.version } })
    .catch(() => {});

  return { engineVersion: res.version };
}

/**
 * Push a single published Connection to the engine. Reconciles against the
 * engine's current defs (listConnections) and only (re)creates when the authored
 * payload meaningfully differs — same conservative diff the flow sequence uses.
 * The transform throws a TransformError BEFORE any admin call if a secret value
 * is present in settings (secretRef only). Throws AdminApiError on HTTP failure.
 */
export async function runConnectionPublish(
  strapi: any,
  documentId: string
): Promise<{ created: boolean }> {
  const connDoc = await strapi
    .documents('api::connection.connection')
    .findOne({ documentId, populate: ['settings', 'resilience', 'environment'] });
  if (!connDoc) {
    throw new Error(`connection document ${documentId} not found`);
  }

  const client = clientForEntry(connDoc);

  // Build the payload FIRST so a secret-denylist throw aborts before any HTTP.
  const payload = connectionToEnginePayload(toConnectionEntry(connDoc), '');

  let present: EngineConnectionDef[] = [];
  try {
    const listed = await client.listConnections();
    present = listed.connections ?? [];
  } catch (err) {
    if (err instanceof AdminApiError) throw err;
    throw err;
  }
  const current = present.find((d) => d.key === payload.key);

  if (connectionNeedsCreate(payload, current)) {
    const { env: _env, ...rest } = payload;
    await client.createConnection(rest);
    return { created: true };
  }
  return { created: false };
}

// ----------------------------------------------------------------------------
// Webhook / Schedule / Group publish (FEAT-001).
//
// Webhook is a two-step create+publish like flows. Schedule is mutable CRUD
// (GET -> 404 ? create : update). Group is a single idempotent PUT upsert. Each
// loads the populated Strapi doc, builds the payload via a PURE transform (which
// may throw TransformError BEFORE any admin call), and writes back the sync
// status onto the CMS doc.
// ----------------------------------------------------------------------------

/** Map a loaded Webhook document to the transform's WebhookEntry shape. */
export function toWebhookEntry(doc: any): WebhookEntry {
  return {
    webhookId: doc.webhookId,
    name: doc.name,
    secretRef: doc.secretRef ?? null,
    provider: doc.provider,
    // flowId is a relation; its value is the related flow's flowId uid.
    flowId: typeof doc.flowId === 'object' && doc.flowId !== null ? doc.flowId.flowId : doc.flowId,
    mapping: doc.mapping ?? null,
    filter: doc.filter ?? null,
  };
}

/** Map a loaded Schedule document to the transform's ScheduleEntry shape. */
export function toScheduleEntry(doc: any): ScheduleEntry {
  return {
    scheduleId: doc.scheduleId,
    name: doc.name,
    schedule: doc.schedule,
    timezone: doc.timezone ?? null,
    flowId: typeof doc.flowId === 'object' && doc.flowId !== null ? doc.flowId.flowId : doc.flowId,
    input: doc.input ?? null,
    enabled: doc.enabled ?? null,
  };
}

/** Map a loaded Group document to the transform's GroupEntry shape. */
export function toGroupEntry(doc: any): GroupEntry {
  return {
    groupId: doc.groupId,
    name: doc.name,
    description: doc.description ?? null,
    enabled: doc.enabled ?? null,
    scalingMode: doc.scalingMode,
    minReplicas: doc.minReplicas,
    maxReplicas: doc.maxReplicas,
    scaleDownDelaySeconds: doc.scaleDownDelaySeconds ?? null,
    startupTimeoutSeconds: doc.startupTimeoutSeconds ?? null,
    resources: doc.resources ?? null,
    connections: Array.isArray(doc.connections)
      ? doc.connections.map((c: any) => ({ key: c.key }))
      : null,
  };
}

/**
 * Push a single published Webhook to the engine: POST /admin/webhooks (create,
 * returns {id,version}) then POST /admin/webhooks/{id}/publish to activate. On
 * success writes back {engineVersion, lastSyncStatus:'synced'}; on AdminApiError
 * writes {lastSyncStatus:'failed'} and rethrows. TransformError (empty flowId)
 * throws BEFORE any admin call.
 */
export async function runWebhookPublish(
  strapi: any,
  documentId: string
): Promise<{ engineVersion: number }> {
  const doc = await strapi
    .documents('api::webhook.webhook')
    .findOne({ documentId, populate: ['flowId', 'mapping', 'filter', 'environment'] });
  if (!doc) {
    throw new Error(`webhook document ${documentId} not found`);
  }

  // Build the payload FIRST so a TransformError aborts before any HTTP.
  const payload = webhookToEnginePayload(toWebhookEntry(doc));
  const client = clientForEntry(doc);

  try {
    const created = await client.createWebhook(payload);
    await client.publishWebhook(created.id, created.version);
    await strapi
      .documents('api::webhook.webhook')
      .update({
        documentId,
        data: { engineVersion: created.version, lastSyncStatus: 'synced' },
      })
      .catch(() => {});
    return { engineVersion: created.version };
  } catch (err) {
    if (err instanceof AdminApiError) {
      await strapi
        .documents('api::webhook.webhook')
        .update({ documentId, data: { lastSyncStatus: 'failed' } })
        .catch(() => {});
    }
    throw err;
  }
}

/**
 * Push a single Schedule to the engine. Schedules are mutable CRUD: GET the
 * engine schedule and, on 404, create; otherwise update. The transform validates
 * the cron + IANA timezone and throws BEFORE any admin call on a bad value. On
 * success writes back lastSyncStatus='synced' (+ lastRun/nextRun when returned);
 * on AdminApiError writes 'failed' and rethrows.
 */
export async function runSchedulePublish(
  strapi: any,
  documentId: string
): Promise<{ created: boolean }> {
  const doc = await strapi
    .documents('api::schedule.schedule')
    .findOne({ documentId, populate: ['flowId', 'environment'] });
  if (!doc) {
    throw new Error(`schedule document ${documentId} not found`);
  }

  // Build the payload FIRST so a bad cron/timezone aborts before any HTTP.
  const payload = scheduleToEnginePayload(toScheduleEntry(doc));
  const client = clientForEntry(doc);

  try {
    let created = false;
    let result: any;
    try {
      await client.getSchedule(payload.id);
    } catch (err) {
      if (err instanceof AdminApiError && err.status === 404) {
        result = await client.createSchedule(payload);
        created = true;
      } else {
        throw err;
      }
    }
    if (!created) {
      const { id: _id, ...update } = payload;
      result = await client.updateSchedule(payload.id, update);
    }

    const data: Record<string, unknown> = { lastSyncStatus: 'synced' };
    if (result?.lastRun != null) data.lastRun = result.lastRun;
    if (result?.nextRun != null) data.nextRun = result.nextRun;
    await strapi
      .documents('api::schedule.schedule')
      .update({ documentId, data })
      .catch(() => {});

    return { created };
  } catch (err) {
    if (err instanceof AdminApiError) {
      await strapi
        .documents('api::schedule.schedule')
        .update({ documentId, data: { lastSyncStatus: 'failed' } })
        .catch(() => {});
    }
    throw err;
  }
}

/**
 * Push a single Group to the engine via PUT /admin/groups/{id} (idempotent
 * upsert). The transform flattens CMS fields to the nested ScalingConfig,
 * converting seconds -> Go duration strings and connections -> string[] of keys,
 * and throws TransformError (maxReplicas<minReplicas / static minReplicas<1)
 * BEFORE any admin call.
 */
export async function runGroupPublish(
  strapi: any,
  documentId: string
): Promise<{ groupId: string }> {
  const doc = await strapi
    .documents('api::group.group')
    .findOne({ documentId, populate: ['connections', 'resources', 'environment'] });
  if (!doc) {
    throw new Error(`group document ${documentId} not found`);
  }

  // Build the payload FIRST so a validation TransformError aborts before any HTTP.
  const { groupId, body } = groupToEnginePayload(toGroupEntry(doc));
  const client = clientForEntry(doc);
  await client.upsertGroup(groupId, body);
  return { groupId };
}
