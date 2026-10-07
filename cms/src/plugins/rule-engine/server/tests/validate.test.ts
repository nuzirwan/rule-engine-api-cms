// Unit tests for the validate controller (validate + dry-run).
//
// Tests use nock to intercept HTTP calls (the controller is passed the httpFetch
// shim so nock sees the traffic — same pattern as flow.test.ts).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import nock from 'nock';

import validateController from '../src/controllers/validate';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.validate-test';
const TOKEN = 'validate-token-xyz';

/** Build a minimal mock Strapi ctx. */
function makeCtx(body: unknown = {}) {
  return {
    request: { body },
    body: undefined as unknown,
    status: undefined as number | undefined,
    badRequest: vi.fn(),
  };
}

/** Build a mock strapi instance (not used by validate controller, but required by factory). */
function makeStrapi() {
  return {};
}

/** Build a nock scope that requires the Bearer token on every call. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

/** A sample flow tree for testing. */
const sampleTree = {
  id: 'trigger',
  type: 'trigger',
  spec: {},
  children: [
    {
      id: 'action-1',
      type: 'action',
      spec: { connection: 'pg', operation: { Kind: 'query', Payload: 'SELECT 1' } },
    },
  ],
};

// -----------------------------------------------------------------------
// Controller tests
// -----------------------------------------------------------------------

describe('validate controller', () => {
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

  describe('validate', () => {
    it('returns validation success for valid flow', async () => {
      engine()
        .post('/admin/flows/validate', (body) => {
          // Verify the request shape is candidate-mode
          return body.flow && body.flow.tree && body.env !== undefined;
        })
        .reply(200, {
          ok: true,
          structural: [],
          fixtures: [],
        });

      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
      });
      await ctrl.validate(ctx);

      expect(ctx.body).toMatchObject({
        ok: true,
        structural: [],
        fixtures: [],
      });
    });

    it('returns validation errors for invalid flow', async () => {
      engine()
        .post('/admin/flows/validate')
        .reply(200, {
          ok: false,
          structural: [{ nodeId: 'action-1', message: 'connection not found: pg' }],
          fixtures: [],
        });

      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
      });
      await ctrl.validate(ctx);

      expect(ctx.body).toMatchObject({
        ok: false,
        structural: [{ nodeId: 'action-1', message: 'connection not found: pg' }],
      });
    });

    it('returns 400 for missing tree field', async () => {
      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        // tree is missing
      });
      await ctrl.validate(ctx);

      expect(ctx.status).toBe(400);
      expect((ctx.body as any)?.error).toMatch(/tree/i);
    });

    it('returns 400 for missing method field', async () => {
      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        // method is missing
        path: '/test',
        tree: sampleTree,
      });
      await ctrl.validate(ctx);

      expect(ctx.status).toBe(400);
      expect((ctx.body as any)?.error).toMatch(/method/i);
    });

    it('returns 400 for missing path field', async () => {
      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        // path is missing
        tree: sampleTree,
      });
      await ctrl.validate(ctx);

      expect(ctx.status).toBe(400);
      expect((ctx.body as any)?.error).toMatch(/path/i);
    });

    it('returns 503 for engine unavailable (5xx)', async () => {
      engine().post('/admin/flows/validate').reply(503, { error: 'unavailable' });

      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
      });
      await ctrl.validate(ctx);

      expect(ctx.status).toBe(503);
      expect((ctx.body as any)?.recoverable).toBe(true);
    });

    it('returns 503 for missing config', async () => {
      delete process.env.ADMIN_API_BASE_URL;

      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
      });
      await ctrl.validate(ctx);

      expect(ctx.status).toBe(503);
      expect(typeof (ctx.body as any)?.error).toBe('string');
    });
  });

  describe('dryRun', () => {
    it('returns trace and response for successful dry-run', async () => {
      engine()
        .post('/admin/flows/dry-run', (body) => {
          // Verify the request shape is candidate-mode
          return body.flow && body.flow.tree && body.input && body.env !== undefined;
        })
        .reply(200, {
          trace: [
            { nodeId: 'trigger', type: 'trigger', duration: 1 },
            { nodeId: 'action-1', type: 'action', duration: 5 },
          ],
          response: { result: 'ok' },
          errors: [],
        });

      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
        input: { params: { id: '123' } },
      });
      await ctrl.dryRun(ctx);

      expect(ctx.body).toMatchObject({
        trace: [
          { nodeId: 'trigger', type: 'trigger', duration: 1 },
          { nodeId: 'action-1', type: 'action', duration: 5 },
        ],
        response: { result: 'ok' },
        errors: [],
      });
    });

    it('returns errors in dry-run result (not HTTP error)', async () => {
      engine()
        .post('/admin/flows/dry-run')
        .reply(200, {
          trace: [{ nodeId: 'trigger', type: 'trigger', duration: 1 }],
          response: null,
          errors: ['connection failed: pg'],
        });

      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
        input: { params: {} },
      });
      await ctrl.dryRun(ctx);

      expect(ctx.body).toMatchObject({
        errors: ['connection failed: pg'],
      });
    });

    it('returns 400 for missing tree field', async () => {
      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        // tree is missing
        input: { params: {} },
      });
      await ctrl.dryRun(ctx);

      expect(ctx.status).toBe(400);
      expect((ctx.body as any)?.error).toMatch(/tree/i);
    });

    it('returns 400 for missing input field', async () => {
      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
        // input is missing
      });
      await ctrl.dryRun(ctx);

      expect(ctx.status).toBe(400);
      expect((ctx.body as any)?.error).toMatch(/input/i);
    });

    it('returns 503 for engine unavailable (5xx)', async () => {
      engine().post('/admin/flows/dry-run').reply(500, { error: 'internal error' });

      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
        input: { params: {} },
      });
      await ctrl.dryRun(ctx);

      expect(ctx.status).toBe(503);
      expect((ctx.body as any)?.recoverable).toBe(true);
    });

    it('returns 503 for missing config', async () => {
      delete process.env.ADMIN_API_OPERATOR_TOKEN;

      const strapi = makeStrapi();
      const ctrl = validateController({ strapi } as any, httpFetch);
      const ctx = makeCtx({
        flowId: 'test-flow',
        method: 'GET',
        path: '/test',
        tree: sampleTree,
        input: { params: {} },
      });
      await ctrl.dryRun(ctx);

      expect(ctx.status).toBe(503);
      expect(typeof (ctx.body as any)?.error).toBe('string');
    });
  });
});
