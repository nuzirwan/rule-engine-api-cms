// §6.3 — integration: the ordered, publish-blocking sequence (validation.ts)
// driven through the real AdminClient, with the admin HTTP API mocked by
// **nock**. The AdminClient is given the http-based fetch shim (see
// fixtures/http-fetch.ts) so nock intercepts and asserts every request.
//
// Every interceptor asserts the FLAT body (env top-level, NO flow/connection
// wrapper) and the `Authorization: Bearer <token>` header. The three mandated
// blocking gates each assert ZERO publishFlow hits (and the secret gate asserts
// ZERO admin HTTP calls at all).

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import nock from 'nock';

import { AdminClient, AdminApiError } from '../src/services/admin-client';
import {
  PublishBlockedError,
  runPublishSequence,
  connectionNeedsCreate,
} from '../src/services/validation';
import { TransformError } from '../src/services/publish-transform';
import { httpFetch } from './fixtures/http-fetch';
import {
  fmcOrderByIdFlow,
  fmcPaymentJdm,
  fmcPgConnection,
} from './fixtures/seed-entries';

const BASE = 'http://engine.test';
const TOKEN = 'op-token-123';

/** Build a client wired to the http-fetch shim so nock sees the traffic. */
function makeClient(payloadEnv = ''): AdminClient {
  return new AdminClient({ baseUrl: BASE, token: TOKEN, payloadEnv }, httpFetch);
}

/** nock scope requiring the operator bearer on EVERY intercepted request. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

/** A single-flow publish input: fmc-order-by-id + its jdm + its connection. */
function input() {
  return {
    flow: fmcOrderByIdFlow,
    jdms: [fmcPaymentJdm],
    connections: [fmcPgConnection],
  };
}

/** Assert a body is flat: env top-level, no flow/connection wrapper. */
function assertFlat(body: any): boolean {
  expect(body).not.toHaveProperty('flow');
  expect(body).not.toHaveProperty('connection');
  expect(body.env).toBe('');
  return true;
}

beforeEach(() => {
  nock.disableNetConnect();
});

afterEach(() => {
  nock.cleanAll();
  nock.enableNetConnect();
});

describe('§6.3 happy path — exact order, flat bodies, bearer on every call', () => {
  it('listConnections -> createConnection -> createJdm -> createFlow -> validateFlow(stored) -> publishFlow', async () => {
    const calls: string[] = [];

    const sList = engine()
      .get('/admin/connections')
      .reply(200, function () {
        calls.push('listConnections');
        return { connections: [] }; // empty => fmc-pg is missing => must create
      });

    const sConn = engine()
      .post('/admin/connections', (b: any) => {
        calls.push('createConnection');
        assertFlat(b);
        expect(b.key).toBe('fmc-pg');
        expect(b.type).toBe('postgres');
        expect(b.secretRef).toBe('env:FMC_PG_PASSWORD');
        // Go-cased ns resilience on the wire.
        expect(b.resilience).toEqual({ Timeout: 2_000_000_000, Retry: { MaxAttempts: 1 } });
        return true;
      })
      .reply(201, { key: 'fmc-pg', version: 1 });

    const sJdm = engine()
      .post('/admin/jdms', (b: any) => {
        calls.push('createJdm');
        assertFlat(b);
        expect(b.jdmId).toBe('fmc-payment');
        expect(b.version).toBe(0); // always 0
        expect(b).not.toHaveProperty('flowId');
        return true;
      })
      .reply(201, { jdmId: 'fmc-payment', version: 5 });

    const sFlow = engine()
      .post('/admin/flows', (b: any) => {
        calls.push('createFlow');
        assertFlat(b);
        expect(b.flowId).toBe('fmc-order-by-id');
        expect(b.method).toBe('GET');
        expect(b.path).toBe('/order/{order_id}');
        expect(b).toHaveProperty('tree');
        expect(b).not.toHaveProperty('version'); // version assigned by store
        return true;
      })
      .reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });

    const sValidate = engine()
      .post('/admin/flows/validate', (b: any) => {
        calls.push('validateFlow');
        assertFlat(b);
        // STORED mode: exactly {env, flowId, version} — no inline flow.
        expect(Object.keys(b).sort()).toEqual(['env', 'flowId', 'version']);
        expect(b.flowId).toBe('fmc-order-by-id');
        expect(b.version).toBe(7);
        return true;
      })
      .reply(200, { ok: true, structural: [], fixtures: [] });

    const sPublish = engine()
      .post('/admin/flows/fmc-order-by-id/publish', (b: any) => {
        calls.push('publishFlow');
        assertFlat(b);
        expect(Object.keys(b).sort()).toEqual(['env', 'version']);
        expect(b.version).toBe(7);
        return true;
      })
      .reply(200, { flowId: 'fmc-order-by-id', activeVersion: 7, action: 'publish' });

    const writeBack = await runPublishSequence(makeClient(), input());

    // exact ordering.
    expect(calls).toEqual([
      'listConnections',
      'createConnection',
      'createJdm',
      'createFlow',
      'validateFlow',
      'publishFlow',
    ]);

    // write-backs read DISTINCT response keys (finding 4).
    expect(writeBack.flowEngineVersion).toBe(7); // createFlow().version
    expect(writeBack.jdmEngineVersions!['fmc-payment']).toBe(5); // createJdm().version
    expect(writeBack.activeVersion).toBe(7); // publishFlow().activeVersion
    expect(writeBack.lastPublishStatus).toBe('published');

    for (const s of [sList, sConn, sJdm, sFlow, sValidate, sPublish]) {
      expect(s.isDone()).toBe(true);
    }
  });
});

