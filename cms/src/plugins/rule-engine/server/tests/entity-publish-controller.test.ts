// entity-publish-controller.test.ts (FEAT-002) — exercises the Publish/Sync
// controllers behind POST /rule-engine/{webhooks|schedules|groups}/:id/publish.
//
// Each handler resolves ctx.params.id (the business id) to a Strapi documentId
// via documents(uid).findMany, then runs the matching FEAT-001 runner against
// the engine admin API (mocked by nock through the httpFetch shim). The tests
// assert a successful publish returns 200 and that a TransformError surfaces as
// a 400 (ctx.badRequest) with ZERO admin calls.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import nock from 'nock';

import entityPublishController from '../src/controllers/entity-publish';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.entity-publish-test';
const TOKEN = 'entity-publish-token';

/** nock scope requiring the operator bearer on every intercepted request. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

/** The `environment` relation the runners resolve the client from. */
const ENVIRONMENT = {
  adminApiBaseUrl: BASE,
  operatorTokenRef: 'ENTITY_PUBLISH_TOKEN',
  payloadEnv: '',
};

/** Minimal Strapi ctx with the Koa helpers the controller uses. */
function makeCtx(params: Record<string, string> = {}) {
  return {
    params,
    body: undefined as unknown,
    status: undefined as number | undefined,
    badRequest: vi.fn(),
    notFound: vi.fn(),
  };
}

/**
 * Build a strapi whose documents(uid) resolves the business id via findMany and
 * returns the full populated doc via findOne (what the runner loads). update is
 * a no-op spy.
 */
function makeStrapi(uid: string, idField: string, id: string, doc: any) {
  const findMany = vi.fn().mockImplementation((opts?: any) => {
    const match = opts?.filters?.[idField]?.$eq === id;
    return Promise.resolve(match ? [{ [idField]: id, documentId: doc.documentId }] : []);
  });
  const findOne = vi.fn().mockResolvedValue(doc);
  const update = vi.fn().mockResolvedValue({ documentId: doc.documentId });
  const documents = vi.fn((requested: string) => {
    if (requested !== uid) {
      // The runner loads only `uid`; any other uid is unexpected here.
      return { findMany: vi.fn().mockResolvedValue([]), findOne: vi.fn(), update: vi.fn() };
    }
    return { findMany, findOne, update };
  });
  return { strapi: { documents }, findMany, findOne, update };
}

beforeEach(() => {
  nock.disableNetConnect();
  process.env.ENTITY_PUBLISH_TOKEN = TOKEN;
  vi.stubGlobal('fetch', httpFetch);
});

afterEach(() => {
  nock.cleanAll();
  nock.enableNetConnect();
  delete process.env.ENTITY_PUBLISH_TOKEN;
  vi.unstubAllGlobals();
});

describe('entity-publish controller — webhookPublish', () => {
  it('resolves the business id, publishes, and returns 200', async () => {
    const doc = {
      documentId: 'wh-doc',
      webhookId: 'wh-stripe',
      name: 'Stripe',
      secretRef: 'env:S',
      provider: 'stripe',
      flowId: { flowId: 'payment-flow' },
      mapping: [{ sourceJsonPath: '$.id', targetContextPath: 'id' }],
      filter: null,
      environment: ENVIRONMENT,
    };
    const { strapi } = makeStrapi('api::webhook.webhook', 'webhookId', 'wh-stripe', doc);

    engine().post('/admin/webhooks').reply(201, { id: 'wh-stripe', version: 2 });
    engine()
      .post('/admin/webhooks/wh-stripe/publish')
      .reply(200, { webhookId: 'wh-stripe', activeVersion: 2, action: 'publish' });

    const ctrl = entityPublishController({ strapi } as any);
    const ctx = makeCtx({ id: 'wh-stripe' });
    await ctrl.webhookPublish(ctx);

    expect(ctx.body).toMatchObject({ published: true, type: 'webhook', id: 'wh-stripe', engineVersion: 2 });
    expect(ctx.badRequest).not.toHaveBeenCalled();
  });

  it('surfaces a TransformError (no flowId) as a 400 with no admin call', async () => {
    const doc = {
      documentId: 'wh-doc',
      webhookId: 'wh-stripe',
      name: 'Stripe',
      secretRef: 'env:S',
      provider: 'generic',
      flowId: null,
      environment: ENVIRONMENT,
    };
    const { strapi } = makeStrapi('api::webhook.webhook', 'webhookId', 'wh-stripe', doc);

    const ctrl = entityPublishController({ strapi } as any);
    const ctx = makeCtx({ id: 'wh-stripe' });
    await ctrl.webhookPublish(ctx);

    expect(ctx.badRequest).toHaveBeenCalledWith(expect.stringContaining('flowId'));
    expect(nock.pendingMocks()).toHaveLength(0);
  });

  it('returns 404 when the business id resolves to no document', async () => {
    const { strapi } = makeStrapi('api::webhook.webhook', 'webhookId', 'other', { documentId: 'x' });

    const ctrl = entityPublishController({ strapi } as any);
    const ctx = makeCtx({ id: 'wh-missing' });
    await ctrl.webhookPublish(ctx);

    expect(ctx.notFound).toHaveBeenCalled();
  });
});

