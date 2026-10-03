// Unit coverage for the publish controller's pure helpers: reference collection
// from a flow tree, and the §5.5 status mapping for a blocked publish. The
// Strapi I/O half of the controller is exercised end-to-end by the nock suite
// via runPublishSequence; here we pin the two pure branches.

import { describe, expect, it } from 'vitest';

import {
  collectReferences,
  statusForBlocked,
  toConnectionEntry,
  toFlowEntry,
  toJdmEntry,
} from '../src/controllers/publish';
import { PublishBlockedError } from '../src/services/validation';
import { fmcOrderByIdTree } from './fixtures/seed-entries';

describe('collectReferences', () => {
  it('collects decision jdmIds and action connection keys from the seed tree', () => {
    const { jdmIds, connectionKeys } = collectReferences(fmcOrderByIdTree);
    expect(jdmIds).toEqual(['fmc-payment']);
    expect(connectionKeys).toEqual(['fmc-pg']);
  });

  it('dedupes repeated refs and tolerates an empty/na tree', () => {
    expect(collectReferences(null)).toEqual({ jdmIds: [], connectionKeys: [] });
    const tree = {
      id: 't',
      type: 'trigger',
      spec: {},
      children: [
        { id: 'a', type: 'action', spec: { connection: 'c1' } },
        { id: 'b', type: 'action', spec: { connection: 'c1' } },
        { id: 'd', type: 'decision', spec: { jdmId: 'j1' } },
      ],
    };
    expect(collectReferences(tree)).toEqual({ jdmIds: ['j1'], connectionKeys: ['c1'] });
  });
});

describe('toFlowEntry / toJdmEntry / toConnectionEntry mappers', () => {
  it('maps a loaded Flow document to the transform FlowEntry shape', () => {
    const doc = {
      flowId: 'f',
      method: 'GET',
      path: '/x',
      tree: { id: 't', type: 'trigger', spec: {} },
      fixtures: null,
      note: null,
    };
    expect(toFlowEntry(doc)).toEqual({
      flowId: 'f',
      method: 'GET',
      path: '/x',
      tree: doc.tree,
      fixtures: null,
      note: null,
    });
  });

  it('maps jdm + connection documents', () => {
    expect(toJdmEntry({ jdmId: 'j', doc: { a: 1 } })).toEqual({ jdmId: 'j', doc: { a: 1 } });
    expect(
      toConnectionEntry({ key: 'k', type: 'postgres', settings: { host: 'h' }, secretRef: 'r', resilience: null })
    ).toEqual({ key: 'k', type: 'postgres', settings: { host: 'h' }, secretRef: 'r', resilience: null });
  });
});

describe('statusForBlocked (§5.5 mapping)', () => {
  it('route collision => 409', () => {
    expect(statusForBlocked(new PublishBlockedError('x', 'createFlow', false, 409))).toBe(409);
  });
  it('version not found => 404', () => {
    expect(statusForBlocked(new PublishBlockedError('x', 'validateFlow', false, 404))).toBe(404);
  });
  it('under-privileged => 403', () => {
    expect(statusForBlocked(new PublishBlockedError('x', 'publishFlow', false, 403))).toBe(403);
  });
  it('publish un-validated => 422', () => {
    expect(statusForBlocked(new PublishBlockedError('x', 'publishFlow', false, 422))).toBe(422);
  });
  it('transport/5xx recoverable => 503', () => {
    expect(statusForBlocked(new PublishBlockedError('x', 'validateFlow', true, 503))).toBe(503);
    expect(statusForBlocked(new PublishBlockedError('x', 'listConnections', true, 0))).toBe(503);
  });
  it('ok:false validation failure (no HTTP status) => 422, never 503', () => {
    // the validate call returned 200 ok:false — recoverable flag is irrelevant;
    // it is author-fixable and publish-blocking, so 422.
    expect(statusForBlocked(new PublishBlockedError('x', 'validateFlow', true))).toBe(422);
    expect(statusForBlocked(new PublishBlockedError('x', 'validateFlow', false))).toBe(422);
  });
});
