// Unit tests for the flow controller (version history + rollback).
//
// Tests use nock to intercept HTTP calls (the controller is passed the httpFetch
// shim so nock sees the traffic — same pattern as sync.test.ts).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import nock from 'nock';

import flowController from '../src/controllers/flow';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.flow-test';
const TOKEN = 'flow-token-xyz';

/** Build a minimal mock Strapi ctx. */
function makeCtx(params: Record<string, string> = {}, body: unknown = {}) {
  return {
    params,
    request: { body },
    body: undefined as unknown,
    status: undefined as number | undefined,
    badRequest: vi.fn(),
  };
}

/** Build a mock strapi instance (not used by flow controller, but required by factory). */
function makeStrapi() {
  return {};
}

/** Build a nock scope that requires the Bearer token on every call. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

// -----------------------------------------------------------------------
// Controller tests
// -----------------------------------------------------------------------

describe('flow controller', () => {
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

  describe('getVersions', () => {
    it('returns versions list from engine', async () => {
      engine().get('/admin/flows/flow-abc/versions').reply(200, {
        flowId: 'flow-abc',
        versions: [
          { version: 1, validated: true, createdAt: '2024-01-01T10:00:00Z', createdBy: 'admin' },
          { version: 2, validated: false, createdAt: '2024-01-02T10:00:00Z', createdBy: 'admin' },
          { version: 3, validated: true, createdAt: '2024-01-03T10:00:00Z', createdBy: 'user' },
        ],
      });

      const strapi = makeStrapi();
      const ctrl = flowController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ id: 'flow-abc' });
      await ctrl.getVersions(ctx);

      expect(ctx.body).toMatchObject({
        flowId: 'flow-abc',
        versions: [
          { version: 1, validated: true, createdAt: '2024-01-01T10:00:00Z', createdBy: 'admin' },
          { version: 2, validated: false, createdAt: '2024-01-02T10:00:00Z', createdBy: 'admin' },
          { version: 3, validated: true, createdAt: '2024-01-03T10:00:00Z', createdBy: 'user' },
        ],
      });
    });

    it('returns 404 for missing flow', async () => {
      engine().get('/admin/flows/missing-flow/versions').reply(404, { error: 'flow not found' });

      const strapi = makeStrapi();
      const ctrl = flowController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ id: 'missing-flow' });
      await ctrl.getVersions(ctx);

      expect(ctx.status).toBe(404);
      expect(ctx.body).toMatchObject({ recoverable: false });
    });

    it('returns 503 for engine unavailable (5xx)', async () => {
      engine().get('/admin/flows/flow-abc/versions').reply(503, { error: 'unavailable' });

      const strapi = makeStrapi();
      const ctrl = flowController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ id: 'flow-abc' });
      await ctrl.getVersions(ctx);

      expect(ctx.status).toBe(503);
      expect(ctx.body).toMatchObject({ recoverable: true });
    });

    it('returns 503 for missing config (no ADMIN_API_BASE_URL)', async () => {
      delete process.env.ADMIN_API_BASE_URL;

      const strapi = makeStrapi();
      const ctrl = flowController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ id: 'flow-abc' });
      await ctrl.getVersions(ctx);

      expect(ctx.status).toBe(503);
      expect(typeof (ctx.body as any)?.error).toBe('string');
    });
  });

  describe('rollback', () => {
    it('calls engine and returns new active version', async () => {
      engine()
        .post('/admin/flows/flow-xyz/rollback', { env: '', version: 2 })
        .reply(200, {
          flowId: 'flow-xyz',
          activeVersion: 2,
          action: 'rollback',
        });

      const strapi = makeStrapi();
      const ctrl = flowController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ id: 'flow-xyz' }, { version: 2 });
      await ctrl.rollback(ctx);

      expect(ctx.body).toMatchObject({
        flowId: 'flow-xyz',
        activeVersion: 2,
        action: 'rollback',
      });
    });

    it('returns 404 for missing flow', async () => {
      engine()
        .post('/admin/flows/missing-flow/rollback', { env: '', version: 1 })
        .reply(404, { error: 'flow not found' });

      const strapi = makeStrapi();
      const ctrl = flowController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ id: 'missing-flow' }, { version: 1 });
      await ctrl.rollback(ctx);

      expect(ctx.status).toBe(404);
      expect(ctx.body).toMatchObject({ recoverable: false });
    });

    it('returns 503 for engine unavailable (5xx)', async () => {
      engine()
        .post('/admin/flows/flow-xyz/rollback', { env: '', version: 2 })
        .reply(500, { error: 'internal error' });

      const strapi = makeStrapi();
      const ctrl = flowController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ id: 'flow-xyz' }, { version: 2 });
      await ctrl.rollback(ctx);

      expect(ctx.status).toBe(503);
      expect(ctx.body).toMatchObject({ recoverable: true });
    });

    it('returns 503 for missing config (no ADMIN_API_OPERATOR_TOKEN)', async () => {
      delete process.env.ADMIN_API_OPERATOR_TOKEN;

      const strapi = makeStrapi();
      const ctrl = flowController({ strapi } as any, httpFetch);
      const ctx = makeCtx({ id: 'flow-xyz' }, { version: 2 });
      await ctrl.rollback(ctx);

      expect(ctx.status).toBe(503);
      expect(typeof (ctx.body as any)?.error).toBe('string');
    });
  });
});
