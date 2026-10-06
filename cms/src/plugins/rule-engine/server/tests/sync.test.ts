// Unit tests for the sync controller.
//
// Tests use nock to intercept HTTP calls (the controller is passed the httpFetch
// shim so nock sees the traffic — same pattern as audit-controller.test.ts).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import nock from 'nock';

import syncController from '../src/controllers/sync';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.sync-test';
const TOKEN = 'sync-token-xyz';

/** Build a minimal mock Strapi ctx. */
function makeCtx(params: Record<string, string> = {}) {
  return {
    params,
    body: undefined as unknown,
    status: undefined as number | undefined,
    badRequest: vi.fn(),
  };
}

/** Build a mock strapi instance with document service stubs. */
function makeStrapi(cmsData: {
  flows?: { flowId: string; documentId: string }[];
  jdms?: { jdmId: string; documentId: string }[];
  connections?: { key: string; documentId: string }[];
}) {
  return {
    documents: vi.fn((uid: string) => {
      if (uid === 'api::flow.flow') {
        return {
          findMany: vi.fn().mockResolvedValue(
            (cmsData.flows ?? []).map((f) => ({
              flowId: f.flowId,
              documentId: f.documentId,
            }))
          ),
          create: vi.fn().mockResolvedValue({ documentId: 'new-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-doc' }),
        };
      }
      if (uid === 'api::jdm.jdm') {
        return {
          findMany: vi.fn().mockResolvedValue(
            (cmsData.jdms ?? []).map((j) => ({
              jdmId: j.jdmId,
              documentId: j.documentId,
            }))
          ),
          create: vi.fn().mockResolvedValue({ documentId: 'new-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-doc' }),
        };
      }
      if (uid === 'api::connection.connection') {
        return {
          findMany: vi.fn().mockResolvedValue(
            (cmsData.connections ?? []).map((c) => ({
              key: c.key,
              documentId: c.documentId,
            }))
          ),
          create: vi.fn().mockResolvedValue({ documentId: 'new-doc' }),
          update: vi.fn().mockResolvedValue({ documentId: 'updated-doc' }),
        };
      }
      return {
        findMany: vi.fn().mockResolvedValue([]),
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
