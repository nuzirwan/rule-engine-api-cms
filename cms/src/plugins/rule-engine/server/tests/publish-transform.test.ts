import { describe, expect, it } from 'vitest';

import {
  TransformError,
  connectionToEnginePayload,
  flowToEnginePayload,
  groupToEnginePayload,
  isValidCron,
  isValidTimezone,
  jdmToEnginePayload,
  resilienceToEngine,
  scheduleToEnginePayload,
  webhookToEnginePayload,
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

// §6.1 — FEAT-001 transforms: webhook array->map, group seconds->duration-string
// + id-strip + connections->keys, and the cron/timezone validators.
describe('§6.1 webhookToEnginePayload', () => {
  const base = {
    webhookId: 'wh-stripe',
    name: 'Stripe Payment',
    secretRef: 'env:STRIPE_SECRET',
    provider: 'stripe' as const,
    flowId: 'payment-flow',
  };

  it('maps webhookId->id and arrays to MAP-shaped mapping/filter', () => {
    const payload = webhookToEnginePayload({
      ...base,
      mapping: [
        { sourceJsonPath: '$.data.object.id', targetContextPath: 'payment_id' },
        { sourceJsonPath: '$.data.object.amount', targetContextPath: 'amount' },
      ],
      filter: [{ jsonPath: '$.type', allowedValues: ['payment_intent.succeeded', 'charge.refunded'] }],
    });

    expect(payload.id).toBe('wh-stripe');
    expect(payload).not.toHaveProperty('webhookId');
    expect(payload.mapping).toEqual({
      '$.data.object.id': 'payment_id',
      '$.data.object.amount': 'amount',
    });
    expect(payload.filter).toEqual({ '$.type': ['payment_intent.succeeded', 'charge.refunded'] });
    expect(payload.secretRef).toBe('env:STRIPE_SECRET');
  });

  it('omits mapping/filter/secretRef when absent/empty', () => {
    const payload = webhookToEnginePayload({ ...base, secretRef: null, mapping: [], filter: null });
    expect(payload).not.toHaveProperty('mapping');
    expect(payload).not.toHaveProperty('filter');
    expect(payload).not.toHaveProperty('secretRef');
  });

  it('throws TransformError when flowId is empty', () => {
    expect(() => webhookToEnginePayload({ ...base, flowId: '' })).toThrow(TransformError);
  });
});

describe('§6.1 groupToEnginePayload', () => {
  const base = {
    groupId: 'orders',
    name: 'Orders Group',
    scalingMode: 'dynamic' as const,
    minReplicas: 1,
    maxReplicas: 5,
    scaleDownDelaySeconds: 300,
    startupTimeoutSeconds: 30,
  };

  it('nests scaling with mode, seconds->Go duration strings', () => {
    const { groupId, body } = groupToEnginePayload(base);
    expect(groupId).toBe('orders');
    expect(body.scaling.mode).toBe('dynamic');
    expect(body.scaling.minReplicas).toBe(1);
    expect(body.scaling.maxReplicas).toBe(5);
    expect(body.scaling.scaleDownDelay).toBe('300s');
    expect(body.scaling.startupTimeout).toBe('30s');
    // field is `mode`, not scalingMode.
    expect(body.scaling).not.toHaveProperty('scalingMode');
  });

  it('strips Strapi id/__component from the resources component', () => {
    const { body } = groupToEnginePayload({
      ...base,
      resources: {
        id: 42,
        __component: 'config.resource-limits',
        cpuRequest: '100m',
        cpuLimit: '500m',
        memoryRequest: '128Mi',
        memoryLimit: '512Mi',
      },
    });
    expect(body.scaling.resources).toEqual({
      cpuRequest: '100m',
      cpuLimit: '500m',
      memoryRequest: '128Mi',
      memoryLimit: '512Mi',
    });
    expect(body.scaling.resources).not.toHaveProperty('id');
    expect(body.scaling.resources).not.toHaveProperty('__component');
  });

  it('collapses the connections relation to a string[] of keys', () => {
    const { body } = groupToEnginePayload({
      ...base,
      connections: [{ key: 'fmc-pg' }, { key: 'redis-cache' }],
    });
    expect(body.connections).toEqual(['fmc-pg', 'redis-cache']);
  });

  it('throws TransformError when maxReplicas < minReplicas', () => {
    expect(() => groupToEnginePayload({ ...base, minReplicas: 5, maxReplicas: 2 })).toThrow(
      TransformError
    );
  });

  it('throws TransformError when static mode has minReplicas < 1', () => {
    expect(() =>
      groupToEnginePayload({ ...base, scalingMode: 'static', minReplicas: 0, maxReplicas: 3 })
    ).toThrow(TransformError);
  });
});

describe('§6.1 scheduleToEnginePayload + cron/timezone validators', () => {
  const base = {
    scheduleId: 'nightly',
    name: 'Nightly Job',
    schedule: '0 0 * * *',
    timezone: 'Asia/Jakarta',
    flowId: 'cleanup-flow',
  };

  it('maps the schedule, defaulting an empty timezone to UTC', () => {
    const payload = scheduleToEnginePayload({ ...base, timezone: null });
    expect(payload.id).toBe('nightly');
    expect(payload.timezone).toBe('UTC');
    expect(payload.schedule).toBe('0 0 * * *');
    expect(payload.flowId).toBe('cleanup-flow');
  });

  it('throws on empty flowId, invalid cron, and non-IANA timezone', () => {
    expect(() => scheduleToEnginePayload({ ...base, flowId: '' })).toThrow(TransformError);
    expect(() => scheduleToEnginePayload({ ...base, schedule: 'not-a-cron' })).toThrow(TransformError);
    expect(() => scheduleToEnginePayload({ ...base, schedule: '0 0 * *' })).toThrow(TransformError);
    expect(() => scheduleToEnginePayload({ ...base, timezone: 'Mars/Phobos ' })).not.toThrow();
    expect(() => scheduleToEnginePayload({ ...base, timezone: 'not a zone' })).toThrow(TransformError);
  });

  it('isValidCron accepts 5-field, @aliases, and @every', () => {
    expect(isValidCron('0 0 * * *')).toBe(true);
    expect(isValidCron('*/5 * * * *')).toBe(true);
    expect(isValidCron('0 9-17 * * 1-5')).toBe(true);
    expect(isValidCron('@daily')).toBe(true);
    expect(isValidCron('@midnight')).toBe(true);
    expect(isValidCron('@every 1h30m')).toBe(true);
    expect(isValidCron('@every 30s')).toBe(true);
  });

  it('isValidCron rejects malformed expressions', () => {
    expect(isValidCron('')).toBe(false);
    expect(isValidCron('0 0 * *')).toBe(false); // 4 fields
    expect(isValidCron('0 0 * * * *')).toBe(false); // 6 fields
    expect(isValidCron('@bogus')).toBe(false);
    expect(isValidCron('@every notaduration')).toBe(false);
    expect(isValidCron('abc def ghi jkl mno')).toBe(false);
  });

  it('isValidTimezone accepts UTC and Region/City, rejects junk', () => {
    expect(isValidTimezone('UTC')).toBe(true);
    expect(isValidTimezone('Asia/Jakarta')).toBe(true);
    expect(isValidTimezone('America/Argentina/Salta')).toBe(true);
    expect(isValidTimezone('')).toBe(false);
    expect(isValidTimezone('not a zone')).toBe(false);
    expect(isValidTimezone('Jakarta')).toBe(false);
  });
});
