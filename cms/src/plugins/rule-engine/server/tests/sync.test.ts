// Unit tests for the sync controller.
//
// Tests use nock to intercept HTTP calls (the controller is passed the httpFetch
// shim so nock sees the traffic — same pattern as audit-controller.test.ts).
//
// FEAT-003: Added tests for environment-filtered sync status.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import nock from 'nock';

import syncController from '../src/controllers/sync';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.sync-test';
const TOKEN = 'sync-token-xyz';

/** Build a minimal mock Strapi ctx. */
function makeCtx(params: Record<string, string> = {}, query: Record<string, string> = {}) {
  return {
    params,
    query,
    body: undefined as unknown,
    status: undefined as number | undefined,
    badRequest: vi.fn(),
  };
}

/** Build a mock strapi instance with document service stubs. */
function makeStrapi(cmsData: {
  flows?: { flowId: string; documentId: string; environmentName?: string; environmentId?: string }[];
  jdms?: { jdmId: string; documentId: string; environmentName?: string; environmentId?: string }[];
  connections?: { key: string; documentId: string; environmentName?: string; environmentId?: string }[];
  webhooks?: { webhookId: string; documentId: string; name?: string; provider?: string; environmentName?: string; environmentId?: string }[];
  schedules?: { scheduleId: string; documentId: string; name?: string; schedule?: string; enabled?: boolean; environmentName?: string; environmentId?: string }[];
  groups?: { groupId: string; documentId: string; name?: string; enabled?: boolean; environmentName?: string; environmentId?: string }[];
  environments?: { documentId: string; name: string; adminApiBaseUrl?: string; operatorTokenRef?: string; payloadEnv?: string }[];
}) {
  return {
    documents: vi.fn((uid: string) => {
      if (uid === 'api::flow.flow') {
        return {
          findMany: vi.fn().mockImplementation((opts?: any) => {
            let flows = cmsData.flows ?? [];
            // Filter by environment if specified
            if (opts?.filters?.environment?.documentId?.$eq) {
              const envId = opts.filters.environment.documentId.$eq;
              flows = flows.filter(f => f.environmentId === envId);
            }
            return Promise.resolve(
              flows.map((f) => ({
                flowId: f.flowId,
                documentId: f.documentId,
                environment: f.environmentId ? { name: f.environmentName, documentId: f.environmentId } : null,
              }))
            );
          }),
          create: vi.fn().mockResolvedValue({ documentId: 'new-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-doc' }),
        };
      }
      if (uid === 'api::jdm.jdm') {
        return {
          findMany: vi.fn().mockImplementation((opts?: any) => {
            let jdms = cmsData.jdms ?? [];
            if (opts?.filters?.environment?.documentId?.$eq) {
              const envId = opts.filters.environment.documentId.$eq;
              jdms = jdms.filter(j => j.environmentId === envId);
            }
            return Promise.resolve(
              jdms.map((j) => ({
                jdmId: j.jdmId,
                documentId: j.documentId,
                environment: j.environmentId ? { name: j.environmentName, documentId: j.environmentId } : null,
              }))
            );
          }),
          create: vi.fn().mockResolvedValue({ documentId: 'new-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-doc' }),
        };
      }
      if (uid === 'api::connection.connection') {
        return {
          findMany: vi.fn().mockImplementation((opts?: any) => {
            let connections = cmsData.connections ?? [];
            if (opts?.filters?.environment?.documentId?.$eq) {
              const envId = opts.filters.environment.documentId.$eq;
              connections = connections.filter(c => c.environmentId === envId);
            }
            return Promise.resolve(
              connections.map((c) => ({
                key: c.key,
                documentId: c.documentId,
                environment: c.environmentId ? { name: c.environmentName, documentId: c.environmentId } : null,
              }))
            );
          }),
          create: vi.fn().mockResolvedValue({ documentId: 'new-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-doc' }),
        };
      }
      if (uid === 'api::webhook.webhook') {
        return {
          findMany: vi.fn().mockImplementation((opts?: any) => {
            let webhooks = cmsData.webhooks ?? [];
            if (opts?.filters?.environment?.documentId?.$eq) {
              const envId = opts.filters.environment.documentId.$eq;
              webhooks = webhooks.filter(w => w.environmentId === envId);
            }
            return Promise.resolve(
              webhooks.map((w) => ({
                webhookId: w.webhookId,
                documentId: w.documentId,
                name: w.name,
                provider: w.provider,
                environment: w.environmentId ? { name: w.environmentName, documentId: w.environmentId } : null,
              }))
            );
          }),
          create: vi.fn().mockResolvedValue({ documentId: 'new-webhook-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-webhook-doc' }),
        };
      }
      if (uid === 'api::schedule.schedule') {
        return {
          findMany: vi.fn().mockImplementation((opts?: any) => {
            let schedules = cmsData.schedules ?? [];
            if (opts?.filters?.environment?.documentId?.$eq) {
              const envId = opts.filters.environment.documentId.$eq;
              schedules = schedules.filter(s => s.environmentId === envId);
            }
            return Promise.resolve(
              schedules.map((s) => ({
                scheduleId: s.scheduleId,
                documentId: s.documentId,
                name: s.name,
                schedule: s.schedule,
                enabled: s.enabled,
                environment: s.environmentId ? { name: s.environmentName, documentId: s.environmentId } : null,
              }))
            );
          }),
          create: vi.fn().mockResolvedValue({ documentId: 'new-schedule-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-schedule-doc' }),
        };
      }
      if (uid === 'api::group.group') {
        return {
          findMany: vi.fn().mockImplementation((opts?: any) => {
            let groups = cmsData.groups ?? [];
            if (opts?.filters?.environment?.documentId?.$eq) {
              const envId = opts.filters.environment.documentId.$eq;
              groups = groups.filter(g => g.environmentId === envId);
            }
            return Promise.resolve(
              groups.map((g) => ({
                groupId: g.groupId,
                documentId: g.documentId,
                name: g.name,
                enabled: g.enabled,
                environment: g.environmentId ? { name: g.environmentName, documentId: g.environmentId } : null,
              }))
            );
          }),
          create: vi.fn().mockResolvedValue({ documentId: 'new-group-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-group-doc' }),
        };
      }
      if (uid === 'api::environment.environment') {
        return {
          findMany: vi.fn().mockResolvedValue(cmsData.environments ?? []),
          findOne: vi.fn().mockImplementation(({ documentId }: { documentId: string }) => {
            const env = (cmsData.environments ?? []).find(e => e.documentId === documentId);
            return Promise.resolve(env ?? null);
          }),
        };
      }
      return {
        findMany: vi.fn().mockResolvedValue([]),
        findOne: vi.fn().mockResolvedValue(null),
        create: vi.fn(),
        update: vi.fn(),
      };
    }),
  };
}

