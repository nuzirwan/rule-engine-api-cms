// parseStored.ts — the PURE "parse-or-fallback" helper shared by both custom
// fields (design §4: "if a stored JSON value fails to parse, the field shows a
// raw-JSON fallback editor and an error, never a blank/broken canvas").
//
// This module has NO React import so the §6.4 smoke test can exercise the
// parse/fallback decision directly, without mounting the heavy jdm-editor /
// reactflow runtimes.

/** The outcome of trying to coerce a Strapi custom-field value into usable JSON. */
export interface ParsedStored<T> {
  /** true when `value` is a usable parsed object; false when we fell back. */
  ok: boolean;
  /** the parsed value when ok; undefined on fallback. */
  value?: T;
  /** the raw string to show in the raw-JSON fallback editor when !ok. */
  raw: string;
  /** a human-readable parse error when !ok. */
  error?: string;
}

/**
 * Coerce a Strapi-provided custom-field `value` into a usable JSON object.
 *
 * Strapi persists a base `type:'json'` custom field as the parsed value, but a
 * value can arrive as:
 *   - `null`/`undefined` (a fresh, never-saved entry) → treated as "empty", ok:true
 *     with the supplied `emptyValue` so the editor opens blank-but-valid;
 *   - an already-parsed object/array (the normal persisted path) → ok:true;
 *   - a JSON string (hand-edited, or an engine-returned raw value) → parsed; ok on
 *     success, fallback on failure;
 *   - a malformed string / non-JSON → ok:false with the raw text + error so the
 *     field renders the raw-JSON fallback instead of crashing.
 */
export function parseStoredJson<T = unknown>(value: unknown, emptyValue: T): ParsedStored<T> {
  if (value === null || value === undefined || value === '') {
    return { ok: true, value: emptyValue, raw: safeStringify(emptyValue) };
  }

  // Already a parsed object/array — the normal persisted path for type:'json'.
  if (typeof value === 'object') {
    return { ok: true, value: value as T, raw: safeStringify(value) };
  }

  // A string: try to parse it as JSON.
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value) as T;
      // A bare scalar ("1", "true", "\"x\"") parses but is not an editable graph/
      // tree object — treat anything that isn't an object/array as a fallback so
      // the author sees the raw text rather than a broken editor.
      if (parsed === null || typeof parsed !== 'object') {
        return {
          ok: false,
          raw: value,
          error: 'Stored value is not a JSON object',
        };
      }
      return { ok: true, value: parsed, raw: value };
    } catch (err) {
      return {
        ok: false,
        raw: value,
        error: `Invalid JSON: ${(err as Error).message}`,
      };
    }
  }

  // A number/boolean/etc. — not a usable graph; show it raw.
  return {
    ok: false,
    raw: safeStringify(value),
    error: 'Stored value is not a JSON object',
  };
}

/** Stringify that never throws (circular refs fall back to String()). */
export function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value ?? null, null, 2);
  } catch {
    return String(value);
  }
}
