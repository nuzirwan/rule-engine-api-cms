import { errors } from '@strapi/utils';

/**
 * Validates that secretRefs is null, undefined, or a {string: string} map.
 * Throws a ValidationError if the shape is invalid.
 */
export function validateSecretRefs(refs: unknown): void {
  if (refs == null) {
    return;
  }
  if (typeof refs !== 'object' || Array.isArray(refs)) {
    throw new errors.ValidationError(
      'secretRefs must be an object (map of string keys to string values)'
    );
  }
  // Object.entries always yields string keys, so only the value type needs checking.
  for (const [key, value] of Object.entries(refs as Record<string, unknown>)) {
    if (typeof value !== 'string') {
      throw new errors.ValidationError(
        `secretRefs value for key "${key}" must be a string, got ${typeof value}`
      );
    }
  }
}

/**
 * Connection secret-denylist guard (design §3.3.1).
 *
 * The CMS-side superset of the engine's edge guard: it rejects any key in a
 * Connection's `settings` whose name matches the case-insensitive denylist
 * below. This is the first of the two secret guards (the publish-transform in
 * FEAT-003 is the second). Settings must only ever carry the credential-free
 * discrete driver shape; a secret value belongs in `secretRef` as a reference,
 * never inline, and a `dsn` is a blanket reject because the CMS authors the
 * discrete shape instead.
 */
export const SECRET_DENYLIST = [
  'password',
  'pwd',
  'secret',
  'token',
  'apikey',
  'dsn',
] as const;

/**
 * Walks a settings value (object/array, arbitrarily nested) and returns the
 * first offending key that matches the denylist (case-insensitive), or null if
 * clean. Matching is on the exact key name lower-cased, so `apiKey` -> `apikey`
 * is caught while unrelated keys like `apikeyId` are not.
 */
export function findDeniedSecretKey(settings: unknown): string | null {
  const denied = new Set<string>(SECRET_DENYLIST);

  const walk = (value: unknown): string | null => {
    if (Array.isArray(value)) {
      for (const item of value) {
        const hit = walk(item);
        if (hit) return hit;
      }
      return null;
    }
    if (value !== null && typeof value === 'object') {
      for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
        if (denied.has(key.toLowerCase())) {
          return key;
        }
        const hit = walk(child);
        if (hit) return hit;
      }
    }
    return null;
  };

  return walk(settings);
}

/**
 * Throws a ValidationError naming the offending key when the Connection's
 * `settings` carries a denied secret key. No-op when settings is clean.
 */
export function assertNoSecretInSettings(data: { settings?: unknown } | undefined): void {
  if (!data || data.settings == null) {
    return;
  }
  const offending = findDeniedSecretKey(data.settings);
  if (offending) {
    throw new errors.ValidationError(
      `Connection settings must not contain the secret key "${offending}". ` +
        `Use a reference in secretRef instead (denied keys, case-insensitive: ${SECRET_DENYLIST.join(', ')}).`
    );
  }
}

export default {
  beforeCreate(event: { params: { data: { settings?: unknown; secretRefs?: unknown } } }) {
    assertNoSecretInSettings(event.params.data);
    validateSecretRefs(event.params.data.secretRefs);
  },
  beforeUpdate(event: { params: { data: { settings?: unknown; secretRefs?: unknown } } }) {
    assertNoSecretInSettings(event.params.data);
    validateSecretRefs(event.params.data.secretRefs);
  },
};
