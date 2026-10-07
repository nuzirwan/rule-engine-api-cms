// Pure diff logic for comparing two JSON trees.
// No React dependencies — can be unit tested in node environment.

/** A single difference entry in the diff result. */
export interface DiffEntry {
  /** Dot-notation path to the changed value (e.g., "tree.children.0.spec.timeout"). */
  path: string;
  /** Type of change. */
  type: 'added' | 'removed' | 'changed';
  /** Previous value (undefined for 'added'). */
  oldValue?: unknown;
  /** New value (undefined for 'removed'). */
  newValue?: unknown;
}

/**
 * Compare two JSON-like objects and return a list of differences.
 * Performs recursive comparison with support for nested objects and arrays.
 */
export function jsonDiff(a: unknown, b: unknown, basePath = ''): DiffEntry[] {
  const entries: DiffEntry[] = [];

  // Handle primitives and null
  if (a === b) {
    return entries;
  }

  const aType = getType(a);
  const bType = getType(b);

  // Type mismatch or primitive change
  if (aType !== bType) {
    if (a === undefined) {
      entries.push({ path: basePath || '(root)', type: 'added', newValue: b });
    } else if (b === undefined) {
      entries.push({ path: basePath || '(root)', type: 'removed', oldValue: a });
    } else {
      entries.push({ path: basePath || '(root)', type: 'changed', oldValue: a, newValue: b });
    }
    return entries;
  }

  // Both are arrays
  if (aType === 'array') {
    const aArr = a as unknown[];
    const bArr = b as unknown[];
    const maxLen = Math.max(aArr.length, bArr.length);

    for (let i = 0; i < maxLen; i++) {
      const itemPath = basePath ? `${basePath}[${i}]` : `[${i}]`;
      if (i >= aArr.length) {
        entries.push({ path: itemPath, type: 'added', newValue: bArr[i] });
      } else if (i >= bArr.length) {
        entries.push({ path: itemPath, type: 'removed', oldValue: aArr[i] });
      } else {
        entries.push(...jsonDiff(aArr[i], bArr[i], itemPath));
      }
    }
    return entries;
  }

  // Both are objects
  if (aType === 'object') {
    const aObj = a as Record<string, unknown>;
    const bObj = b as Record<string, unknown>;
    const allKeys = new Set([...Object.keys(aObj), ...Object.keys(bObj)]);

    for (const key of allKeys) {
      const keyPath = basePath ? `${basePath}.${key}` : key;
      if (!(key in aObj)) {
        entries.push({ path: keyPath, type: 'added', newValue: bObj[key] });
      } else if (!(key in bObj)) {
        entries.push({ path: keyPath, type: 'removed', oldValue: aObj[key] });
      } else {
        entries.push(...jsonDiff(aObj[key], bObj[key], keyPath));
      }
    }
    return entries;
  }

  // Primitive value change
  if (a !== b) {
    entries.push({ path: basePath || '(root)', type: 'changed', oldValue: a, newValue: b });
  }

  return entries;
}

/**
 * Get the type of a value for comparison purposes.
 */
function getType(value: unknown): 'null' | 'undefined' | 'array' | 'object' | 'primitive' {
  if (value === null) return 'null';
  if (value === undefined) return 'undefined';
  if (Array.isArray(value)) return 'array';
  if (typeof value === 'object') return 'object';
  return 'primitive';
}

/**
 * Format a value for display in the diff view.
 */
export function formatValue(value: unknown): string {
  if (value === undefined) return 'undefined';
  if (value === null) return 'null';
  if (typeof value === 'string') return `"${value}"`;
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  }
  return String(value);
}

/**
 * Group diff entries by their top-level path segment for organized display.
 */
export function groupDiffsBySection(diffs: DiffEntry[]): Map<string, DiffEntry[]> {
  const groups = new Map<string, DiffEntry[]>();

  for (const diff of diffs) {
    const section = diff.path.split(/[.\[]/)[0] || '(root)';
    const existing = groups.get(section) ?? [];
    existing.push(diff);
    groups.set(section, existing);
  }

  return groups;
}
