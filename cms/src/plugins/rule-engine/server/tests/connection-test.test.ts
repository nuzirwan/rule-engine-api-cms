// connection-test.test.ts — tests for POST /rule-engine/connections/test proxy endpoint.
//
// The connectionTestController proxies to the engine's POST /admin/connections/test,
// handling error classification per §5.5:
//   * 5xx / transport (recoverable) → 503
//   * 4xx (non-recoverable) → echo the engine status

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import nock from 'nock';

import connectionTestController from '../src/controllers/connection-test';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.test';
const TOKEN = 'op-token-123';

// Set up env vars for resolveAdminConfig
beforeEach(() => {
  process.env.ADMIN_API_BASE_URL = BASE;
  process.env.ADMIN_API_OPERATOR_TOKEN = TOKEN;
  nock.disableNetConnect();
});

afterEach(() => {
  delete process.env.ADMIN_API_BASE_URL;
  delete process.env.ADMIN_API_OPERATOR_TOKEN;
  nock.cleanAll();
  nock.enableNetConnect();
});

/** nock scope requiring the operator bearer on EVERY intercepted request. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

/** Create a controller instance with the http-fetch shim for nock interception. */
function makeController() {
  return connectionTestController({ strapi: {} }, httpFetch);
}

/** Create a mock Koa-like context for the handler. */
function makeCtx(body: unknown) {
  return {
    request: { body },
    status: 200,
    body: undefined as unknown,
  };
}

describe('POST /rule-engine/connections/test', () => {
  it('returns 200 with success:true on successful connection test', async () => {
    const scope = engine()
      .post('/admin/connections/test', (b: unknown) => {
        const body = b as Record<string, unknown>;
        expect(body.type).toBe('postgres');
        expect(body.settings).toEqual({ host: 'localhost', port: 5432, database: 'test' });
        expect(body.secret).toBe('my-password');
        return true;
      })
      .reply(200, { success: true, message: 'Connection successful' });

    const controller = makeController();
    const ctx = makeCtx({
      type: 'postgres',
      settings: { host: 'localhost', port: 5432, database: 'test' },
      secret: 'my-password',
    });

    await controller.testConnection(ctx);

    expect(ctx.status).toBe(200);
    expect(ctx.body).toEqual({ success: true, message: 'Connection successful' });
    scope.done();
  });

  it('returns 200 with success:false on failed connection test', async () => {
    const scope = engine()
      .post('/admin/connections/test')
      .reply(200, { success: false, error: 'connection refused' });

    const controller = makeController();
    const ctx = makeCtx({
      type: 'postgres',
      settings: { host: 'localhost', port: 5432, database: 'test' },
      secret: 'wrong-password',
    });

    await controller.testConnection(ctx);

    expect(ctx.status).toBe(200);
    expect(ctx.body).toEqual({ success: false, error: 'connection refused' });
    scope.done();
  });

  it('returns 400 when type is missing', async () => {
    const controller = makeController();
    const ctx = makeCtx({
      settings: { host: 'localhost' },
      secret: 'password',
    });

    await controller.testConnection(ctx);

    expect(ctx.status).toBe(400);
    expect(ctx.body).toEqual({ error: 'Missing or invalid "type" field' });
  });

  it('returns 400 when settings is missing', async () => {
    const controller = makeController();
    const ctx = makeCtx({
      type: 'postgres',
      secret: 'password',
    });

    await controller.testConnection(ctx);

    expect(ctx.status).toBe(400);
    expect(ctx.body).toEqual({ error: 'Missing or invalid "settings" field' });
  });

  it('returns 400 when secret is missing', async () => {
    const controller = makeController();
    const ctx = makeCtx({
      type: 'postgres',
      settings: { host: 'localhost' },
    });

    await controller.testConnection(ctx);

    expect(ctx.status).toBe(400);
    expect(ctx.body).toEqual({ error: 'Missing or invalid "secret" field' });
  });

  it('returns 503 (recoverable) when engine returns 5xx', async () => {
    const scope = engine()
      .post('/admin/connections/test')
      .reply(500, { error: 'internal error' });

    const controller = makeController();
    const ctx = makeCtx({
      type: 'postgres',
      settings: { host: 'localhost' },
      secret: 'password',
    });

    await controller.testConnection(ctx);

    expect(ctx.status).toBe(503);
    expect(ctx.body).toEqual({
      error: 'admin request POST /admin/connections/test returned 500',
      recoverable: true,
    });
    scope.done();
  });

  it('echoes 4xx status (non-recoverable) from engine', async () => {
    const scope = engine()
      .post('/admin/connections/test')
      .reply(400, { error: 'unknown type' });

    const controller = makeController();
    const ctx = makeCtx({
      type: 'unknown-type',
      settings: { host: 'localhost' },
      secret: 'password',
    });

    await controller.testConnection(ctx);

    expect(ctx.status).toBe(400);
    expect(ctx.body).toEqual({
      error: 'admin request POST /admin/connections/test returned 400',
      recoverable: false,
    });
    scope.done();
  });

  it('returns 503 when ADMIN_API_BASE_URL is not configured', async () => {
    delete process.env.ADMIN_API_BASE_URL;

    const controller = makeController();
    const ctx = makeCtx({
      type: 'postgres',
      settings: { host: 'localhost' },
      secret: 'password',
    });

    await controller.testConnection(ctx);

    expect(ctx.status).toBe(503);
    expect(ctx.body).toEqual({
      error: 'ADMIN_API_BASE_URL (or Environment.adminApiBaseUrl) is required',
      recoverable: false,
    });
  });
});
