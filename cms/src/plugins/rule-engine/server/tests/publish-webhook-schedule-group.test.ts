// FEAT-001 integration — runWebhookPublish / runSchedulePublish / runGroupPublish
// driven through the real AdminClient with the engine admin API mocked by nock.
//
// A minimal `strapi.documents(uid)` object (findOne/update) stands in for the
// Document Service (same approach as the controller/sync tests). The AdminClient
// is wired to the httpFetch shim so nock intercepts and asserts every request.
//
// The runners read the per-entry env via resolveAdminConfig(); the mocked docs
// carry an `environment` relation pointing at the nock base + an env-var token.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import nock from 'nock';

import {
  runGroupPublish,
  runSchedulePublish,
  runWebhookPublish,
} from '../src/services/publish-core';
import { TransformError } from '../src/services/publish-transform';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.feat001-test';
const TOKEN = 'feat001-token';

/** nock scope requiring the operator bearer on EVERY intercepted request. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

/** The `environment` relation shape the runners resolve the client from. */
const ENVIRONMENT = {
  adminApiBaseUrl: BASE,
  operatorTokenRef: 'FEAT001_TOKEN',
  payloadEnv: '',
};

/**
 * Build a minimal strapi whose documents(uid) exposes findOne (returns `doc`)
 * and a spy-able update. The AdminClient resolves its token from process.env, so
 * the test stamps FEAT001_TOKEN before constructing clients.
 */
function makeStrapi(uid: string, doc: any) {
  const update = vi.fn().mockResolvedValue({ documentId: doc?.documentId ?? 'doc-1' });
  const findOne = vi.fn().mockResolvedValue(doc);
  const documents = vi.fn((requested: string) => {
    if (requested !== uid) {
      throw new Error(`unexpected uid ${requested}`);
    }
    return { findOne, update };
  });
  return { strapi: { documents }, update, findOne };
}

beforeEach(() => {
  nock.disableNetConnect();
  process.env.FEAT001_TOKEN = TOKEN;
  // Inject the http shim so nock sees AdminClient traffic (undici bypasses nock).
  vi.stubGlobal('fetch', httpFetch);
});

afterEach(() => {
  nock.cleanAll();
  nock.enableNetConnect();
  delete process.env.FEAT001_TOKEN;
  vi.unstubAllGlobals();
});

