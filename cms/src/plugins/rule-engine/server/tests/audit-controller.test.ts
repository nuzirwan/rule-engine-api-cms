// Unit tests for the audit controller and the AuditViewer pure helpers.
//
// Controller tests use nock to intercept HTTP calls (the controller is passed
// the httpFetch shim so nock sees the traffic — same pattern as
// publish-sequence.test.ts). Pure-helper tests (labelForAction, relativeTime,
// versionDelta) run without any network or DOM.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import nock from 'nock';

import auditController from '../src/controllers/audit';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.audit-test';
const TOKEN = 'audit-token-xyz';

/** Build a minimal mock Strapi ctx. */
function makeCtx(params: Record<string, string>) {
  return {
    params,
    body: undefined as unknown,
    status: undefined as number | undefined,
    badRequest: vi.fn(),
    notFound: vi.fn(),
  };
}

/** Build a nock scope that requires the Bearer token on every call. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

// -----------------------------------------------------------------------
// Controller tests
// -----------------------------------------------------------------------

describe('audit controller', () => {
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

  it('returns badRequest for an invalid audit type', async () => {
    const ctrl = auditController({ strapi: {} as any }, httpFetch);
    const ctx = makeCtx({ type: 'workflow', id: '1' });
    await ctrl.audit(ctx);
    expect(ctx.badRequest).toHaveBeenCalledWith(
      expect.stringContaining('invalid audit type')
    );
    // No HTTP call should have been made.
    expect(nock.pendingMocks()).toHaveLength(0);
  });

  it('proxies the audit trail response on success (flow)', async () => {
    const payload = {
      objectType: 'flow',
      objectId: 'orders',
      entries: [
        {
          action: 'publish',
          fromVersion: 5,
          toVersion: 6,
          actor: 'op:alice',
          at: '2026-01-01T00:00:00Z',
          reason: '',
        },
      ],
    };
    engine().get('/admin/audit/flow/orders').reply(200, payload);

    const ctrl = auditController({ strapi: {} as any }, httpFetch);
    const ctx = makeCtx({ type: 'flow', id: 'orders' });
    await ctrl.audit(ctx);

    expect(ctx.badRequest).not.toHaveBeenCalled();
    expect(ctx.body).toEqual(payload);
  });

  it('accepts jdm and connection types', async () => {
    for (const type of ['jdm', 'connection']) {
      const payload = { objectType: type, objectId: 'x', entries: [] };
      engine().get(`/admin/audit/${type}/x`).reply(200, payload);

      const ctrl = auditController({ strapi: {} as any }, httpFetch);
      const ctx = makeCtx({ type, id: 'x' });
      await ctrl.audit(ctx);

      expect(ctx.badRequest).not.toHaveBeenCalled();
      expect(ctx.body).toEqual(payload);
    }
  });

  it('returns 503 and recoverable:true for a 5xx engine error', async () => {
    engine().get('/admin/audit/flow/orders').reply(503, { error: 'unavailable' });

    const ctrl = auditController({ strapi: {} as any }, httpFetch);
    const ctx = makeCtx({ type: 'flow', id: 'orders' });
    await ctrl.audit(ctx);

    expect(ctx.status).toBe(503);
    expect(ctx.body).toMatchObject({ recoverable: true });
  });

  it('returns 404 and recoverable:false for a 404 engine error', async () => {
    engine().get('/admin/audit/flow/missing').reply(404, { error: 'not found' });

    const ctrl = auditController({ strapi: {} as any }, httpFetch);
    const ctx = makeCtx({ type: 'flow', id: 'missing' });
    await ctrl.audit(ctx);

    expect(ctx.status).toBe(404);
    expect(ctx.body).toMatchObject({ recoverable: false });
  });

  it('returns 503 for a transport failure (no ADMIN_API_BASE_URL)', async () => {
    delete process.env.ADMIN_API_BASE_URL;
    nock.enableNetConnect(); // no outbound call expected

    const ctrl = auditController({ strapi: {} as any }, httpFetch);
    const ctx = makeCtx({ type: 'flow', id: 'orders' });
    await ctrl.audit(ctx);

    expect(ctx.status).toBe(503);
    expect(typeof (ctx.body as any)?.error).toBe('string');
  });
});

// -----------------------------------------------------------------------
// AuditViewer pure-helper tests (no React / jsdom needed)
// -----------------------------------------------------------------------

import {
  labelForAction,
  relativeTime,
  versionDelta,
} from '../../admin/src/components/AuditViewer/utils';

describe('labelForAction', () => {
  it('maps known action codes to readable labels', () => {
    expect(labelForAction('create_version')).toBe('Created version');
    expect(labelForAction('publish')).toBe('Published');
    expect(labelForAction('rollback')).toBe('Rolled back');
    expect(labelForAction('validate')).toBe('Validated');
  });

  it('returns unknown codes verbatim', () => {
    expect(labelForAction('custom_action')).toBe('custom_action');
  });
});

describe('versionDelta', () => {
  it('formats a full transition', () => {
    expect(versionDelta(5, 6)).toBe('v5 → v6');
  });

  it('formats a null from-version (initial create)', () => {
    expect(versionDelta(null, 1)).toBe('→ v1');
  });

  it('returns null when toVersion is null', () => {
    expect(versionDelta(null, null)).toBeNull();
    expect(versionDelta(1, null)).toBeNull();
  });
});

describe('relativeTime', () => {
  it('preserves the original ISO timestamp as the title', () => {
    const iso = '2026-01-01T00:00:00.000Z';
    const { title } = relativeTime(iso);
    expect(title).toBe(iso);
  });

  it('returns a non-empty label string', () => {
    const { label } = relativeTime(new Date(Date.now() - 3_600_000).toISOString());
    expect(typeof label).toBe('string');
    expect(label.length).toBeGreaterThan(0);
  });
});
