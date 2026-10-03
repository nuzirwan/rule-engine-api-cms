// §6.4 admin plugin smoke test (lightweight). Two guarantees:
//   1. BOTH custom fields register with base type:'json' (finding 13) without
//      throwing, from the admin entry's register(app) call.
//   2. A malformed stored JSON value resolves to the raw-JSON fallback decision
//      (parsed.ok === false) rather than crashing — the same pure decision the
//      JdmEditorField / FlowCanvasField use to choose the fallback editor.
//
// This stays deliberately light: it exercises the PURE descriptors + parse
// helper, not the heavy jdm-editor/reactflow React runtimes (which load lazily
// via the descriptors' async `Input` loaders and need a browser). No jsdom.

import { describe, expect, it, vi } from 'vitest';

import adminEntry from './index';
import { buildCustomFields, PLUGIN_ID } from './customFields';
import { parseStoredJson } from './lib/parseStored';

describe('§6.4 admin plugin smoke', () => {
  it('registers both custom fields with base type:json without throwing', () => {
    const registered: Array<{ name: string; pluginId: string; type: string }> = [];
    const app = {
      customFields: {
        register: vi.fn((field: { name: string; pluginId: string; type: string }) => {
          registered.push(field);
        }),
      },
    };

    expect(() => adminEntry.register(app)).not.toThrow();

    expect(app.customFields.register).toHaveBeenCalledTimes(2);
    expect(registered.map((f) => f.name).sort()).toEqual(['flow-canvas', 'jdm-editor']);
    // Both MUST use base type:'json' (finding 13) so Strapi persists the
    // serialized value into the JSON column with no double-encode.
    for (const field of registered) {
      expect(field.type).toBe('json');
      expect(field.pluginId).toBe(PLUGIN_ID);
    }
  });

  it('exposes both descriptors with lazy Input component loaders', () => {
    const fields = buildCustomFields();
    expect(fields).toHaveLength(2);
    for (const f of fields) {
      expect(f.type).toBe('json');
      expect(typeof f.components.Input).toBe('function');
    }
  });

  it('malformed stored JSON falls back to the raw-JSON editor (does not crash)', () => {
    const malformed = '{ "nodes": [ {bad json';
    const result = parseStoredJson(malformed, { nodes: [], edges: [] });
    expect(result.ok).toBe(false);
    // the raw text is preserved verbatim for the fallback editor
    expect(result.raw).toBe(malformed);
    expect(result.error).toMatch(/Invalid JSON/);
  });

  it('a bare scalar string is treated as a fallback, not a usable graph', () => {
    const result = parseStoredJson('42', {});
    expect(result.ok).toBe(false);
    expect(result.error).toMatch(/not a JSON object/);
  });

  it('a well-formed stored object parses cleanly (no fallback)', () => {
    const graph = { nodes: [{ id: 'a' }], edges: [] };
    const result = parseStoredJson<typeof graph>(graph, { nodes: [], edges: [] });
    expect(result.ok).toBe(true);
    expect(result.value).toEqual(graph);
  });

  it('a JSON string parses to its object (engine-returned raw value path)', () => {
    const result = parseStoredJson('{"nodes":[],"edges":[]}', { nodes: [], edges: [] });
    expect(result.ok).toBe(true);
    expect(result.value).toEqual({ nodes: [], edges: [] });
  });

  it('null/empty resolves to the supplied empty value (fresh entry)', () => {
    const empty = { nodes: [], edges: [] };
    expect(parseStoredJson(null, empty).value).toEqual(empty);
    expect(parseStoredJson(undefined, empty).ok).toBe(true);
    expect(parseStoredJson('', empty).value).toEqual(empty);
  });
});