describe('FEAT-001 runWebhookPublish', () => {
  it('creates (id + map mapping/filter), publishes, then writes back synced', async () => {
    const doc = {
      documentId: 'wh-doc',
      webhookId: 'wh-stripe',
      name: 'Stripe Payment',
      secretRef: 'env:STRIPE_SECRET',
      provider: 'stripe',
      flowId: { flowId: 'payment-flow' },
      mapping: [{ sourceJsonPath: '$.data.object.id', targetContextPath: 'payment_id' }],
      filter: [{ jsonPath: '$.type', allowedValues: ['payment_intent.succeeded'] }],
      environment: ENVIRONMENT,
    };
    const { strapi, update } = makeStrapi('api::webhook.webhook', doc);

    let createBody: any;
    const sCreate = engine()
      .post('/admin/webhooks', (b: any) => {
        createBody = b;
        return true;
      })
      .reply(201, { id: 'wh-stripe', version: 4 });

    let publishBody: any;
    const sPublish = engine()
      .post('/admin/webhooks/wh-stripe/publish', (b: any) => {
        publishBody = b;
        return true;
      })
      .reply(200, { webhookId: 'wh-stripe', activeVersion: 4, action: 'publish' });

    const res = await runWebhookPublish(strapi, 'wh-doc');

    expect(res).toEqual({ engineVersion: 4 });
    // create body: top-level `id` (NOT webhookId), map-shaped mapping/filter.
    expect(createBody.id).toBe('wh-stripe');
    expect(createBody).not.toHaveProperty('webhookId');
    expect(createBody.flowId).toBe('payment-flow');
    expect(createBody.mapping).toEqual({ '$.data.object.id': 'payment_id' });
    expect(createBody.filter).toEqual({ '$.type': ['payment_intent.succeeded'] });
    // publish body carries {env,version}.
    expect(publishBody).toEqual({ env: '', version: 4 });
    // write-back: engineVersion + lastSyncStatus=synced.
    expect(update).toHaveBeenCalledWith({
      documentId: 'wh-doc',
      data: { engineVersion: 4, lastSyncStatus: 'synced' },
    });
    expect(sCreate.isDone()).toBe(true);
    expect(sPublish.isDone()).toBe(true);
  });

  it('writes back lastSyncStatus=failed and rethrows on an admin error', async () => {
    const doc = {
      documentId: 'wh-doc',
      webhookId: 'wh-stripe',
      name: 'Stripe',
      secretRef: 'env:S',
      provider: 'generic',
      flowId: { flowId: 'f' },
      environment: ENVIRONMENT,
    };
    const { strapi, update } = makeStrapi('api::webhook.webhook', doc);
    engine().post('/admin/webhooks').reply(400, { error: 'validation failed' });

    await expect(runWebhookPublish(strapi, 'wh-doc')).rejects.toMatchObject({ status: 400 });
    expect(update).toHaveBeenCalledWith({
      documentId: 'wh-doc',
      data: { lastSyncStatus: 'failed' },
    });
  });

  it('throws TransformError (no flowId) BEFORE any admin call', async () => {
    const doc = {
      documentId: 'wh-doc',
      webhookId: 'wh-stripe',
      name: 'Stripe',
      secretRef: 'env:S',
      provider: 'generic',
      flowId: null,
      environment: ENVIRONMENT,
    };
    const { strapi, update } = makeStrapi('api::webhook.webhook', doc);

    await expect(runWebhookPublish(strapi, 'wh-doc')).rejects.toBeInstanceOf(TransformError);
    expect(update).not.toHaveBeenCalled();
    expect(nock.pendingMocks()).toHaveLength(0);
  });
});

describe('FEAT-001 runSchedulePublish', () => {
  const scheduleDoc = (overrides: Record<string, unknown> = {}) => ({
    documentId: 'sch-doc',
    scheduleId: 'nightly',
    name: 'Nightly Job',
    schedule: '0 0 * * *',
    timezone: 'Asia/Jakarta',
    flowId: { flowId: 'cleanup-flow' },
    input: { foo: 'bar' },
    enabled: true,
    environment: ENVIRONMENT,
    ...overrides,
  });

  it('creates when GET returns 404', async () => {
    const { strapi, update } = makeStrapi('api::schedule.schedule', scheduleDoc());
    engine().get('/admin/schedules/nightly').reply(404, { error: 'not found' });
    let createBody: any;
    const sCreate = engine()
      .post('/admin/schedules', (b: any) => {
        createBody = b;
        return true;
      })
      .reply(201, { id: 'nightly', nextRun: '2026-01-01T00:00:00Z' });

    const res = await runSchedulePublish(strapi, 'sch-doc');

    expect(res).toEqual({ created: true });
    expect(createBody.id).toBe('nightly');
    expect(createBody.schedule).toBe('0 0 * * *');
    expect(createBody.timezone).toBe('Asia/Jakarta');
    expect(createBody.flowId).toBe('cleanup-flow');
    expect(update).toHaveBeenCalledWith({
      documentId: 'sch-doc',
      data: { lastSyncStatus: 'synced', nextRun: '2026-01-01T00:00:00Z' },
    });
    expect(sCreate.isDone()).toBe(true);
  });

  it('updates when GET returns 200', async () => {
    const { strapi, update } = makeStrapi('api::schedule.schedule', scheduleDoc());
    engine().get('/admin/schedules/nightly').reply(200, { id: 'nightly', name: 'Nightly Job' });
    let putBody: any;
    const sUpdate = engine()
      .put('/admin/schedules/nightly', (b: any) => {
        putBody = b;
        return true;
      })
      .reply(200, { id: 'nightly' });

    const res = await runSchedulePublish(strapi, 'sch-doc');

    expect(res).toEqual({ created: false });
    // update body omits `id` (it lives in the URL path).
    expect(putBody).not.toHaveProperty('id');
    expect(putBody.schedule).toBe('0 0 * * *');
    expect(update).toHaveBeenCalledWith({
      documentId: 'sch-doc',
      data: { lastSyncStatus: 'synced' },
    });
    expect(sUpdate.isDone()).toBe(true);
  });

  it('rejects a bad cron with TransformError and ZERO admin calls', async () => {
    const { strapi, update } = makeStrapi('api::schedule.schedule', scheduleDoc({ schedule: 'nope' }));

    await expect(runSchedulePublish(strapi, 'sch-doc')).rejects.toBeInstanceOf(TransformError);
    expect(update).not.toHaveBeenCalled();
    expect(nock.pendingMocks()).toHaveLength(0);
    expect(nock.activeMocks()).toHaveLength(0);
  });

  it('rejects a non-IANA timezone with TransformError and ZERO admin calls', async () => {
    const { strapi } = makeStrapi('api::schedule.schedule', scheduleDoc({ timezone: 'not a zone' }));

    await expect(runSchedulePublish(strapi, 'sch-doc')).rejects.toBeInstanceOf(TransformError);
    expect(nock.pendingMocks()).toHaveLength(0);
  });
});

