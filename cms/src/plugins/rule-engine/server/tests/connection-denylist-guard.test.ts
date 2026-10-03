import { describe, expect, it } from 'vitest';

import {
  SECRET_DENYLIST,
  assertNoSecretInSettings,
  findDeniedSecretKey,
} from '../../../../api/connection/content-types/connection/lifecycles';

// The Connection secret-denylist guard (design §3.3.1). This is the CMS-side
// superset of the engine edge guard; it rejects any settings key matching the
// case-insensitive denylist password/pwd/secret/token/apikey/dsn.
describe('connection secret-denylist guard', () => {
  it('exposes the exact denylist from design §3.3.1', () => {
    expect([...SECRET_DENYLIST]).toEqual([
      'password',
      'pwd',
      'secret',
      'token',
      'apikey',
      'dsn',
    ]);
  });

  it('passes a clean credential-free postgres settings object', () => {
    const clean = {
      host: 'db.internal',
      port: 5432,
      database: 'fmc',
      user: 'app',
      sslmode: 'require',
      pool: { maxConns: 10, minConns: 1 },
    };
    expect(findDeniedSecretKey(clean)).toBeNull();
    expect(() => assertNoSecretInSettings({ settings: clean })).not.toThrow();
  });

  it.each([
    ['password', { host: 'db', password: 'hunter2' }],
    ['pwd', { host: 'db', pwd: 'hunter2' }],
    ['secret', { secret: 'x' }],
    ['token', { token: 'x' }],
    ['apikey', { apikey: 'x' }],
    ['dsn', { dsn: 'postgres://root:root@db/fmc' }],
  ])('rejects a settings object containing %s', (key, settings) => {
    expect(findDeniedSecretKey(settings)).toBe(key);
    expect(() => assertNoSecretInSettings({ settings })).toThrow(key);
  });

  it('matches denied keys case-insensitively', () => {
    expect(findDeniedSecretKey({ Password: 'x' })).toBe('Password');
    expect(findDeniedSecretKey({ ApiKey: 'x' })).toBe('ApiKey');
    expect(findDeniedSecretKey({ DSN: 'x' })).toBe('DSN');
  });

  it('catches a denied key nested inside the settings tree', () => {
    const nested = {
      baseURL: 'https://api.example.com',
      headers: { Authorization: 'Bearer x', apiKey: 'leak' },
    };
    expect(findDeniedSecretKey(nested)).toBe('apiKey');
    expect(() => assertNoSecretInSettings({ settings: nested })).toThrow('apiKey');
  });

  it('catches a denied key nested inside an array', () => {
    const nested = { entries: [{ ok: 1 }, { token: 'leak' }] };
    expect(findDeniedSecretKey(nested)).toBe('token');
  });

  it('does not flag non-secret keys that merely contain a denied substring', () => {
    const ok = { apikeyId: 'ref-1', tokenizer: true, passwordless: true };
    expect(findDeniedSecretKey(ok)).toBeNull();
  });

  it('no-ops when settings is absent or null', () => {
    expect(() => assertNoSecretInSettings(undefined)).not.toThrow();
    expect(() => assertNoSecretInSettings({})).not.toThrow();
    expect(() => assertNoSecretInSettings({ settings: null })).not.toThrow();
  });
});
