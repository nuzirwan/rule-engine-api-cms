// sync.ts — controller for the CMS-to-Engine sync feature. Provides endpoints:
//   * GET  /sync/status    — compare CMS vs engine, return diff status
//   * POST /sync/import    — pull all engine config and create/update CMS content
//   * POST /sync/import/:type/:id — pull a single item from engine
//
// Error classification follows the §5.5 table (same as audit.ts):
//   * 5xx / transport (recoverable) → 503
//   * 4xx (non-recoverable) → echo the engine status
//   * Config missing → 503 (env not provisioned)

import { AdminApiError, AdminClient, resolveAdminConfig } from '../services/admin-client';
import type {
  FlowSummary,
  GetFlowResponse,
  GetJdmResponse,
  JDMSummary,
  EngineConnectionDef,
} from '../../../../../../types/engine';

/** Valid sync item types. */
type SyncItemType = 'flow' | 'jdm' | 'connection';

/** Sync status diff for one category. */
interface SyncDiff<T> {
  /** Items present in both CMS and engine. */
  synced: T[];
  /** Items only in CMS (not in engine). */
  localOnly: T[];
  /** Items only in engine (not in CMS). */
  engineOnly: T[];
}

/** Response shape for GET /sync/status. */
interface SyncStatusResponse {
  flows: SyncDiff<FlowSummary>;
  jdms: SyncDiff<JDMSummary>;
  connections: SyncDiff<{ key: string }>;
}

/** Response shape for POST /sync/import. */
interface ImportAllResponse {
  imported: {
    flows: number;
    jdms: number;
    connections: number;
  };
}

/** Response shape for POST /sync/import/:type/:id. */
interface ImportOneResponse {
  imported: boolean;
  type: SyncItemType;
  id: string;
}

const VALID_SYNC_TYPES = new Set<string>(['flow', 'jdm', 'connection']);

/**
 * Sync controller factory. The optional `fetchImpl` parameter is a test seam:
 * it is passed through to AdminClient so tests can inject the nock-compatible
 * http-fetch shim.
 */