describe('entity-publish controller — schedulePublish', () => {
  it('publishes a schedule (create path) and returns 200', async () => {
    const doc = {
      documentId: 'sch-doc',
      scheduleId: 'nightly',
      name: 'Nightly',
      schedule: '0 0 * * *',
      timezone: 'UTC',
      flowId: { flowId: 'cleanup-flow' },
      input: null,
      enabled: true,
      environment: ENVIRONMENT,
    };
    const { strapi } = makeStrapi('api::schedule.schedule', 'scheduleId', 'nightly', doc);

    engine().get('/admin/schedules/nightly').reply(404, { error: 'not found' });
    engine().post('/admin/schedules').reply(201, { id: 'nightly' });

    const ctrl = entityPublishController({ strapi } as any);
    const ctx = makeCtx({ id: 'nightly' });
    await ctrl.schedulePublish(ctx);

    expect(ctx.body).toMatchObject({ published: true, type: 'schedule', id: 'nightly', created: true });
  });

  it('surfaces an invalid cron as a 400 with no admin call', async () => {
    const doc = {
      documentId: 'sch-doc',
      scheduleId: 'nightly',
      name: 'Nightly',
      schedule: 'not-a-cron',
      timezone: 'UTC',
      flowId: { flowId: 'cleanup-flow' },
      environment: ENVIRONMENT,
    };
    const { strapi } = makeStrapi('api::schedule.schedule', 'scheduleId', 'nightly', doc);

    const ctrl = entityPublishController({ strapi } as any);
    const ctx = makeCtx({ id: 'nightly' });
    await ctrl.schedulePublish(ctx);

    expect(ctx.badRequest).toHaveBeenCalled();
    expect(nock.pendingMocks()).toHaveLength(0);
  });
});

describe('entity-publish controller — groupPublish', () => {
  it('publishes a group (PUT upsert) and returns 200', async () => {
    const doc = {
      documentId: 'grp-doc',
      groupId: 'orders',
      name: 'Orders',
      description: 'workers',
      enabled: true,
      scalingMode: 'dynamic',
      minReplicas: 1,
      maxReplicas: 5,
      scaleDownDelaySeconds: 300,
      startupTimeoutSeconds: 30,
      resources: null,
      connections: [{ key: 'conn-x' }],
      environment: ENVIRONMENT,
    };
    const { strapi } = makeStrapi('api::group.group', 'groupId', 'orders', doc);

    engine().put('/admin/groups/orders').reply(200, { id: 'orders', version: 1 });

    const ctrl = entityPublishController({ strapi } as any);
    const ctx = makeCtx({ id: 'orders' });
    await ctrl.groupPublish(ctx);

    expect(ctx.body).toMatchObject({ published: true, type: 'group', id: 'orders', groupId: 'orders' });
  });

  it('surfaces a TransformError (maxReplicas<minReplicas) as a 400 with no admin call', async () => {
    const doc = {
      documentId: 'grp-doc',
      groupId: 'orders',
      name: 'Orders',
      enabled: true,
      scalingMode: 'dynamic',
      minReplicas: 5,
      maxReplicas: 2,
      connections: null,
      environment: ENVIRONMENT,
    };
    const { strapi } = makeStrapi('api::group.group', 'groupId', 'orders', doc);

    const ctrl = entityPublishController({ strapi } as any);
    const ctx = makeCtx({ id: 'orders' });
    await ctrl.groupPublish(ctx);

    expect(ctx.badRequest).toHaveBeenCalled();
    expect(nock.pendingMocks()).toHaveLength(0);
  });
});
