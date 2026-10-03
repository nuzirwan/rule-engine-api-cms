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
  connectionToEnginePayload,
  jdmToEnginePayload,
} from './publish-transform';
import {
  collectReferences,
  toConnectionEntry,
  toFlowEntry,
  toJdmEntry,
} from '../controllers/publish';
import type { EngineConnectionDef } from '../../../../../../../types/engine';

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
