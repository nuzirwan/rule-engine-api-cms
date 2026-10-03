// flow.ts lifecycle (design §5.3) — the beforePublish hook that enforces the
// publish-blocking gate regardless of entry point. Strapi's Draft & Publish
// "publish" action is the editorial trigger; this hook runs the engine publish
// sequence so an entry can never reach the engine without passing validation.
//
// The hook delegates to the same runPublishSequence used by the controller, so
// the gate holds identically from either path. A PublishBlockedError aborts the
// Strapi publish with a surfaced message.

import { AdminClient, resolveAdminConfig } from '../services/admin-client';
import { PublishBlockedError, runPublishSequence } from '../services/validation';
import { TransformError } from '../services/publish-transform';
import {
  collectReferences,
  toConnectionEntry,
  toFlowEntry,
  toJdmEntry,
} from '../controllers/publish';

/**
 * Build the beforePublish lifecycle for the Flow content type. The returned
 * object is registered via register()/bootstrap() so the gate runs on every
 * Draft & Publish "publish".
 */
export function flowPublishLifecycle(strapi: any) {
  return {
    async beforePublish(event: any) {
      const documentId: string = event?.params?.documentId ?? event?.params?.where?.documentId;
      if (!documentId) return;

      const flowDoc = await strapi
        .documents('api::flow.flow')
        .findOne({ documentId, populate: ['fixtures', 'environment'] });
      if (!flowDoc) return;

      const { jdmIds, connectionKeys } = collectReferences(flowDoc.tree);

      const jdmDocs = jdmIds.length
        ? await strapi.documents('api::jdm.jdm').findMany({ filters: { jdmId: { $in: jdmIds } } })
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

      try {
        await runPublishSequence(client, {
          flow: toFlowEntry(flowDoc),
          jdms: jdmDocs.map(toJdmEntry),
          connections: connDocs.map(toConnectionEntry),
        });
      } catch (err) {
        if (err instanceof TransformError || err instanceof PublishBlockedError) {
          // Abort the Strapi publish; Strapi surfaces the thrown message.
          throw new Error(`publish blocked: ${(err as Error).message}`);
        }
        throw err;
      }
    },
  };
}