describe('§6.3 BLOCKING gate — validation ok:false blocks publish', () => {
  it('validateFlow 200 {ok:false} => publishFlow NEVER called, lastPublishStatus=failed, diff stored', async () => {
    engine().get('/admin/connections').reply(200, { connections: [] });
    engine().post('/admin/connections').reply(201, { key: 'fmc-pg', version: 1 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });
    engine()
      .post('/admin/flows/validate')
      .reply(200, {
        ok: false,
        structural: [],
        fixtures: [{ name: 'asserts', passed: false, diff: { field: 'x', expected: 1, actual: 2 } }],
      });
    // publish interceptor asserts ZERO hits.
    const sPublish = engine().post('/admin/flows/fmc-order-by-id/publish').reply(200, {});

    let err: unknown;
    try {
      await runPublishSequence(makeClient(), input());
    } catch (e) {
      err = e;
    }

    expect(err).toBeInstanceOf(PublishBlockedError);
    expect((err as PublishBlockedError).stage).toBe('validateFlow');
    expect(sPublish.isDone()).toBe(false); // ZERO publish hits
    expect(nock.pendingMocks().some((m) => m.includes('/publish'))).toBe(true);
  });
});

describe('§6.3 BLOCKING gate — validator unreachable (503)', () => {
  it('validateFlow 503 => blocked, publish never called, recoverable error', async () => {
    engine().get('/admin/connections').reply(200, { connections: [] });
    engine().post('/admin/connections').reply(201, { key: 'fmc-pg', version: 1 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });
    engine().post('/admin/flows/validate').reply(503, { error: 'store unavailable' });
    const sPublish = engine().post('/admin/flows/fmc-order-by-id/publish').reply(200, {});

    let err: unknown;
    try {
      await runPublishSequence(makeClient(), input());
    } catch (e) {
      err = e;
    }

    expect(err).toBeInstanceOf(PublishBlockedError);
    expect((err as PublishBlockedError).stage).toBe('validateFlow');
    expect((err as PublishBlockedError).recoverable).toBe(true); // 5xx retry-recoverable
    expect(sPublish.isDone()).toBe(false);
  });
});

describe('§6.3 BLOCKING gate — inline secret: ZERO admin HTTP calls', () => {
  it('a settings secret makes the transform throw BEFORE any admin call', async () => {
    // Nothing is intercepted; if any HTTP call were made, nock.disableNetConnect
    // would make it fail loudly.
    const withSecret = {
      ...input(),
      connections: [
        {
          key: 'leaky',
          type: 'postgres',
          settings: { host: 'db', password: 'hunter2' },
          secretRef: '',
          resilience: { timeoutMs: 1000, retry: { maxAttempts: 1 } },
        },
      ],
    };

    let err: unknown;
    try {
      await runPublishSequence(makeClient(), withSecret);
    } catch (e) {
      err = e;
    }

    expect(err).toBeInstanceOf(TransformError);
    expect((err as Error).message).toContain('password');
    // ZERO admin HTTP calls: no interceptors were even registered.
    expect(nock.pendingMocks()).toHaveLength(0);
    expect(nock.activeMocks()).toHaveLength(0);
  });

  it('a seed-style dsn in settings is rejected before any admin call', async () => {
    const withDsn = {
      ...input(),
      connections: [
        {
          key: 'fmc-pg',
          type: 'postgres',
          settings: { dsn: 'postgres://root:root@127.0.0.1:5432/fmc_utility?sslmode=disable' },
          secretRef: '',
          resilience: { timeoutMs: 2000, retry: { maxAttempts: 1 } },
        },
      ],
    };

    await expect(runPublishSequence(makeClient(), withDsn)).rejects.toBeInstanceOf(TransformError);
    expect(nock.pendingMocks()).toHaveLength(0);
  });
});

