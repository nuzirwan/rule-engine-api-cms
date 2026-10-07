// entity-publish.ts — admin-only controllers behind the explicit Publish/Sync
// actions for webhooks, schedules, and groups (FEAT-002). Each handler resolves
// ctx.params.id to a Strapi documentId and runs the matching FEAT-001 runner
// (runWebhookPublish / runSchedulePublish / runGroupPublish from publish-core),
// which pushes the entity to the engine admin HTTP API.
//
// Error classification mirrors publish.ts / the §5.5 table:
//   * TransformError (author-fixable: empty flowId, bad cron/timezone,
//     maxReplicas<minReplicas) → 400 (ctx.badRequest);
//   * AdminApiError → recoverable (5xx/transport) 503, non-recoverable 4xx
//     echoes the engine status;
//   * a missing document → 404.
//
// Each content type exposes a `uid`-style business id (webhookId / scheduleId /
// groupId). The Publish/Sync route carries that business id in ctx.params.id;
// the handler resolves it to the Strapi documentId before calling the runner.

import { AdminApiError } from '../services/admin-client';
import { TransformError } from '../services/publish-transform';
import {
  runGroupPublish,
  runSchedulePublish,
  runWebhookPublish,
} from '../services/publish-core';

/** Shared error mapping for the three publish handlers. */
function handlePublishError(ctx: any, err: unknown): void {
  if (err instanceof TransformError) {
    ctx.badRequest(err.message);
    return;
  }
  if (err instanceof AdminApiError) {
    ctx.status = err.recoverable ? 503 : err.status || 500;
    ctx.body = { error: err.message, recoverable: err.recoverable };
    return;
  }
  throw err;
}

/**
 * Resolve a content type's business id (ctx.params.id) to its Strapi
 * documentId. Returns null when no entry matches so the caller can 404.
 */
async function resolveDocumentId(
  strapi: any,
  uid: string,
  idField: string,
  id: string
): Promise<string | null> {
  const matches = await strapi.documents(uid).findMany({
    fields: [idField],
    filters: { [idField]: { $eq: id } },
  });
  return matches?.[0]?.documentId ?? null;
}

export default function entityPublishController({ strapi }: { strapi: any }) {
  return {
    /** POST /webhooks/:id/publish — push a webhook to the engine (create+publish). */
    async webhookPublish(ctx: any) {
      const id: string = ctx.params.id;
      const documentId = await resolveDocumentId(strapi, 'api::webhook.webhook', 'webhookId', id);
      if (!documentId) {
        ctx.notFound(`webhook "${id}" not found`);
        return;
      }
      try {
        const result = await runWebhookPublish(strapi, documentId);
        ctx.body = { published: true, type: 'webhook', id, engineVersion: result.engineVersion };
      } catch (err) {
        handlePublishError(ctx, err);
      }
    },

    /** POST /schedules/:id/publish — push a schedule to the engine (create/update). */
    async schedulePublish(ctx: any) {
      const id: string = ctx.params.id;
      const documentId = await resolveDocumentId(strapi, 'api::schedule.schedule', 'scheduleId', id);
      if (!documentId) {
        ctx.notFound(`schedule "${id}" not found`);
        return;
      }
      try {
        const result = await runSchedulePublish(strapi, documentId);
        ctx.body = { published: true, type: 'schedule', id, created: result.created };
      } catch (err) {
        handlePublishError(ctx, err);
      }
    },

    /** POST /groups/:id/publish — push a group to the engine (idempotent PUT upsert). */
    async groupPublish(ctx: any) {
      const id: string = ctx.params.id;
      const documentId = await resolveDocumentId(strapi, 'api::group.group', 'groupId', id);
      if (!documentId) {
        ctx.notFound(`group "${id}" not found`);
        return;
      }
      try {
        const result = await runGroupPublish(strapi, documentId);
        ctx.body = { published: true, type: 'group', id, groupId: result.groupId };
      } catch (err) {
        handlePublishError(ctx, err);
      }
    },
  };
}