describe('FEAT-001 runGroupPublish', () => {
  const groupDoc = (overrides: Record<string, unknown> = {}) => ({
    documentId: 'grp-doc',
    groupId: 'orders',
    name: 'Orders Group',
    description: 'order processing workers',
    enabled: true,
    scalingMode: 'dynamic',
    minReplicas: 1,
    maxReplicas: 5,
    scaleDownDelaySeconds: 300,
    startupTimeoutSeconds: 30,
    resources: {
      id: 7,
      __component: 'config.resource-limits',
      cpuRequest: '100m',
      cpuLimit: '500m',
      memoryRequest: '128Mi',
      memoryLimit: '512Mi',
    },
    connections: [{ key: 'fmc-pg' }, { key: 'redis-cache' }],
    environment: ENVIRONMENT,
    ...overrides,
  });

  it('PUTs /admin/groups/{groupId} with nested scaling + duration strings + connection keys', async () => {
    const { strapi } = makeStrapi('api::group.group', groupDoc());
    let putBody: any;
    const sPut = engine()
      .put('/admin/groups/orders', (b: any) => {
        putBody = b;
        return true;
      })
      .reply(200, { id: 'orders', version: 2 });

    const res = await runGroupPublish(strapi, 'grp-doc');

    expect(res).toEqual({ groupId: 'orders' });
    expect(putBody.env).toBe('');
    expect(putBody).not.toHaveProperty('scalingMode');
    expect(putBody.scaling.mode).toBe('dynamic');
    expect(putBody.scaling.minReplicas).toBe(1);
    expect(putBody.scaling.maxReplicas).toBe(5);
    expect(putBody.scaling.scaleDownDelay).toBe('300s');
    expect(putBody.scaling.startupTimeout).toBe('30s');
    expect(putBody.scaling.resources).toEqual({
      cpuRequest: '100m',
      cpuLimit: '500m',
      memoryRequest: '128Mi',
      memoryLimit: '512Mi',
    });
    expect(putBody.connections).toEqual(['fmc-pg', 'redis-cache']);
    expect(sPut.isDone()).toBe(true);
  });

  it('throws TransformError (maxReplicas<minReplicas) BEFORE any HTTP call', async () => {
    const { strapi } = makeStrapi(
      'api::group.group',
      groupDoc({ minReplicas: 5, maxReplicas: 2 })
    );

    await expect(runGroupPublish(strapi, 'grp-doc')).rejects.toBeInstanceOf(TransformError);
    expect(nock.pendingMocks()).toHaveLength(0);
    expect(nock.activeMocks()).toHaveLength(0);
  });
});