/** Build a nock scope that requires the Bearer token on every call. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

/**
 * Stub the FEAT-002 engine list endpoints (webhooks/schedules/groups) with empty
 * results. syncStatus and importAll now fan out to all six list endpoints, so a
 * test that only cares about flows/jdms/connections still needs these mocked.
 */
function stubEmptyExtras() {
  engine().get('/admin/webhooks').reply(200, { webhooks: [] });
  engine().get('/admin/schedules').reply(200, { schedules: [] });
  engine().get('/admin/groups').reply(200, { groups: [] });
}

// -----------------------------------------------------------------------
// Controller tests
// -----------------------------------------------------------------------

describe('sync controller', () => {
  beforeEach(() => {
    process.env.ADMIN_API_BASE_URL = BASE;
    process.env.ADMIN_API_OPERATOR_TOKEN = TOKEN;
    nock.disableNetConnect();
  });

  afterEach(() => {
    nock.cleanAll();
    nock.enableNetConnect();
    delete process.env.ADMIN_API_BASE_URL;
    delete process.env.ADMIN_API_OPERATOR_TOKEN;
  });

  describe('syncStatus', () => {
    it('returns correct diff when CMS and engine have overlapping items', async () => {
      // Engine has flow-a, flow-b; CMS has flow-a, flow-c
      engine().get('/admin/flows').reply(200, {
        flows: [
          { id: 'flow-a', method: 'POST', path: '/a', activeVersion: 1, updatedAt: '2024-01-01' },
          { id: 'flow-b', method: 'GET', path: '/b', activeVersion: 2, updatedAt: '2024-01-02' },
        ],
      });
      engine().get('/admin/jdms').reply(200, { jdms: [] });
      engine().get('/admin/connections').reply(200, { connections: [] });
      stubEmptyExtras();

      const strapi = makeStrapi({
        flows: [
          { flowId: 'flow-a', documentId: 'doc-a' },
          { flowId: 'flow-c', documentId: 'doc-c' },
        ],
      });

      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx();
      await ctrl.syncStatus(ctx);

      expect(ctx.body).toMatchObject({
        flows: {
          synced: [{ id: 'flow-a' }],
          localOnly: [{ id: 'flow-c' }],
          engineOnly: [{ id: 'flow-b' }],
        },
        jdms: { synced: [], localOnly: [], engineOnly: [] },
        connections: { synced: [], localOnly: [], engineOnly: [] },
      });
    });

    it('returns 503 and recoverable:true for a 5xx engine error', async () => {
      engine().get('/admin/flows').reply(503, { error: 'unavailable' });

      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx();
      await ctrl.syncStatus(ctx);

      expect(ctx.status).toBe(503);
      expect(ctx.body).toMatchObject({ recoverable: true });
    });

    it('returns 503 for missing config (no ADMIN_API_BASE_URL)', async () => {
      delete process.env.ADMIN_API_BASE_URL;

      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx();
      await ctrl.syncStatus(ctx);

      expect(ctx.status).toBe(503);
      expect(typeof (ctx.body as any)?.error).toBe('string');
    });

    it('filters CMS items by environment when ?env= query param is provided', async () => {
      // Engine has flow-a, flow-b
      engine().get('/admin/flows').reply(200, {
        flows: [
          { id: 'flow-a', method: 'POST', path: '/a', activeVersion: 1, updatedAt: '2024-01-01' },
          { id: 'flow-b', method: 'GET', path: '/b', activeVersion: 2, updatedAt: '2024-01-02' },
        ],
      });
      engine().get('/admin/jdms').reply(200, { jdms: [] });
      engine().get('/admin/connections').reply(200, { connections: [] });
      stubEmptyExtras();

      const strapi = makeStrapi({
        flows: [
          { flowId: 'flow-a', documentId: 'doc-a', environmentId: 'env-prod', environmentName: 'production' },
          { flowId: 'flow-c', documentId: 'doc-c', environmentId: 'env-staging', environmentName: 'staging' },
          { flowId: 'flow-d', documentId: 'doc-d', environmentId: 'env-prod', environmentName: 'production' },
        ],
        environments: [
          { documentId: 'env-prod', name: 'production' },
          { documentId: 'env-staging', name: 'staging' },
        ],
      });

      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({}, { env: 'env-prod' });
      await ctrl.syncStatus(ctx);

      // Should only see production flows (flow-a synced, flow-d local only)
      expect(ctx.body).toMatchObject({
        flows: {
          synced: [{ id: 'flow-a', environmentName: 'production' }],
          localOnly: [{ id: 'flow-d', environmentName: 'production' }],
          engineOnly: [{ id: 'flow-b' }],
        },
        environment: 'env-prod',
      });
    });

    it('returns webhook/schedule/group diff sections alongside flows', async () => {
      engine().get('/admin/flows').reply(200, { flows: [] });
      engine().get('/admin/jdms').reply(200, { jdms: [] });
      engine().get('/admin/connections').reply(200, { connections: [] });
      engine().get('/admin/webhooks').reply(200, {
        webhooks: [
          { id: 'wh-a', name: 'A', provider: 'stripe', flowId: 'flow-a', updatedAt: '2024-01-01' },
          { id: 'wh-b', name: 'B', provider: 'github', flowId: 'flow-b', updatedAt: '2024-01-02' },
        ],
      });
      engine().get('/admin/schedules').reply(200, {
        schedules: [
          { id: 'sched-a', name: 'SA', schedule: '@daily', timezone: 'UTC', flowId: 'flow-a', input: {}, enabled: true, createdAt: '', updatedAt: '' },
        ],
      });
      engine().get('/admin/groups').reply(200, {
        groups: [
          { id: 'grp-a', name: 'GA', enabled: true, version: 1, updatedAt: '2024-01-01' },
          { id: 'grp-b', name: 'GB', enabled: false, version: 1, updatedAt: '2024-01-02' },
        ],
      });

      const strapi = makeStrapi({
        webhooks: [{ webhookId: 'wh-a', documentId: 'wdoc-a' }],
        schedules: [{ scheduleId: 'sched-c', documentId: 'sdoc-c' }],
        groups: [{ groupId: 'grp-b', documentId: 'gdoc-b' }],
      });

      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx();
      await ctrl.syncStatus(ctx);

      expect(ctx.body).toMatchObject({
        webhooks: {
          synced: [{ id: 'wh-a' }],
          localOnly: [],
          engineOnly: [{ id: 'wh-b' }],
        },
        schedules: {
          synced: [],
          localOnly: [{ id: 'sched-c' }],
          engineOnly: [{ id: 'sched-a' }],
        },
        groups: {
          synced: [{ id: 'grp-b' }],
          localOnly: [],
          engineOnly: [{ id: 'grp-a' }],
        },
      });
    });

    it('returns 404 when env query param references non-existent environment', async () => {
      const strapi = makeStrapi({
        environments: [],
      });

      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({}, { env: 'non-existent' });
      await ctrl.syncStatus(ctx);

      expect(ctx.status).toBe(404);
      expect(ctx.body).toMatchObject({ error: 'Environment not found' });
    });

    it('includes environment info in response when showing all environments', async () => {
      engine().get('/admin/flows').reply(200, {
        flows: [
          { id: 'flow-a', method: 'POST', path: '/a', activeVersion: 1, updatedAt: '2024-01-01' },
        ],
      });
      engine().get('/admin/jdms').reply(200, { jdms: [] });
      engine().get('/admin/connections').reply(200, { connections: [] });
      stubEmptyExtras();

      const strapi = makeStrapi({
        flows: [
          { flowId: 'flow-a', documentId: 'doc-a', environmentId: 'env-prod', environmentName: 'production' },
        ],
      });

      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx(); // No env filter = all environments
      await ctrl.syncStatus(ctx);

      expect(ctx.body).toMatchObject({
        flows: {
          synced: [{ id: 'flow-a', environmentName: 'production', environmentId: 'env-prod' }],
        },
        environment: null,
      });
    });
  });

  describe('importAll', () => {
    it('imports all engine items and returns counts', async () => {
      engine().get('/admin/flows').reply(200, {
        flows: [{ id: 'flow-a', method: 'POST', path: '/a', activeVersion: 1, updatedAt: '2024-01-01' }],
      });
      engine().get('/admin/jdms').reply(200, {
        jdms: [{ id: 'jdm-1', updatedAt: '2024-01-01' }],
      });
      engine().get('/admin/connections').reply(200, {
        connections: [{ key: 'conn-x', type: 'http', settings: {}, secretRef: '', resilience: {} }],
      });
      stubEmptyExtras();

      // Detail endpoints
      engine().get('/admin/flows/flow-a').reply(200, {
        flowId: 'flow-a',
        version: 1,
        method: 'POST',
        path: '/a',
        tree: { id: 'root', type: 'input', spec: {}, children: [] },
        fixtures: [],
      });
      engine().get('/admin/jdms/jdm-1').reply(200, {
        jdmId: 'jdm-1',
        version: 1,
        doc: { rules: [] },
      });

      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx();
      await ctrl.importAll(ctx);

      expect(ctx.body).toMatchObject({
        imported: {
          flows: 1,
          jdms: 1,
          connections: 1,
        },
      });
    });

    it('returns 503 for a 5xx engine error during import', async () => {
      engine().get('/admin/flows').reply(500, { error: 'internal error' });

      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx();
      await ctrl.importAll(ctx);

      expect(ctx.status).toBe(503);
      expect(ctx.body).toMatchObject({ recoverable: true });
    });
  });

  describe('importOne', () => {
    it('returns badRequest for an invalid sync type', async () => {
      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ type: 'workflow', id: 'test' });
      await ctrl.importOne(ctx);

      expect(ctx.badRequest).toHaveBeenCalledWith(
        expect.stringContaining('invalid sync type')
      );
    });

    it('imports a single flow successfully', async () => {
      engine().get('/admin/flows/flow-a').reply(200, {
        flowId: 'flow-a',
        version: 1,
        method: 'POST',
        path: '/a',
        tree: { id: 'root', type: 'input', spec: {}, children: [] },
        fixtures: [],
      });

      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ type: 'flow', id: 'flow-a' });
      await ctrl.importOne(ctx);

      expect(ctx.body).toMatchObject({
        imported: true,
        type: 'flow',
        id: 'flow-a',
      });
    });

    it('imports a single jdm successfully', async () => {
      engine().get('/admin/jdms/jdm-1').reply(200, {
        jdmId: 'jdm-1',
        version: 1,
        doc: { rules: [] },
      });

      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ type: 'jdm', id: 'jdm-1' });
      await ctrl.importOne(ctx);

      expect(ctx.body).toMatchObject({
        imported: true,
        type: 'jdm',
        id: 'jdm-1',
      });
    });

    it('imports a single connection successfully', async () => {
      engine().get('/admin/connections/conn-x').reply(200, {
        key: 'conn-x',
        type: 'http',
        settings: {},
        secretRef: '',
        resilience: {},
      });

      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ type: 'connection', id: 'conn-x' });
      await ctrl.importOne(ctx);

      expect(ctx.body).toMatchObject({
        imported: true,
        type: 'connection',
        id: 'conn-x',
      });
    });

    it('imports a single webhook, converting the engine mapping/filter maps to CMS component arrays', async () => {
      engine().get('/admin/webhooks/wh-a').reply(200, {
        webhookId: 'wh-a',
        name: 'Checkout',
        secretRef: 'env:WH_SECRET',
        provider: 'stripe',
        flowId: 'flow-a',
        mapping: { '$.id': 'event.id', '$.type': 'event.type' },
        filter: { '$.type': ['payment_intent.succeeded'] },
        version: 3,
      });

      // Capture the data passed to webhook create.
      let created: any = null;
      const strapi: any = {
        documents: vi.fn((uid: string) => {
          if (uid === 'api::flow.flow') {
            return { findMany: vi.fn().mockResolvedValue([{ flowId: 'flow-a', documentId: 'flow-doc-a' }]) };
          }
          if (uid === 'api::webhook.webhook') {
            return {
              findMany: vi.fn().mockResolvedValue([]),
              create: vi.fn().mockImplementation(({ data }: any) => {
                created = data;
                return Promise.resolve({ documentId: 'new-wh' });
              }),
              update: vi.fn(),
            };
          }
          return { findMany: vi.fn().mockResolvedValue([]), create: vi.fn(), update: vi.fn() };
        }),
      };

      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ type: 'webhook', id: 'wh-a' });
      await ctrl.importOne(ctx);

      expect(ctx.body).toMatchObject({ imported: true, type: 'webhook', id: 'wh-a' });
      expect(created.webhookId).toBe('wh-a');
      expect(created.flowId).toBe('flow-doc-a');
      // map -> component array
      expect(created.mapping).toEqual(
        expect.arrayContaining([
          { sourceJsonPath: '$.id', targetContextPath: 'event.id' },
          { sourceJsonPath: '$.type', targetContextPath: 'event.type' },
        ])
      );
      expect(created.filter).toEqual([
        { jsonPath: '$.type', allowedValues: ['payment_intent.succeeded'] },
      ]);
    });

    it('imports a single schedule (straight field copy, resolves flowId relation)', async () => {
      engine().get('/admin/schedules/sched-a').reply(200, {
        id: 'sched-a',
        name: 'Nightly',
        schedule: '0 0 * * *',
        timezone: 'Asia/Jakarta',
        flowId: 'flow-a',
        input: { k: 'v' },
        enabled: true,
        createdAt: '',
        updatedAt: '',
      });

      let created: any = null;
      const strapi: any = {
        documents: vi.fn((uid: string) => {
          if (uid === 'api::flow.flow') {
            return { findMany: vi.fn().mockResolvedValue([{ flowId: 'flow-a', documentId: 'flow-doc-a' }]) };
          }
          if (uid === 'api::schedule.schedule') {
            return {
              findMany: vi.fn().mockResolvedValue([]),
              create: vi.fn().mockImplementation(({ data }: any) => {
                created = data;
                return Promise.resolve({ documentId: 'new-sched' });
              }),
              update: vi.fn(),
            };
          }
          return { findMany: vi.fn().mockResolvedValue([]), create: vi.fn(), update: vi.fn() };
        }),
      };

      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ type: 'schedule', id: 'sched-a' });
      await ctrl.importOne(ctx);

      expect(ctx.body).toMatchObject({ imported: true, type: 'schedule', id: 'sched-a' });
      expect(created).toMatchObject({
        scheduleId: 'sched-a',
        name: 'Nightly',
        schedule: '0 0 * * *',
        timezone: 'Asia/Jakarta',
        enabled: true,
        input: { k: 'v' },
        flowId: 'flow-doc-a',
      });
    });

    it('imports a single group, converting the nested ScalingConfig to flat CMS fields', async () => {
      engine().get('/admin/groups/grp-a').reply(200, {
        id: 'grp-a',
        name: 'Workers',
        description: 'pool',
        version: 2,
        connections: ['conn-x', 'conn-y'],
        enabled: true,
        scaling: {
          mode: 'dynamic',
          minReplicas: 1,
          maxReplicas: 5,
          scaleDownDelay: '5m',
          startupTimeout: '30s',
          resources: { cpuRequest: '100m', memoryLimit: '512Mi' },
        },
      });

      let created: any = null;
      const strapi: any = {
        documents: vi.fn((uid: string) => {
          if (uid === 'api::connection.connection') {
            return {
              findMany: vi.fn().mockResolvedValue([
                { key: 'conn-x', documentId: 'conn-doc-x' },
                { key: 'conn-y', documentId: 'conn-doc-y' },
              ]),
            };
          }
          if (uid === 'api::group.group') {
            return {
              findMany: vi.fn().mockResolvedValue([]),
              create: vi.fn().mockImplementation(({ data }: any) => {
                created = data;
                return Promise.resolve({ documentId: 'new-grp' });
              }),
              update: vi.fn(),
            };
          }
          return { findMany: vi.fn().mockResolvedValue([]), create: vi.fn(), update: vi.fn() };
        }),
      };

      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ type: 'group', id: 'grp-a' });
      await ctrl.importOne(ctx);

      expect(ctx.body).toMatchObject({ imported: true, type: 'group', id: 'grp-a' });
      expect(created).toMatchObject({
        groupId: 'grp-a',
        name: 'Workers',
        enabled: true,
        scalingMode: 'dynamic',
        minReplicas: 1,
        maxReplicas: 5,
        // duration strings -> integer seconds
        scaleDownDelaySeconds: 300,
        startupTimeoutSeconds: 30,
        resources: { cpuRequest: '100m', memoryLimit: '512Mi' },
        // connection keys -> relation documentIds
        connections: ['conn-doc-x', 'conn-doc-y'],
      });
    });

    it('returns 404 for a missing item', async () => {
      engine().get('/admin/flows/missing').reply(404, { error: 'not found' });

      const strapi = makeStrapi({});
      const ctrl = syncController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ type: 'flow', id: 'missing' });
      await ctrl.importOne(ctx);

      expect(ctx.status).toBe(404);
      expect(ctx.body).toMatchObject({ recoverable: false });
    });
  });
});
