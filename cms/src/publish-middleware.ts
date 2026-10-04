// Publish interception for Flow / JDM / Connection, as a Strapi 5 Document
// Service middleware.
//
// STRAPI 5 FIX (proven live during planning): a `strapi.documents.use()`
// middleware registered from the LOCAL PLUGIN's register() is NOT attached to
// the content-manager publish path, so it silently no-ops (publish returns 200
// but nothing reaches the engine). The same middleware registered from the
// APP-LEVEL cms/src/index.ts bootstrap({ strapi }) DOES fire with
// context.action === 'publish'. This factory holds the handler so index.ts can
// register it via strapi.documents.use(buildPublishMiddleware(strapi)); the
// push logic itself is REUSED from the plugin's publish-core (DRY).

import {
  runFlowPublish,
  persistFlowWriteBack,
  runJdmPublish,
  runConnectionPublish,
} from './plugins/rule-engine/server/src/services/publish-core';
import { PublishBlockedError } from './plugins/rule-engine/server/src/services/validation';
import { TransformError } from './plugins/rule-engine/server/src/services/publish-transform';

const PUBLISHABLE = new Set([
  'api::flow.flow',
  'api::jdm.jdm',
  'api::connection.connection',
]);

export function buildPublishMiddleware(strapi: any) {
  return async (context: any, next: any) => {
    if (context?.action !== 'publish' || !PUBLISHABLE.has(context?.uid)) {
      return next();
    }

    const documentId: string =
      context?.params?.documentId ?? context?.params?.where?.documentId;
    if (!documentId) {
      return next();
    }

    // --- Flow: full ordered sequence (conn reconcile -> jdms -> flow ->
    //     validate -> publish), with write-backs. Strict gate. ---
    if (context.uid === 'api::flow.flow') {
      let result;
      try {
        result = await runFlowPublish(strapi, documentId);
      } catch (err) {
        if (err instanceof TransformError) {
          throw new Error(`publish blocked (transform): ${(err as Error).message}`);
        }
        if (err instanceof PublishBlockedError) {
          await persistFlowWriteBack(strapi, documentId, (err as any).writeBack);
          await strapi
            .documents('api::flow.flow')
            .update({ documentId, data: { lastPublishStatus: 'failed' } })
            .catch(() => {});
          throw new Error(`publish blocked (${err.stage}): ${(err as Error).message}`);
        }
        throw new Error(`publish failed: ${(err as Error).message}`);
      }
      const res = await next();
      await persistFlowWriteBack(strapi, documentId, result.writeBack, result.jdmDocs);
      return res;
    }

    // --- JDM: standalone create==activate on the engine. ---
    if (context.uid === 'api::jdm.jdm') {
      try {
        await runJdmPublish(strapi, documentId);
      } catch (err) {
        if (err instanceof TransformError) {
          throw new Error(`jdm publish blocked (transform): ${(err as Error).message}`);
        }
        throw new Error(`jdm publish failed: ${(err as Error).message}`);
      }
      return next();
    }

    // --- Connection: standalone reconcile+create on the engine. ---
    if (context.uid === 'api::connection.connection') {
      try {
        await runConnectionPublish(strapi, documentId);
      } catch (err) {
        if (err instanceof TransformError) {
          throw new Error(`connection publish blocked (transform): ${(err as Error).message}`);
        }
        throw new Error(`connection publish failed: ${(err as Error).message}`);
      }
      return next();
    }

    return next();
  };
}