describe('§6.3 validate 404 (version not found)', () => {
  it('stored-mode validate 404 => blocked, non-recoverable, publish never called', async () => {
    engine().get('/admin/connections').reply(200, { connections: [] });
    engine().post('/admin/connections').reply(201, { key: 'fmc-pg', version: 1 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });
    engine().post('/admin/flows/validate').reply(404, { error: 'not found' });
    const sPublish = engine().post('/admin/flows/fmc-order-by-id/publish').reply(200, {});

    let err: unknown;
    try {
      await runPublishSequence(makeClient(), input());
    } catch (e) {
      err = e;
    }

    expect(err).toBeInstanceOf(PublishBlockedError);
    const blocked = err as PublishBlockedError;
    expect(blocked.stage).toBe('validateFlow');
    expect(blocked.status).toBe(404);
    expect(blocked.recoverable).toBe(false); // re-create and retry (not an outage)
    expect(sPublish.isDone()).toBe(false);
  });
});

describe('§6.3 publish 422 (un-validated)', () => {
  it('publishFlow 422 => abort keyed to 422 (NOT 409)', async () => {
    engine().get('/admin/connections').reply(200, { connections: [] });
    engine().post('/admin/connections').reply(201, { key: 'fmc-pg', version: 1 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });
    engine().post('/admin/flows/validate').reply(200, { ok: true, structural: [], fixtures: [] });
    engine().post('/admin/flows/fmc-order-by-id/publish').reply(422, { error: 'flow version not validated' });

    let err: unknown;
    try {
      await runPublishSequence(makeClient(), input());
    } catch (e) {
      err = e;
    }

    expect(err).toBeInstanceOf(PublishBlockedError);
    const blocked = err as PublishBlockedError;
    expect(blocked.stage).toBe('publishFlow');
    expect(blocked.status).toBe(422); // keyed to 422, matching ErrUnvalidated
    expect(blocked.recoverable).toBe(false);
  });
});

