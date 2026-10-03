import { describe, expect, it } from 'vitest';

import {
  TransformError,
  connectionToEnginePayload,
  flowToEnginePayload,
  jdmToEnginePayload,
  resilienceToEngine,
} from '../src/services/publish-transform';
import {
  fmcOrderByIdFlow,
  ordersExpediteFlow,
  orderJdmDoc,
  fmcPaymentJdmDoc,
} from './fixtures/seed-entries';

// §6.1 — publish-transform PURE functions. Flat bodies (env top-level, no
// wrapper), mixed casing preserved, jdm version:0, Go-cased ns resilience,
// THROW on the secret denylist.
describe('§6.1 flowToEnginePayload', () => {
  it('emits a flat body with env top-level and NO flow wrapper or stray fields', () => {
    const body = flowToEnginePayload(fmcOrderByIdFlow, '');
    expect(Object.keys(body).sort()).toEqual(['env', 'flowId', 'method', 'path', 'tree']);
    expect(body.env).toBe('');
    expect((body as Record<string, unknown>).flow).toBeUndefined();
    expect((body as Record<string, unknown>).connection).toBeUndefined();
    expect((body as Record<string, unknown>).version).toBeUndefined();
  });

  it('reproduces the seed tree for fmc-order-by-id (structure round-trip)', () => {
    const body = flowToEnginePayload(fmcOrderByIdFlow, '');
    expect(body.tree).toEqual(fmcOrderByIdFlow.tree);
    expect(body.flowId).toBe('fmc-order-by-id');
    expect(body.method).toBe('GET');
    expect(body.path).toBe('/order/{order_id}');
  });

  it('preserves mixed spec casing for orders-expedite (operation.{Kind,Payload,Required} capitalized)', () => {
    const body = flowToEnginePayload(ordersExpediteFlow, '');
    // read-order action node carries the capitalized operation keys.
    const action = (body.tree.children ?? [])[0];
    const operation = action.spec.operation as Record<string, unknown>;
    expect(operation).toHaveProperty('Kind');
    expect(operation).toHaveProperty('Payload');
    expect(operation).toHaveProperty('Required');
    expect(operation).not.toHaveProperty('kind');
    // camelCase elsewhere: action spec uses connection/saveAs.
    expect(action.spec).toHaveProperty('connection');
    expect(action.spec).toHaveProperty('saveAs');
  });

  it('maps fixtures to the wire shape omitting absent mocks/expect', () => {
    const body = flowToEnginePayload(ordersExpediteFlow, '');
    expect(body.fixtures).toHaveLength(2);
    for (const f of body.fixtures ?? []) {
      expect(f).toHaveProperty('name');
      expect(f).toHaveProperty('input');
      // seed fixtures declare neither mocks nor expect — omitted, not empty objects.
      expect(f).not.toHaveProperty('mocks');
      expect(f).not.toHaveProperty('expect');
    }
  });

  it('omits the fixtures key entirely when the flow has none', () => {
    const body = flowToEnginePayload(fmcOrderByIdFlow, '');
    expect(body).not.toHaveProperty('fixtures');
  });

  it('keeps a present-only mocks/expect fixture field through the transform', () => {
    const withExpect = {
      ...ordersExpediteFlow,
      fixtures: [{ name: 'asserts', input: { id: 1 }, expect: { status: 200 } }],
    };
    const body = flowToEnginePayload(withExpect, '');
    expect(body.fixtures?.[0]).toEqual({ name: 'asserts', input: { id: 1 }, expect: { status: 200 } });
    expect(body.fixtures?.[0]).not.toHaveProperty('mocks');
  });
});

describe('§6.1 jdmToEnginePayload', () => {
  it('emits a flat {env,jdmId,doc,version:0} body and reproduces the seed graph (fmc-payment)', () => {
    const body = jdmToEnginePayload({ jdmId: 'fmc-payment', doc: fmcPaymentJdmDoc }, '');
    expect(Object.keys(body).sort()).toEqual(['doc', 'env', 'jdmId', 'version']);
    expect(body.env).toBe('');
    expect(body.jdmId).toBe('fmc-payment');
    expect(body.doc).toEqual(fmcPaymentJdmDoc);
    expect(body.version).toBe(0);
  });

  it('always sets version:0 (order graph too)', () => {
    const body = jdmToEnginePayload({ jdmId: 'order', doc: orderJdmDoc }, '');
    expect(body.version).toBe(0);
    expect(body.doc).toEqual(orderJdmDoc);
  });
});

describe('§6.1 connectionToEnginePayload', () => {
  const cleanPg = {
    key: 'fmc-pg',
    type: 'postgres',
    settings: {
      host: '127.0.0.1',
      port: 5432,
      database: 'fmc_utility',
      user: 'app',
      sslmode: 'disable',
      pool: { maxConns: 4, minConns: 1 },
    },
    secretRef: 'env:FMC_PG_PASSWORD',
    resilience: { timeoutMs: 2000, retry: { maxAttempts: 1 } },
  };

  it('emits a flat body with NO connection wrapper and secretRef passed through', () => {
    const body = connectionToEnginePayload(cleanPg, '');
    expect(Object.keys(body).sort()).toEqual([
      'env',
      'key',
      'resilience',
      'secretRef',
      'settings',
      'type',
    ]);
    expect((body as Record<string, unknown>).connection).toBeUndefined();
    expect(body.secretRef).toBe('env:FMC_PG_PASSWORD');
    expect(body.env).toBe('');
  });

  it('emits Go-cased ns resilience (timeoutMs ms -> Timeout ns, retry.maxAttempts -> Retry.MaxAttempts)', () => {
    const body = connectionToEnginePayload(cleanPg, '');
    expect(body.resilience).toEqual({ Timeout: 2_000_000_000, Retry: { MaxAttempts: 1 } });
  });

  it('resilienceToEngine converts ms->ns and defaults MaxAttempts to 1', () => {
    expect(resilienceToEngine({ timeoutMs: 500 })).toEqual({
      Timeout: 500_000_000,
      Retry: { MaxAttempts: 1 },
    });
  });

  it('THROWS a TransformError on a seed-style dsn connection', () => {
    const seedStyle = {
      key: 'fmc-pg',
      type: 'postgres',
      settings: { dsn: 'postgres://root:root@127.0.0.1:5432/fmc_utility?sslmode=disable' },
      secretRef: '',
      resilience: { timeoutMs: 2000, retry: { maxAttempts: 1 } },
    };
    expect(() => connectionToEnginePayload(seedStyle, '')).toThrow(TransformError);
    expect(() => connectionToEnginePayload(seedStyle, '')).toThrow('dsn');
  });

  it.each([
    ['password', { host: 'db', password: 'hunter2' }],
    ['pwd', { host: 'db', pwd: 'hunter2' }],
    ['secret', { secret: 'x' }],
    ['token', { token: 'x' }],
    ['apikey', { apikey: 'x' }],
  ])('THROWS when a denylist key (%s) appears in settings', (key, settings) => {
    const conn = { key: 'k', type: 'postgres', settings, secretRef: '', resilience: null };
    expect(() => connectionToEnginePayload(conn, '')).toThrow(TransformError);
    expect(() => connectionToEnginePayload(conn, '')).toThrow(key);
  });
});
