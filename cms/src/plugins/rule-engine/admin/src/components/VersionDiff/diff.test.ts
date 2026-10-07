// Unit tests for the jsonDiff function.
// Tests the pure diff logic with various tree shapes.

import { describe, expect, it } from 'vitest';

import { jsonDiff, formatValue, groupDiffsBySection, type DiffEntry } from './diff';

describe('jsonDiff', () => {
  describe('primitives', () => {
    it('returns empty array for identical primitives', () => {
      expect(jsonDiff(1, 1)).toEqual([]);
      expect(jsonDiff('foo', 'foo')).toEqual([]);
      expect(jsonDiff(true, true)).toEqual([]);
      expect(jsonDiff(null, null)).toEqual([]);
    });

    it('detects changed primitives', () => {
      const result = jsonDiff(1, 2);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: '(root)',
        type: 'changed',
        oldValue: 1,
        newValue: 2,
      });
    });

    it('detects type changes', () => {
      const result = jsonDiff(1, 'one');
      expect(result).toHaveLength(1);
      expect(result[0].type).toBe('changed');
    });

    it('detects added value from undefined', () => {
      const result = jsonDiff(undefined, 'new');
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: '(root)',
        type: 'added',
        newValue: 'new',
      });
    });

    it('detects removed value to undefined', () => {
      const result = jsonDiff('old', undefined);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: '(root)',
        type: 'removed',
        oldValue: 'old',
      });
    });
  });

  describe('objects', () => {
    it('returns empty array for identical objects', () => {
      const obj = { a: 1, b: 'two' };
      expect(jsonDiff(obj, { ...obj })).toEqual([]);
    });

    it('detects added properties', () => {
      const result = jsonDiff({ a: 1 }, { a: 1, b: 2 });
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: 'b',
        type: 'added',
        newValue: 2,
      });
    });

    it('detects removed properties', () => {
      const result = jsonDiff({ a: 1, b: 2 }, { a: 1 });
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: 'b',
        type: 'removed',
        oldValue: 2,
      });
    });

    it('detects changed properties', () => {
      const result = jsonDiff({ a: 1 }, { a: 2 });
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: 'a',
        type: 'changed',
        oldValue: 1,
        newValue: 2,
      });
    });

    it('handles nested objects', () => {
      const a = { outer: { inner: 1 } };
      const b = { outer: { inner: 2 } };
      const result = jsonDiff(a, b);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: 'outer.inner',
        type: 'changed',
        oldValue: 1,
        newValue: 2,
      });
    });

    it('handles deeply nested changes', () => {
      const a = { level1: { level2: { level3: { value: 'old' } } } };
      const b = { level1: { level2: { level3: { value: 'new' } } } };
      const result = jsonDiff(a, b);
      expect(result).toHaveLength(1);
      expect(result[0].path).toBe('level1.level2.level3.value');
    });
  });

  describe('arrays', () => {
    it('returns empty array for identical arrays', () => {
      expect(jsonDiff([1, 2, 3], [1, 2, 3])).toEqual([]);
    });

    it('detects added array elements', () => {
      const result = jsonDiff([1], [1, 2]);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: '[1]',
        type: 'added',
        newValue: 2,
      });
    });

    it('detects removed array elements', () => {
      const result = jsonDiff([1, 2], [1]);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: '[1]',
        type: 'removed',
        oldValue: 2,
      });
    });

    it('detects changed array elements', () => {
      const result = jsonDiff([1, 2, 3], [1, 5, 3]);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: '[1]',
        type: 'changed',
        oldValue: 2,
        newValue: 5,
      });
    });

    it('handles arrays of objects', () => {
      const a = [{ id: 1, name: 'foo' }];
      const b = [{ id: 1, name: 'bar' }];
      const result = jsonDiff(a, b);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: '[0].name',
        type: 'changed',
        oldValue: 'foo',
        newValue: 'bar',
      });
    });
  });

  describe('engine node trees', () => {
    it('detects changes in flow tree structure', () => {
      const oldTree = {
        id: 'trigger',
        type: 'trigger',
        spec: {},
        children: [{ id: 'action-1', type: 'action', spec: { timeout: 1000 } }],
      };
      const newTree = {
        id: 'trigger',
        type: 'trigger',
        spec: {},
        children: [{ id: 'action-1', type: 'action', spec: { timeout: 2000 } }],
      };

      const result = jsonDiff(oldTree, newTree);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        path: 'children[0].spec.timeout',
        type: 'changed',
        oldValue: 1000,
        newValue: 2000,
      });
    });

    it('detects added child nodes', () => {
      const oldTree = {
        id: 'trigger',
        type: 'trigger',
        spec: {},
        children: [],
      };
      const newTree = {
        id: 'trigger',
        type: 'trigger',
        spec: {},
        children: [{ id: 'action-1', type: 'action', spec: {} }],
      };

      const result = jsonDiff(oldTree, newTree);
      expect(result).toHaveLength(1);
      expect(result[0].type).toBe('added');
      expect(result[0].path).toBe('children[0]');
    });

    it('detects removed child nodes', () => {
      const oldTree = {
        id: 'trigger',
        type: 'trigger',
        spec: {},
        children: [{ id: 'action-1', type: 'action', spec: {} }],
      };
      const newTree = {
        id: 'trigger',
        type: 'trigger',
        spec: {},
        children: [],
      };

      const result = jsonDiff(oldTree, newTree);
      expect(result).toHaveLength(1);
      expect(result[0].type).toBe('removed');
    });
  });

  describe('fixtures comparison', () => {
    it('detects fixture changes', () => {
      const oldFixtures = [{ name: 'test1', input: { key: 'old' } }];
      const newFixtures = [{ name: 'test1', input: { key: 'new' } }];

      const result = jsonDiff(oldFixtures, newFixtures);
      expect(result).toHaveLength(1);
      expect(result[0].path).toBe('[0].input.key');
    });

    it('detects added fixtures', () => {
      const oldFixtures = [{ name: 'test1', input: {} }];
      const newFixtures = [
        { name: 'test1', input: {} },
        { name: 'test2', input: {} },
      ];

      const result = jsonDiff(oldFixtures, newFixtures);
      expect(result).toHaveLength(1);
      expect(result[0].type).toBe('added');
    });
  });
});