describe('§6.3 create route collision 409', () => {
  it('createFlow 409 => abort, validate/publish never called, author-fixable', async () => {
    engine().get('/admin/connections').reply(200, { connections: [] });
    engine().post('/admin/connections').reply(201, { key: 'fmc-pg', version: 1 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(409, { error: 'route already owned by another flow' });
    const sValidate = engine().post('/admin/flows/validate').reply(200, {});
    const sPublish = engine().post('/admin/flows/fmc-order-by-id/publish').reply(200, {});

    let err: unknown;
    try {
      await runPublishSequence(makeClient(), input());
    } catch (e) {
      err = e;
    }

    expect(err).toBeInstanceOf(PublishBlockedError);
    const blocked = err as PublishBlockedError;
    expect(blocked.stage).toBe('createFlow');
    expect(blocked.status).toBe(409);
    expect(blocked.recoverable).toBe(false); // author-fixable, not a retry/outage
    expect(sValidate.isDone()).toBe(false);
    expect(sPublish.isDone()).toBe(false);
  });
});

describe('§6.3 under-privileged operator 403', () => {
  it('publishFlow 403 => abort, non-recoverable, no retry', async () => {
    engine().get('/admin/connections').reply(200, { connections: [] });
    engine().post('/admin/connections').reply(201, { key: 'fmc-pg', version: 1 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });
    engine().post('/admin/flows/validate').reply(200, { ok: true, structural: [], fixtures: [] });
    // a single interceptor: if the client retried, a second publish would 404 (no mock).
    engine().post('/admin/flows/fmc-order-by-id/publish').reply(403, { error: 'operator token lacks flow.publish' });

    let err: unknown;
    try {
      await runPublishSequence(makeClient(), input());
    } catch (e) {
      err = e;
    }

    expect(err).toBeInstanceOf(PublishBlockedError);
    const blocked = err as PublishBlockedError;
    expect(blocked.stage).toBe('publishFlow');
    expect(blocked.status).toBe(403);
    expect(blocked.recoverable).toBe(false);
    expect(nock.pendingMocks()).toHaveLength(0); // exactly one publish attempt
  });
});

describe('§6.3 create fails mid-sequence (500)', () => {
  it('createFlow 500 => validate/publish not called, failed', async () => {
    engine().get('/admin/connections').reply(200, { connections: [] });
    engine().post('/admin/connections').reply(201, { key: 'fmc-pg', version: 1 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(500, { error: 'internal' });
    const sValidate = engine().post('/admin/flows/validate').reply(200, {});
    const sPublish = engine().post('/admin/flows/fmc-order-by-id/publish').reply(200, {});

    let err: unknown;
    try {
      await runPublishSequence(makeClient(), input());
    } catch (e) {
      err = e;
    }

    expect(err).toBeInstanceOf(PublishBlockedError);
    expect((err as PublishBlockedError).stage).toBe('createFlow');
    expect(sValidate.isDone()).toBe(false);
    expect(sPublish.isDone()).toBe(false);
  });
});

describe('§6.3 connection reconcile — no spurious re-create', () => {
  it('skips createConnection when GET differs only by driver defaults + Go-cased ns resilience', async () => {
    const calls: string[] = [];
    // Existing fmc-pg carries extra driver-defaulted keys the author never set
    // (sslmode default, extra pool fields) and Go-cased ns resilience.
    engine()
      .get('/admin/connections')
      .reply(200, function () {
        calls.push('listConnections');
        return {
          connections: [
            {
              key: 'fmc-pg',
              type: 'postgres',
              settings: {
                host: '127.0.0.1',
                port: 5432,
                database: 'fmc_utility',
                user: 'app',
                sslmode: 'disable',
                // extra defaulted keys the author never authored:
                connect_timeout: 10,
                pool: { maxConns: 4, minConns: 1 },
              },
              secretRef: 'env:FMC_PG_PASSWORD',
              resilience: { Timeout: 2_000_000_000, Retry: { MaxAttempts: 1 } },
            },
          ],
        };
      });
    // createConnection must NOT be hit for fmc-pg.
    const sConn = engine().post('/admin/connections').reply(201, { key: 'fmc-pg', version: 2 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });
    engine().post('/admin/flows/validate').reply(200, { ok: true, structural: [], fixtures: [] });
    engine().post('/admin/flows/fmc-order-by-id/publish').reply(200, {
      flowId: 'fmc-order-by-id',
      activeVersion: 7,
      action: 'publish',
    });

    const writeBack = await runPublishSequence(makeClient(), input());

    expect(calls).toEqual(['listConnections']);
    expect(sConn.isDone()).toBe(false); // NOT re-created
    expect(writeBack.lastPublishStatus).toBe('published');
  });

  it('DOES re-create a connection whose maxConns genuinely changed', async () => {
    engine()
      .get('/admin/connections')
      .reply(200, {
        connections: [
          {
            key: 'fmc-pg',
            type: 'postgres',
            settings: {
              host: '127.0.0.1',
              port: 5432,
              database: 'fmc_utility',
              user: 'app',
              sslmode: 'disable',
              pool: { maxConns: 99, minConns: 1 }, // DIFFERENT from authored maxConns:4
            },
            secretRef: 'env:FMC_PG_PASSWORD',
            resilience: { Timeout: 2_000_000_000, Retry: { MaxAttempts: 1 } },
          },
        ],
      });
    const sConn = engine()
      .post('/admin/connections', (b: any) => {
        expect(b.key).toBe('fmc-pg');
        return true;
      })
      .reply(201, { key: 'fmc-pg', version: 2 });
    engine().post('/admin/jdms').reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows').reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });
    engine().post('/admin/flows/validate').reply(200, { ok: true, structural: [], fixtures: [] });
    engine().post('/admin/flows/fmc-order-by-id/publish').reply(200, {
      flowId: 'fmc-order-by-id',
      activeVersion: 7,
      action: 'publish',
    });

    await runPublishSequence(makeClient(), input());
    expect(sConn.isDone()).toBe(true); // re-created
  });
});

describe('§6.3 connectionNeedsCreate (reconcile unit)', () => {
  it('returns false when GET differs only by driver defaults + ns resilience', () => {
    const authored = {
      env: '',
      key: 'fmc-pg',
      type: 'postgres',
      settings: { host: '127.0.0.1', port: 5432, database: 'fmc_utility', user: 'app', sslmode: 'disable' },
      secretRef: 'env:FMC_PG_PASSWORD',
      resilience: { Timeout: 2_000_000_000, Retry: { MaxAttempts: 1 } },
    };
    const current = {
      key: 'fmc-pg',
      type: 'postgres',
      settings: {
        host: '127.0.0.1',
        port: 5432,
        database: 'fmc_utility',
        user: 'app',
        sslmode: 'disable',
        connect_timeout: 10, // defaulted extra key — ignored
      },
      secretRef: 'env:FMC_PG_PASSWORD',
      resilience: { Timeout: 2_000_000_000, Retry: { MaxAttempts: 1 }, Breaker: { OpenTimeout: 5 } },
    };
    expect(connectionNeedsCreate(authored, current)).toBe(false);
  });

  it('returns true when an authored key genuinely changed', () => {
    const authored = {
      env: '',
      key: 'fmc-pg',
      type: 'postgres',
      settings: { host: '127.0.0.1', database: 'fmc_utility' },
      secretRef: 'env:FMC_PG_PASSWORD',
      resilience: { Timeout: 2_000_000_000, Retry: { MaxAttempts: 1 } },
    };
    const current = {
      key: 'fmc-pg',
      type: 'postgres',
      settings: { host: '127.0.0.1', database: 'OTHER' },
      secretRef: 'env:FMC_PG_PASSWORD',
      resilience: { Timeout: 2_000_000_000, Retry: { MaxAttempts: 1 } },
    };
    expect(connectionNeedsCreate(authored, current)).toBe(true);
  });

  it('returns true when the key is absent from the engine', () => {
    const authored = {
      env: '',
      key: 'new-key',
      type: 'postgres',
      settings: {},
      secretRef: '',
      resilience: { Timeout: 0, Retry: { MaxAttempts: 1 } },
    };
    expect(connectionNeedsCreate(authored, undefined)).toBe(true);
  });
});

describe('§6.3 rollback', () => {
  it('rollbackFlow calls POST /admin/flows/{id}/rollback with {env:"", version}', async () => {
    const sRollback = engine()
      .post('/admin/flows/fmc-order-by-id/rollback', (b: any) => {
        assertFlat(b);
        expect(Object.keys(b).sort()).toEqual(['env', 'version']);
        expect(b.version).toBe(6);
        return true;
      })
      .reply(200, { flowId: 'fmc-order-by-id', activeVersion: 6, action: 'rollback' });

    const res = await makeClient().rollbackFlow('fmc-order-by-id', 6);
    expect(res.activeVersion).toBe(6);
    expect(res.action).toBe('rollback');
    expect(sRollback.isDone()).toBe(true);
  });
});

describe('§6.3 operator credential — bearer on every call, token never in body', () => {
  it('every intercepted request carries Authorization: Bearer and no body leaks the token', async () => {
    const bodies: unknown[] = [];
    const captureBody = (b: any) => {
      bodies.push(b);
      return true;
    };
    // reqheaders on engine() already enforces the bearer; a request missing it
    // would not match and nock would throw.
    engine().get('/admin/connections').reply(200, { connections: [] });
    engine().post('/admin/connections', captureBody).reply(201, { key: 'fmc-pg', version: 1 });
    engine().post('/admin/jdms', captureBody).reply(201, { jdmId: 'fmc-payment', version: 5 });
    engine().post('/admin/flows', captureBody).reply(201, { flowId: 'fmc-order-by-id', version: 7, validated: false });
    engine().post('/admin/flows/validate', captureBody).reply(200, { ok: true, structural: [], fixtures: [] });
    engine().post('/admin/flows/fmc-order-by-id/publish', captureBody).reply(200, {
      flowId: 'fmc-order-by-id',
      activeVersion: 7,
      action: 'publish',
    });

    await runPublishSequence(makeClient(), input());

    // the operator token must never appear in any request body.
    for (const b of bodies) {
      expect(JSON.stringify(b)).not.toContain(TOKEN);
    }
  });
});

describe('AdminApiError transport failure', () => {
  it('a connection error surfaces as a recoverable AdminApiError (status 0)', async () => {
    // listConnections hits an un-mocked host with net-connect disabled.
    const client = makeClient();
    let err: unknown;
    try {
      await client.listConnections();
    } catch (e) {
      err = e;
    }
    expect(err).toBeInstanceOf(AdminApiError);
    expect((err as AdminApiError).status).toBe(0);
    expect((err as AdminApiError).recoverable).toBe(true);
  });
});