export default function syncController(
  { strapi }: { strapi: any },
  fetchImpl?: typeof fetch
) {
  /**
   * Create an AdminClient instance with resolved config.
   * Returns { client } on success, or sets ctx.status/ctx.body and returns null on failure.
   */
  function createClient(ctx: any): AdminClient | null {
    let config;
    try {
      config = resolveAdminConfig();
    } catch (err) {
      ctx.status = 503;
      ctx.body = { error: (err as Error).message, recoverable: false };
      return null;
    }
    return fetchImpl ? new AdminClient(config, fetchImpl) : new AdminClient(config);
  }

  /**
   * Handle AdminApiError: recoverable (5xx) → 503, non-recoverable → echo status.
   */
  function handleApiError(ctx: any, err: unknown): void {
    if (err instanceof AdminApiError) {
      ctx.status = err.recoverable ? 503 : (err.status || 500);
      ctx.body = { error: err.message, recoverable: err.recoverable };
    } else {
      throw err;
    }
  }

  /**
   * Fetch all CMS flows from the document service.
   */
  async function getCmsFlows(): Promise<{ flowId: string; documentId: string }[]> {
    const results = await strapi.documents('api::flow.flow').findMany({
      fields: ['flowId'],
    });
    return results.map((r: any) => ({ flowId: r.flowId, documentId: r.documentId }));
  }

  /**
   * Fetch all CMS JDMs from the document service.
   */
  async function getCmsJdms(): Promise<{ jdmId: string; documentId: string }[]> {
    const results = await strapi.documents('api::jdm.jdm').findMany({
      fields: ['jdmId'],
    });
    return results.map((r: any) => ({ jdmId: r.jdmId, documentId: r.documentId }));
  }

  /**
   * Fetch all CMS connections from the document service.
   */
  async function getCmsConnections(): Promise<{ key: string; documentId: string }[]> {
    const results = await strapi.documents('api::connection.connection').findMany({
      fields: ['key'],
    });
    return results.map((r: any) => ({ key: r.key, documentId: r.documentId }));
  }

  return {
    /**
     * GET /sync/status — compare CMS vs engine, return diff for flows, jdms, connections.
     */
    async syncStatus(ctx: any) {
      const client = createClient(ctx);
      if (!client) return;

      try {
        // Fetch from engine
        const [engineFlowsResp, engineJdmsResp, engineConnectionsResp] = await Promise.all([
          client.listFlows(),
          client.listJdms(),
          client.listConnections(),
        ]);

        // Fetch from CMS
        const [cmsFlows, cmsJdms, cmsConnections] = await Promise.all([
          getCmsFlows(),
          getCmsJdms(),
          getCmsConnections(),
        ]);

        const cmsFlowIds = new Set(cmsFlows.map(f => f.flowId));
        const cmsJdmIds = new Set(cmsJdms.map(j => j.jdmId));
        const cmsConnectionKeys = new Set(cmsConnections.map(c => c.key));

        const engineFlowIds = new Set(engineFlowsResp.flows.map(f => f.id));
        const engineJdmIds = new Set(engineJdmsResp.jdms.map(j => j.id));
        const engineConnectionKeys = new Set(engineConnectionsResp.connections.map(c => c.key));

        // Compute diffs for flows
        const flowsDiff: SyncDiff<FlowSummary> = {
          synced: engineFlowsResp.flows.filter(f => cmsFlowIds.has(f.id)),
          localOnly: cmsFlows
            .filter(f => !engineFlowIds.has(f.flowId))
            .map(f => ({ id: f.flowId, method: '', path: '', activeVersion: null, updatedAt: '' })),
          engineOnly: engineFlowsResp.flows.filter(f => !cmsFlowIds.has(f.id)),
        };

        // Compute diffs for jdms
        const jdmsDiff: SyncDiff<JDMSummary> = {
          synced: engineJdmsResp.jdms.filter(j => cmsJdmIds.has(j.id)),
          localOnly: cmsJdms
            .filter(j => !engineJdmIds.has(j.jdmId))
            .map(j => ({ id: j.jdmId, updatedAt: '' })),
          engineOnly: engineJdmsResp.jdms.filter(j => !cmsJdmIds.has(j.id)),
        };

        // Compute diffs for connections
        const connectionsDiff: SyncDiff<{ key: string }> = {
          synced: engineConnectionsResp.connections
            .filter(c => cmsConnectionKeys.has(c.key))
            .map(c => ({ key: c.key })),
          localOnly: cmsConnections
            .filter(c => !engineConnectionKeys.has(c.key))
            .map(c => ({ key: c.key })),
          engineOnly: engineConnectionsResp.connections
            .filter(c => !cmsConnectionKeys.has(c.key))
            .map(c => ({ key: c.key })),
        };

        const response: SyncStatusResponse = {
          flows: flowsDiff,
          jdms: jdmsDiff,
          connections: connectionsDiff,
        };

        ctx.body = response;
      } catch (err) {
        handleApiError(ctx, err);
      }
    },

    /**
     * POST /sync/import — pull all engine config and create/update CMS content.
     */
    async importAll(ctx: any) {
      const client = createClient(ctx);
      if (!client) return;

      try {
        // Fetch all from engine
        const [engineFlowsResp, engineJdmsResp, engineConnectionsResp] = await Promise.all([
          client.listFlows(),
          client.listJdms(),
          client.listConnections(),
        ]);

        // Fetch current CMS state
        const [cmsFlows, cmsJdms, cmsConnections] = await Promise.all([
          getCmsFlows(),
          getCmsJdms(),
          getCmsConnections(),
        ]);

        const cmsFlowMap = new Map(cmsFlows.map(f => [f.flowId, f.documentId]));
        const cmsJdmMap = new Map(cmsJdms.map(j => [j.jdmId, j.documentId]));
        const cmsConnectionMap = new Map(cmsConnections.map(c => [c.key, c.documentId]));

        let flowsImported = 0;
        let jdmsImported = 0;
        let connectionsImported = 0;

        // Import flows
        for (const flowSummary of engineFlowsResp.flows) {
          const flowDetail = await client.getFlow(flowSummary.id);
          const existingDocId = cmsFlowMap.get(flowSummary.id);

          if (existingDocId) {
            // Update existing
            await strapi.documents('api::flow.flow').update({
              documentId: existingDocId,
              data: {
                flowId: flowDetail.flowId,
                method: flowDetail.method,
                path: flowDetail.path,
                tree: flowDetail.tree,
                fixtures: flowDetail.fixtures ?? [],
              },
            });
          } else {
            // Create new
            await strapi.documents('api::flow.flow').create({
              data: {
                flowId: flowDetail.flowId,
                method: flowDetail.method,
                path: flowDetail.path,
                tree: flowDetail.tree,
                fixtures: flowDetail.fixtures ?? [],
              },
            });
          }
          flowsImported++;
        }

        // Import JDMs
        for (const jdmSummary of engineJdmsResp.jdms) {
          const jdmDetail = await client.getJdm(jdmSummary.id);
          const existingDocId = cmsJdmMap.get(jdmSummary.id);

          if (existingDocId) {
            await strapi.documents('api::jdm.jdm').update({
              documentId: existingDocId,
              data: {
                jdmId: jdmDetail.jdmId,
                doc: jdmDetail.doc,
              },
            });
          } else {
            await strapi.documents('api::jdm.jdm').create({
              data: {
                jdmId: jdmDetail.jdmId,
                doc: jdmDetail.doc,
              },
            });
          }
          jdmsImported++;
        }

        // Import connections
        for (const conn of engineConnectionsResp.connections) {
          const existingDocId = cmsConnectionMap.get(conn.key);

          if (existingDocId) {
            await strapi.documents('api::connection.connection').update({
              documentId: existingDocId,
              data: {
                key: conn.key,
                type: conn.type,
                settings: conn.settings,
                secretRef: conn.secretRef,
                resilience: conn.resilience,
              },
            });
          } else {
            await strapi.documents('api::connection.connection').create({
              data: {
                key: conn.key,
                type: conn.type,
                settings: conn.settings,
                secretRef: conn.secretRef,
                resilience: conn.resilience,
              },
            });
          }
          connectionsImported++;
        }

        const response: ImportAllResponse = {
          imported: {
            flows: flowsImported,
            jdms: jdmsImported,
            connections: connectionsImported,
          },
        };

        ctx.body = response;
      } catch (err) {
        handleApiError(ctx, err);
      }
    },

    /**
     * POST /sync/import/:type/:id — pull a single item from engine.
     */
    async importOne(ctx: any) {
      const { type, id } = ctx.params as { type: string; id: string };

      if (!VALID_SYNC_TYPES.has(type)) {
        ctx.badRequest('invalid sync type: must be flow, jdm, or connection');
        return;
      }

      const client = createClient(ctx);
      if (!client) return;

      try {
        if (type === 'flow') {
          const flowDetail = await client.getFlow(id);
          const cmsFlows = await getCmsFlows();
          const existing = cmsFlows.find(f => f.flowId === id);

          if (existing) {
            await strapi.documents('api::flow.flow').update({
              documentId: existing.documentId,
              data: {
                flowId: flowDetail.flowId,
                method: flowDetail.method,
                path: flowDetail.path,
                tree: flowDetail.tree,
                fixtures: flowDetail.fixtures ?? [],
              },
            });
          } else {
            await strapi.documents('api::flow.flow').create({
              data: {
                flowId: flowDetail.flowId,
                method: flowDetail.method,
                path: flowDetail.path,
                tree: flowDetail.tree,
                fixtures: flowDetail.fixtures ?? [],
              },
            });
          }
        } else if (type === 'jdm') {
          const jdmDetail = await client.getJdm(id);
          const cmsJdms = await getCmsJdms();
          const existing = cmsJdms.find(j => j.jdmId === id);

          if (existing) {
            await strapi.documents('api::jdm.jdm').update({
              documentId: existing.documentId,
              data: {
                jdmId: jdmDetail.jdmId,
                doc: jdmDetail.doc,
              },
            });
          } else {
            await strapi.documents('api::jdm.jdm').create({
              data: {
                jdmId: jdmDetail.jdmId,
                doc: jdmDetail.doc,
              },
            });
          }
        } else if (type === 'connection') {
          const connDetail = await client.getConnection(id);
          const cmsConnections = await getCmsConnections();
          const existing = cmsConnections.find(c => c.key === id);

          if (existing) {
            await strapi.documents('api::connection.connection').update({
              documentId: existing.documentId,
              data: {
                key: connDetail.key,
                type: connDetail.type,
                settings: connDetail.settings,
                secretRef: connDetail.secretRef,
                resilience: connDetail.resilience,
              },
            });
          } else {
            await strapi.documents('api::connection.connection').create({
              data: {
                key: connDetail.key,
                type: connDetail.type,
                settings: connDetail.settings,
                secretRef: connDetail.secretRef,
                resilience: connDetail.resilience,
              },
            });
          }
        }

        const response: ImportOneResponse = {
          imported: true,
          type: type as SyncItemType,
          id,
        };

        ctx.body = response;
      } catch (err) {
        handleApiError(ctx, err);
      }
    },
  };
}