describe('formatValue', () => {
  it('formats undefined', () => {
    expect(formatValue(undefined)).toBe('undefined');
  });

  it('formats null', () => {
    expect(formatValue(null)).toBe('null');
  });

  it('formats strings with quotes', () => {
    expect(formatValue('hello')).toBe('"hello"');
  });

  it('formats numbers', () => {
    expect(formatValue(42)).toBe('42');
  });

  it('formats booleans', () => {
    expect(formatValue(true)).toBe('true');
  });

  it('formats objects as JSON', () => {
    const result = formatValue({ a: 1 });
    expect(result).toContain('"a"');
    expect(result).toContain('1');
  });

  it('formats arrays as JSON', () => {
    const result = formatValue([1, 2, 3]);
    expect(result).toContain('1');
    expect(result).toContain('2');
    expect(result).toContain('3');
  });
});

describe('groupDiffsBySection', () => {
  it('groups diffs by top-level path', () => {
    const diffs: DiffEntry[] = [
      { path: 'tree.children[0].id', type: 'changed', oldValue: 'a', newValue: 'b' },
      { path: 'tree.children[1].id', type: 'added', newValue: 'c' },
      { path: 'fixtures[0].name', type: 'changed', oldValue: 'x', newValue: 'y' },
    ];

    const groups = groupDiffsBySection(diffs);
    expect(groups.size).toBe(2);
    expect(groups.get('tree')).toHaveLength(2);
    expect(groups.get('fixtures')).toHaveLength(1);
  });

  it('handles root-level changes', () => {
    const diffs: DiffEntry[] = [
      { path: '(root)', type: 'changed', oldValue: 1, newValue: 2 },
    ];

    const groups = groupDiffsBySection(diffs);
    expect(groups.get('(root)')).toHaveLength(1);
  });

  it('handles single-level paths', () => {
    const diffs: DiffEntry[] = [
      { path: 'version', type: 'changed', oldValue: 1, newValue: 2 },
      { path: 'method', type: 'changed', oldValue: 'GET', newValue: 'POST' },
    ];

    const groups = groupDiffsBySection(diffs);
    expect(groups.get('version')).toHaveLength(1);
    expect(groups.get('method')).toHaveLength(1);
  });
});
