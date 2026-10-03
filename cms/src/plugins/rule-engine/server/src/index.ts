// rule-engine plugin — server entry (design §4, §5.3/§5.4). Registers:
//   * the two custom fields on the server (base type:'json') so Strapi's server
//     validates Flow.tree/Jdm.doc under the custom field uids
//     plugin::rule-engine.flow-canvas / .jdm-editor;
//   * the publish controller + route;
//   * a DOCUMENT SERVICE MIDDLEWARE that intercepts the Flow `publish` action and
//     pushes the flow (+ its referenced JDMs/connections) to the engine admin
//     HTTP API, enforcing the publish-blocking gate.
//
// STRAPI 5 FIX: the publish interception is a Document Service middleware
// (strapi.documents.use), NOT a `strapi.db.lifecycles.subscribe({ beforePublish })`
// DB hook. The DB publish lifecycle is a Strapi v4 pattern that the v5 Document
// Service `publish` action does NOT invoke — so the old wiring silently no-opped
// (publish returned 200 but nothing reached the engine). Per Strapi 5 guidance,
// content/publish rules belong in a Document Service middleware.

import publishController from './controllers/publish';
import routes from './routes';
import {
  runFlowPublish,
  persistFlowWriteBack,
  runJdmPublish,
  runConnectionPublish,
} from './services/publish-core';
import { PublishBlockedError } from './services/validation';
import { TransformError } from './services/publish-transform';

const PLUGIN_ID = 'rule-engine';

export default {
  register({ strapi }: { strapi: any }) {
    // Register both custom fields server-side with base type:'json'.
    strapi.customFields.register([
      { name: 'flow-canvas', plugin: PLUGIN_ID, type: 'json' },
      { name: 'jdm-editor', plugin: PLUGIN_ID, type: 'json' },
    ]);

    // Document Service middleware: intercept the `publish` action on Flow, JDM,
    // and Connection and push to the engine BEFORE the publish proceeds (strict
    // gate). A thrown error aborts the Strapi publish so an un-pushable entry
    // never shows as published. JDMs and connections publish standalone (they
    // are first-class engine objects), and also ride along a Flow publish for
    // the flow's referenced ids.
    const PUBLISHABLE = new Set([
      'api::flow.flow',
      'api::jdm.jdm',
      'api::connection.connection',
    ]);

    strapi.documents.use(async (context: any, next: any) => {
      // DIAGNOSTIC: log every document-service action so we can see what the
      // content-manager publish actually looks like (uid/action). Remove once
      // the publish interception is confirmed.
      if (PUBLISHABLE.has(context?.uid)) {
        strapi.log.info(
          `[rule-engine] doc-service: uid=${context?.uid} action=${context?.action}`
        );
      }
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
    });
  },

  bootstrap() {},

  controllers: {
    publish: publishController,
  },

  routes,

  services: {},
};
