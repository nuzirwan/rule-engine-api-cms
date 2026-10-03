// publish.ts (design §5.4) — the admin-only controller behind
// POST /rule-engine/flows/:id/publish. It resolves the Flow entry, its target
// Environment, and the referenced JDMs/Connections from Strapi, then runs the
// ordered publish sequence (validation.ts) and persists the write-backs.
//
// All engine integration goes through the admin HTTP API (admin-client.ts); the
// controller never touches the engine config store directly. secretRef only.

import { AdminClient, resolveAdminConfig } from '../services/admin-client';
import {
  PublishBlockedError,
  PublishWriteBack,
  runPublishSequence,
} from '../services/validation';
import { TransformError } from '../services/publish-transform';
import {
  ConnectionEntry,
  FlowEntry,
  JdmEntry,
} from '../services/publish-transform';

interface StrapiLike {
  documents: (uid: string) => {
    findOne: (args: { documentId: string; populate?: unknown }) => Promise<any>;
    findMany: (args: { filters?: unknown; populate?: unknown }) => Promise<any[]>;
    update: (args: { documentId: string; data: unknown }) => Promise<any>;
  };
  log?: { info: (m: string, ...a: unknown[]) => void; warn: (m: string, ...a: unknown[]) => void; error: (m: string, ...a: unknown[]) => void };
}

/**
 * Collect the referenced JDM ids (decision/condition nodes) and connection keys
 * (action nodes) from a flow tree. Walks the recursive Node tree.
 */
export function collectReferences(tree: unknown): { jdmIds: string[]; connectionKeys: string[] } {
  const jdmIds = new Set<string>();
  const connectionKeys = new Set<string>();

  const walk = (node: any) => {
    if (!node || typeof node !== 'object') return;
    const spec = node.spec ?? {};
    if (typeof spec.jdmId === 'string') jdmIds.add(spec.jdmId);
    if (typeof spec.connection === 'string') connectionKeys.add(spec.connection);
    if (Array.isArray(node.children)) node.children.forEach(walk);
  };
  walk(tree);

  return { jdmIds: [...jdmIds], connectionKeys: [...connectionKeys] };
}

/** Map a loaded Flow document to the transform's FlowEntry shape. */
export function toFlowEntry(doc: any): FlowEntry {
  return {
    flowId: doc.flowId,
    method: doc.method,
    path: doc.path,
    tree: doc.tree,
    fixtures: doc.fixtures ?? null,
    note: doc.note ?? null,
  };
}

export function toJdmEntry(doc: any): JdmEntry {
  return { jdmId: doc.jdmId, doc: doc.doc };
}

/**
 * Map a PublishBlockedError to the HTTP status surfaced to the Strapi admin UI,
 * honoring the §5.5 table. The engine-side status (when present) wins so the
 * author sees the right remedy copy:
 *   409 route collision (createFlow) / 422 publish un-validated / 404 version
 *   not found / 403 under-privileged are echoed as-is; a transport/5xx block is
 *   surfaced as 503 (retry); a bare validation-failure block (no status) is 422.
 */
export function statusForBlocked(err: PublishBlockedError): number {
  if (err.status === 409) return 409; // create-time route collision (author-fixable)
  if (err.status === 404) return 404; // version not found (re-create and retry)
  if (err.status === 403) return 403; // operator token under-privileged
  if (err.status === 422) return 422; // publish un-validated (defense-in-depth)
  // A validation failure (ok:false) carries NO HTTP status: the call succeeded
  // (200) but the author's flow is wrong. That is publish-blocking => 422, never
  // a "retry, engine down" 503.
  if (err.status === undefined) return 422;
  // Everything with a 5xx/transport status is retry-recoverable => 503.
  if (err.recoverable || err.status >= 500 || err.status === 0) return 503;
  // Any other author-fixable 4xx surfaces as 422 (publish-blocking).
  return 422;
}

export function toConnectionEntry(doc: any): ConnectionEntry {
  return {
    key: doc.key,
    type: doc.type,
    settings: doc.settings ?? null,
    secretRef: doc.secretRef ?? null,
    resilience: doc.resilience ?? null,
  };
}

/**
 * The Strapi controller factory. Kept thin: it loads entries, builds the client,
 * delegates to runPublishSequence, persists write-backs, and maps errors to HTTP
 * responses using the §5.5 status semantics.
 */
export default ({ strapi }: { strapi: StrapiLike }) => ({
  async publish(ctx: any) {
    const documentId: string = ctx.params.id;

    const flowDoc = await strapi
      .documents('api::flow.flow')
      .findOne({ documentId, populate: ['fixtures', 'environment'] });
    if (!flowDoc) {
      ctx.notFound('flow not found');
      return;
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
          .findMany({ filters: { key: { $in: connectionKeys } }, populate: ['settings', 'resilience'] })
      : [];

    const config = resolveAdminConfig({
      adminApiBaseUrl: flowDoc.environment?.adminApiBaseUrl ?? null,
      operatorTokenRef: flowDoc.environment?.operatorTokenRef ?? null,
      payloadEnv: flowDoc.environment?.payloadEnv ?? null,
    });
    const client = new AdminClient(config);

    let writeBack: PublishWriteBack;
    try {
      writeBack = await runPublishSequence(client, {
        flow: toFlowEntry(flowDoc),
        jdms: jdmDocs.map(toJdmEntry),
        connections: connDocs.map(toConnectionEntry),
      });
    } catch (err) {
      await persistWriteBack(strapi, documentId, (err as any).writeBack);
      if (err instanceof TransformError) {
        ctx.badRequest(err.message);
        return;
      }
      if (err instanceof PublishBlockedError) {
        await strapi
          .documents('api::flow.flow')
          .update({ documentId, data: { lastPublishStatus: 'failed' } });
        ctx.body = { blocked: true, stage: err.stage, recoverable: err.recoverable, message: err.message };
        ctx.status = statusForBlocked(err);
        return;
      }
      throw err;
    }

    await persistWriteBack(strapi, documentId, writeBack, jdmDocs);
    ctx.body = { published: true, activeVersion: writeBack.activeVersion };
  },
});

async function persistWriteBack(
  strapi: StrapiLike,
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

  // Write back each JDM's engine-assigned version onto its own document.
  for (const jdmDoc of jdmDocs) {
    const v = writeBack.jdmEngineVersions?.[jdmDoc.jdmId];
    if (v != null) {
      await strapi
        .documents('api::jdm.jdm')
        .update({ documentId: jdmDoc.documentId, data: { engineVersion: v } });
    }
  }
}
