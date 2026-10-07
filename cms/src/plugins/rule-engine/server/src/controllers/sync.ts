// sync.ts — controller for the CMS-to-Engine sync feature. Provides endpoints:
//   * GET  /sync/status    — compare CMS vs engine, return diff status
//   * POST /sync/import    — pull all engine config and create/update CMS content
//   * POST /sync/import/:type/:id — pull a single item from engine
//
// Error classification follows the §5.5 table (same as audit.ts):
//   * 5xx / transport (recoverable) → 503
//   * 4xx (non-recoverable) → echo the engine status
//   * Config missing → 503 (env not provisioned)
//
// FEAT-003: Environment-aware sync:
//   * Optional ?env=documentId query param on syncStatus to filter by environment
//   * When env is provided, only CMS items with that environment relation are shown
//   * AdminClient is configured from the environment's settings when specified

import { AdminApiError, AdminClient, resolveAdminConfig } from '../services/admin-client';
import type {
  FlowSummary,
  GetFlowResponse,
  GetJdmResponse,
  JDMSummary,
  EngineConnectionDef,
  WebhookSummary,
  GetWebhookResponse,
  ScheduleSummary,
  GroupSummary,
  EngineGroup,
  ScalingConfig,
} from '../../../../../../types/engine';

/** Valid sync item types. */
type SyncItemType = 'flow' | 'jdm' | 'connection' | 'webhook' | 'schedule' | 'group';

/**
 * Parse a Go duration string (e.g. "5m", "30s", "1h30m") back to an integer
 * number of seconds for the flat CMS group fields. Returns null when the input
 * is empty/unparseable so the caller can fall back to the schema default.
 */
function durationStringToSeconds(dur: string | undefined | null): number | null {
  if (!dur) return null;
  const trimmed = dur.trim();
  if (!trimmed) return null;
  const unitSeconds: Record<string, number> = {
    ns: 1e-9,
    us: 1e-6,
    µs: 1e-6,
    ms: 1e-3,
    s: 1,
    m: 60,
    h: 3600,
  };
  // Match consecutive <number><unit> segments.
  const re = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;
  let total = 0;
  let matched = false;
  let m: RegExpExecArray | null;
  while ((m = re.exec(trimmed)) !== null) {
    matched = true;
    total += parseFloat(m[1]) * unitSeconds[m[2]];
  }
  if (!matched) return null;
  return Math.round(total);
}

/**
 * Convert an engine group (nested ScalingConfig) into the FLAT CMS group fields.
 * Duration strings become integer seconds; resources pass through; connections
 * are kept as a string[] of connection keys to be linked by the caller.
 */
function engineGroupToCmsData(group: EngineGroup): {
  data: Record<string, unknown>;
  connectionKeys: string[];
} {
  const scaling: ScalingConfig = group.scaling;
  const data: Record<string, unknown> = {
    groupId: group.id,
    name: group.name,
    description: group.description ?? '',
    enabled: group.enabled,
    scalingMode: scaling.mode,
    minReplicas: scaling.minReplicas,
    maxReplicas: scaling.maxReplicas,
  };
  const scaleDown = durationStringToSeconds(scaling.scaleDownDelay);
  if (scaleDown != null) data.scaleDownDelaySeconds = scaleDown;
  const startup = durationStringToSeconds(scaling.startupTimeout);
  if (startup != null) data.startupTimeoutSeconds = startup;
  if (scaling.resources) data.resources = { ...scaling.resources };

  return { data, connectionKeys: group.connections ?? [] };
}

/**
 * Convert an engine webhook (GET response: map-shaped mapping/filter) into the
 * FLAT CMS webhook fields with the repeatable component arrays. The flowId
 * relation is set by the caller once the related flow documentId is resolved.
 */
function engineWebhookToCmsData(wh: GetWebhookResponse): Record<string, unknown> {
  const data: Record<string, unknown> = {
    webhookId: wh.webhookId,
    name: wh.name,
    provider: wh.provider,
  };
  if (wh.secretRef != null) data.secretRef = wh.secretRef;
  if (wh.version != null) data.engineVersion = wh.version;

  // The GET response types mapping/filter as component arrays already
  // ({sourceJsonPath,targetContextPath} / {jsonPath,allowedValues}); but the
  // engine create contract is map-shaped. Support BOTH wire shapes defensively.
  data.mapping = normalizeWebhookMapping(wh.mapping);
  data.filter = normalizeWebhookFilter(wh.filter);
  return data;
}

/** Normalize an engine webhook `mapping` (map OR component array) to the CMS component array. */
function normalizeWebhookMapping(
  mapping: unknown
): { sourceJsonPath: string; targetContextPath: string }[] {
  if (!mapping) return [];
  if (Array.isArray(mapping)) {
    return mapping
      .filter((m) => m && typeof m === 'object')
      .map((m: any) => ({
        sourceJsonPath: m.sourceJsonPath,
        targetContextPath: m.targetContextPath,
      }));
  }
  if (typeof mapping === 'object') {
    return Object.entries(mapping as Record<string, string>).map(
      ([sourceJsonPath, targetContextPath]) => ({ sourceJsonPath, targetContextPath })
    );
  }
  return [];
}

/** Normalize an engine webhook `filter` (map OR component array) to the CMS component array. */
function normalizeWebhookFilter(
  filter: unknown
): { jsonPath: string; allowedValues: string[] }[] {
  if (!filter) return [];
  if (Array.isArray(filter)) {
    return filter
      .filter((f) => f && typeof f === 'object')
      .map((f: any) => ({
        jsonPath: f.jsonPath,
        allowedValues: Array.isArray(f.allowedValues) ? f.allowedValues : [],
      }));
  }
  if (typeof filter === 'object') {
    return Object.entries(filter as Record<string, string[]>).map(
      ([jsonPath, allowedValues]) => ({
        jsonPath,
        allowedValues: Array.isArray(allowedValues) ? allowedValues : [],
      })
    );
  }
  return [];
}

/** Convert an engine schedule summary into the FLAT CMS schedule fields (straight copy). */
function engineScheduleToCmsData(sched: ScheduleSummary): Record<string, unknown> {
  const data: Record<string, unknown> = {
    scheduleId: sched.id,
    name: sched.name,
    schedule: sched.schedule,
    timezone: sched.timezone ?? 'UTC',
    enabled: sched.enabled,
  };
  if (sched.input != null) data.input = sched.input;
  if (sched.lastRun != null) data.lastRun = sched.lastRun;
  if (sched.nextRun != null) data.nextRun = sched.nextRun;
  return data;
}

/** Sync status diff for one category. */
interface SyncDiff<T> {
  /** Items present in both CMS and engine. */
  synced: T[];
  /** Items only in CMS (not in engine). */
  localOnly: T[];
  /** Items only in engine (not in CMS). */
  engineOnly: T[];
}

/** Extended FlowSummary with optional environment info. */
interface FlowSummaryWithEnv extends FlowSummary {
  environmentName?: string;
  environmentId?: string;
}

/** Extended JDMSummary with optional environment info. */
interface JDMSummaryWithEnv extends JDMSummary {
  environmentName?: string;
  environmentId?: string;
}

/** Extended connection summary with optional environment info. */
interface ConnectionSummaryWithEnv {
  key: string;
  environmentName?: string;
  environmentId?: string;
}

/** A webhook summary row in the sync diff. */
interface WebhookSummaryWithEnv {
  id: string;
  name?: string;
  provider?: string;
  flowId?: string;
  environmentName?: string;
  environmentId?: string;
}

/** A schedule summary row in the sync diff. */
interface ScheduleSummaryWithEnv {
  id: string;
  name?: string;
  schedule?: string;
  enabled?: boolean;
  environmentName?: string;
  environmentId?: string;
}

/** A group summary row in the sync diff. */
interface GroupSummaryWithEnv {
  id: string;
  name?: string;
  enabled?: boolean;
  environmentName?: string;
  environmentId?: string;
}

/** Response shape for GET /sync/status. */
interface SyncStatusResponse {
  flows: SyncDiff<FlowSummaryWithEnv>;
  jdms: SyncDiff<JDMSummaryWithEnv>;
  connections: SyncDiff<ConnectionSummaryWithEnv>;
  webhooks: SyncDiff<WebhookSummaryWithEnv>;
  schedules: SyncDiff<ScheduleSummaryWithEnv>;
  groups: SyncDiff<GroupSummaryWithEnv>;
  /** Environment documentId if filtered, null for all environments. */
  environment: string | null;
}

/** Response shape for POST /sync/import. */
interface ImportAllResponse {
  imported: {
    flows: number;
    jdms: number;
    connections: number;
    webhooks: number;
    schedules: number;
    groups: number;
  };
}

/** Response shape for POST /sync/import/:type/:id. */
interface ImportOneResponse {
  imported: boolean;
  type: SyncItemType;
  id: string;
}

const VALID_SYNC_TYPES = new Set<string>([
  'flow',
  'jdm',
  'connection',
  'webhook',
  'schedule',
  'group',
]);

/** Environment config override shape. */
interface EnvironmentOverride {
  documentId: string;
  name: string;
  adminApiBaseUrl: string | null;
  operatorTokenRef: string | null;
  payloadEnv: string;
}

/**
 * Sync controller factory. The optional `fetchImpl` parameter is a test seam:
 * it is passed through to AdminClient so tests can inject the nock-compatible
 * http-fetch shim.
 */
export default function syncController(
  { strapi }: { strapi: any },
  fetchImpl?: typeof fetch
) {
  const ENV_UID = 'api::environment.environment';

  /**
   * Fetch an environment by documentId. Returns null if not found.
   */
  async function getEnvironment(documentId: string): Promise<EnvironmentOverride | null> {
    const env = await strapi.documents(ENV_UID).findOne({
      documentId,
      fields: ['name', 'adminApiBaseUrl', 'operatorTokenRef', 'payloadEnv'],
    });
    if (!env) return null;
    return {
      documentId: env.documentId,
      name: env.name,
      adminApiBaseUrl: env.adminApiBaseUrl ?? null,
      operatorTokenRef: env.operatorTokenRef ?? null,
      payloadEnv: env.payloadEnv ?? '',
    };
  }

  /**
   * Create an AdminClient instance with resolved config.
   * Returns { client } on success, or sets ctx.status/ctx.body and returns null on failure.
   * If envOverride is provided, uses that environment's config instead of defaults.
   */
  function createClient(ctx: any, envOverride?: EnvironmentOverride | null): AdminClient | null {
    let config;
    try {
      config = resolveAdminConfig(
        envOverride
          ? {
              adminApiBaseUrl: envOverride.adminApiBaseUrl,
              operatorTokenRef: envOverride.operatorTokenRef,
              payloadEnv: envOverride.payloadEnv,
            }
          : undefined
      );
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
   * CMS flow record shape with environment relation.
   */
  interface CmsFlowRecord {
    flowId: string;
    documentId: string;
    environmentName?: string;
    environmentId?: string;
  }

  /**
   * CMS JDM record shape with environment relation.
   */
  interface CmsJdmRecord {
    jdmId: string;
    documentId: string;
    environmentName?: string;
    environmentId?: string;
  }

  /**
   * CMS connection record shape with environment relation.
   */
  interface CmsConnectionRecord {
    key: string;
    documentId: string;
    environmentName?: string;
    environmentId?: string;
  }

  /**
   * Fetch all CMS flows from the document service.
   * If envDocumentId is provided, only returns flows with that environment relation.
   */
  async function getCmsFlows(envDocumentId?: string): Promise<CmsFlowRecord[]> {
    const query: any = {
      fields: ['flowId'],
      populate: {
        environment: {
          fields: ['name'],
        },
      },
    };

    // Filter by environment if specified
    if (envDocumentId) {
      query.filters = {
        environment: {
          documentId: { $eq: envDocumentId },
        },
      };
    }

    const results = await strapi.documents('api::flow.flow').findMany(query);
    return results.map((r: any) => ({
      flowId: r.flowId,
      documentId: r.documentId,
      environmentName: r.environment?.name,
      environmentId: r.environment?.documentId,
    }));
  }

  /**
   * Fetch all CMS JDMs from the document service.
   * If envDocumentId is provided, only returns JDMs with that environment relation.
   */
  async function getCmsJdms(envDocumentId?: string): Promise<CmsJdmRecord[]> {
    const query: any = {
      fields: ['jdmId'],
      populate: {
        environment: {
          fields: ['name'],
        },
      },
    };

    if (envDocumentId) {
      query.filters = {
        environment: {
          documentId: { $eq: envDocumentId },
        },
      };
    }

    const results = await strapi.documents('api::jdm.jdm').findMany(query);
    return results.map((r: any) => ({
      jdmId: r.jdmId,
      documentId: r.documentId,
      environmentName: r.environment?.name,
      environmentId: r.environment?.documentId,
    }));
  }

  /**
   * Fetch all CMS connections from the document service.
   * If envDocumentId is provided, only returns connections with that environment relation.
   */
  async function getCmsConnections(envDocumentId?: string): Promise<CmsConnectionRecord[]> {
    const query: any = {
      fields: ['key'],
      populate: {
        environment: {
          fields: ['name'],
        },
      },
    };

    if (envDocumentId) {
      query.filters = {
        environment: {
          documentId: { $eq: envDocumentId },
        },
      };
    }

    const results = await strapi.documents('api::connection.connection').findMany(query);
    return results.map((r: any) => ({
      key: r.key,
      documentId: r.documentId,
      environmentName: r.environment?.name,
      environmentId: r.environment?.documentId,
    }));
  }

  /** CMS webhook record shape with environment relation. */
  interface CmsWebhookRecord {
    webhookId: string;
    documentId: string;
    name?: string;
    provider?: string;
    flowId?: string;
    environmentName?: string;
    environmentId?: string;
  }

  /** CMS schedule record shape with environment relation. */
  interface CmsScheduleRecord {
    scheduleId: string;
    documentId: string;
    name?: string;
    schedule?: string;
    enabled?: boolean;
    environmentName?: string;
    environmentId?: string;
  }

  /** CMS group record shape with environment relation. */
  interface CmsGroupRecord {
    groupId: string;
    documentId: string;
    name?: string;
    enabled?: boolean;
    environmentName?: string;
    environmentId?: string;
  }

  /**
   * Fetch all CMS webhooks. Keyed by webhookId. Optionally filtered by env.
   */
  async function getCmsWebhooks(envDocumentId?: string): Promise<CmsWebhookRecord[]> {
    const query: any = {
      fields: ['webhookId', 'name', 'provider'],
      populate: {
        environment: { fields: ['name'] },
        flowId: { fields: ['flowId'] },
      },
    };
    if (envDocumentId) {
      query.filters = { environment: { documentId: { $eq: envDocumentId } } };
    }
    const results = await strapi.documents('api::webhook.webhook').findMany(query);
    return results.map((r: any) => ({
      webhookId: r.webhookId,
      documentId: r.documentId,
      name: r.name,
      provider: r.provider,
      flowId: r.flowId?.flowId,
      environmentName: r.environment?.name,
      environmentId: r.environment?.documentId,
    }));
  }

  /**
   * Fetch all CMS schedules. Keyed by scheduleId. Optionally filtered by env.
   */
  async function getCmsSchedules(envDocumentId?: string): Promise<CmsScheduleRecord[]> {
    const query: any = {
      fields: ['scheduleId', 'name', 'schedule', 'enabled'],
      populate: {
        environment: { fields: ['name'] },
      },
    };
    if (envDocumentId) {
      query.filters = { environment: { documentId: { $eq: envDocumentId } } };
    }
    const results = await strapi.documents('api::schedule.schedule').findMany(query);
    return results.map((r: any) => ({
      scheduleId: r.scheduleId,
      documentId: r.documentId,
      name: r.name,
      schedule: r.schedule,
      enabled: r.enabled,
      environmentName: r.environment?.name,
      environmentId: r.environment?.documentId,
    }));
  }

  /**
   * Fetch all CMS groups. Keyed by groupId. Optionally filtered by env.
   */
  async function getCmsGroups(envDocumentId?: string): Promise<CmsGroupRecord[]> {
    const query: any = {
      fields: ['groupId', 'name', 'enabled'],
      populate: {
        environment: { fields: ['name'] },
      },
    };
    if (envDocumentId) {
      query.filters = { environment: { documentId: { $eq: envDocumentId } } };
    }
    const results = await strapi.documents('api::group.group').findMany(query);
    return results.map((r: any) => ({
      groupId: r.groupId,
      documentId: r.documentId,
      name: r.name,
      enabled: r.enabled,
      environmentName: r.environment?.name,
      environmentId: r.environment?.documentId,
    }));
  }

  /**
   * Resolve the flow documentId for a given engine flowId so a webhook/schedule
   * import can set the flowId relation. Returns null when no CMS flow matches.
   */
  async function resolveFlowDocumentId(flowId: string | undefined | null): Promise<string | null> {
    if (!flowId) return null;
    const matches = await strapi.documents('api::flow.flow').findMany({
      fields: ['flowId'],
      filters: { flowId: { $eq: flowId } },
    });
    return matches?.[0]?.documentId ?? null;
  }

  /**
   * Resolve connection documentIds for a set of engine connection keys so a
   * group import can link the manyToMany `connections` relation.
   */
  async function resolveConnectionDocumentIds(keys: string[]): Promise<string[]> {
    if (!keys.length) return [];
    const matches = await strapi.documents('api::connection.connection').findMany({
      fields: ['key'],
      filters: { key: { $in: keys } },
    });
    return (matches ?? []).map((m: any) => m.documentId);
  }

  return {
    /**
     * GET /sync/status — compare CMS vs engine, return diff for flows, jdms, connections.
     * 
     * Query params:
     *   * env: optional environment documentId to filter CMS items by
     *          When provided, also uses that environment's engine config
     */
    async syncStatus(ctx: any) {
      // Parse optional environment filter
      const envDocumentId = ctx.query?.env as string | undefined;
      let envOverride: EnvironmentOverride | null = null;

      if (envDocumentId) {
        envOverride = await getEnvironment(envDocumentId);
        if (!envOverride) {
          ctx.status = 404;
          ctx.body = { error: 'Environment not found', recoverable: false };
          return;
        }
      }

      const client = createClient(ctx, envOverride);
      if (!client) return;

      try {
        // Fetch from engine
        const [
          engineFlowsResp,
          engineJdmsResp,
          engineConnectionsResp,
          engineWebhooksResp,
          engineSchedulesResp,
          engineGroupsResp,
        ] = await Promise.all([
          client.listFlows(),
          client.listJdms(),
          client.listConnections(),
          client.listWebhooks(),
          client.listSchedules(),
          client.listGroups(),
        ]);

        // Fetch from CMS (filtered by environment if specified)
        const [cmsFlows, cmsJdms, cmsConnections, cmsWebhooks, cmsSchedules, cmsGroups] =
          await Promise.all([
            getCmsFlows(envDocumentId),
            getCmsJdms(envDocumentId),
            getCmsConnections(envDocumentId),
            getCmsWebhooks(envDocumentId),
            getCmsSchedules(envDocumentId),
            getCmsGroups(envDocumentId),
          ]);

        const cmsFlowIds = new Set(cmsFlows.map(f => f.flowId));
        const cmsJdmIds = new Set(cmsJdms.map(j => j.jdmId));
        const cmsConnectionKeys = new Set(cmsConnections.map(c => c.key));
        const cmsWebhookIds = new Set(cmsWebhooks.map(w => w.webhookId));
        const cmsScheduleIds = new Set(cmsSchedules.map(s => s.scheduleId));
        const cmsGroupIds = new Set(cmsGroups.map(g => g.groupId));

        const engineFlowIds = new Set(engineFlowsResp.flows.map(f => f.id));
        const engineJdmIds = new Set(engineJdmsResp.jdms.map(j => j.id));
        const engineConnectionKeys = new Set(engineConnectionsResp.connections.map(c => c.key));
        const engineWebhookIds = new Set(engineWebhooksResp.webhooks.map(w => w.id));
        const engineScheduleIds = new Set(engineSchedulesResp.schedules.map(s => s.id));
        const engineGroupIds = new Set(engineGroupsResp.groups.map(g => g.id));

        // Create a map for CMS items to get environment info
        const cmsFlowMap = new Map(cmsFlows.map(f => [f.flowId, f]));
        const cmsJdmMap = new Map(cmsJdms.map(j => [j.jdmId, j]));
        const cmsConnectionMap = new Map(cmsConnections.map(c => [c.key, c]));
        const cmsWebhookMap = new Map(cmsWebhooks.map(w => [w.webhookId, w]));
        const cmsScheduleMap = new Map(cmsSchedules.map(s => [s.scheduleId, s]));
        const cmsGroupMap = new Map(cmsGroups.map(g => [g.groupId, g]));

        // Compute diffs for flows with environment info
        const flowsDiff: SyncDiff<FlowSummaryWithEnv> = {
          synced: engineFlowsResp.flows
            .filter(f => cmsFlowIds.has(f.id))
            .map(f => {
              const cmsFlow = cmsFlowMap.get(f.id);
              return {
                ...f,
                environmentName: cmsFlow?.environmentName,
                environmentId: cmsFlow?.environmentId,
              };
            }),
          localOnly: cmsFlows
            .filter(f => !engineFlowIds.has(f.flowId))
            .map(f => ({
              id: f.flowId,
              method: '',
              path: '',
              activeVersion: null,
              updatedAt: '',
              environmentName: f.environmentName,
              environmentId: f.environmentId,
            })),
          engineOnly: engineFlowsResp.flows.filter(f => !cmsFlowIds.has(f.id)),
        };

        // Compute diffs for jdms with environment info
        const jdmsDiff: SyncDiff<JDMSummaryWithEnv> = {
          synced: engineJdmsResp.jdms
            .filter(j => cmsJdmIds.has(j.id))
            .map(j => {
              const cmsJdm = cmsJdmMap.get(j.id);
              return {
                ...j,
                environmentName: cmsJdm?.environmentName,
                environmentId: cmsJdm?.environmentId,
              };
            }),
          localOnly: cmsJdms
            .filter(j => !engineJdmIds.has(j.jdmId))
            .map(j => ({
              id: j.jdmId,
              updatedAt: '',
              environmentName: j.environmentName,
              environmentId: j.environmentId,
            })),
          engineOnly: engineJdmsResp.jdms.filter(j => !cmsJdmIds.has(j.id)),
        };

        // Compute diffs for connections with environment info
        const connectionsDiff: SyncDiff<ConnectionSummaryWithEnv> = {
          synced: engineConnectionsResp.connections
            .filter(c => cmsConnectionKeys.has(c.key))
            .map(c => {
              const cmsConn = cmsConnectionMap.get(c.key);
              return {
                key: c.key,
                environmentName: cmsConn?.environmentName,
                environmentId: cmsConn?.environmentId,
              };
            }),
          localOnly: cmsConnections
            .filter(c => !engineConnectionKeys.has(c.key))
            .map(c => ({
              key: c.key,
              environmentName: c.environmentName,
              environmentId: c.environmentId,
            })),
          engineOnly: engineConnectionsResp.connections
            .filter(c => !cmsConnectionKeys.has(c.key))
            .map(c => ({ key: c.key })),
        };

        // Compute diffs for webhooks with environment info
        const webhooksDiff: SyncDiff<WebhookSummaryWithEnv> = {
          synced: engineWebhooksResp.webhooks
            .filter(w => cmsWebhookIds.has(w.id))
            .map(w => {
              const cms = cmsWebhookMap.get(w.id);
              return {
                id: w.id,
                name: w.name,
                provider: w.provider,
                flowId: w.flowId,
                environmentName: cms?.environmentName,
                environmentId: cms?.environmentId,
              };
            }),
          localOnly: cmsWebhooks
            .filter(w => !engineWebhookIds.has(w.webhookId))
            .map(w => ({
              id: w.webhookId,
              name: w.name,
              provider: w.provider,
              flowId: w.flowId,
              environmentName: w.environmentName,
              environmentId: w.environmentId,
            })),
          engineOnly: engineWebhooksResp.webhooks
            .filter(w => !cmsWebhookIds.has(w.id))
            .map(w => ({ id: w.id, name: w.name, provider: w.provider, flowId: w.flowId })),
        };

        // Compute diffs for schedules with environment info
        const schedulesDiff: SyncDiff<ScheduleSummaryWithEnv> = {
          synced: engineSchedulesResp.schedules
            .filter(s => cmsScheduleIds.has(s.id))
            .map(s => {
              const cms = cmsScheduleMap.get(s.id);
              return {
                id: s.id,
                name: s.name,
                schedule: s.schedule,
                enabled: s.enabled,
                environmentName: cms?.environmentName,
                environmentId: cms?.environmentId,
              };
            }),
          localOnly: cmsSchedules
            .filter(s => !engineScheduleIds.has(s.scheduleId))
            .map(s => ({
              id: s.scheduleId,
              name: s.name,
              schedule: s.schedule,
              enabled: s.enabled,
              environmentName: s.environmentName,
              environmentId: s.environmentId,
            })),
          engineOnly: engineSchedulesResp.schedules
            .filter(s => !cmsScheduleIds.has(s.id))
            .map(s => ({ id: s.id, name: s.name, schedule: s.schedule, enabled: s.enabled })),
        };

        // Compute diffs for groups with environment info
        const groupsDiff: SyncDiff<GroupSummaryWithEnv> = {
          synced: engineGroupsResp.groups
            .filter(g => cmsGroupIds.has(g.id))
            .map(g => {
              const cms = cmsGroupMap.get(g.id);
              return {
                id: g.id,
                name: g.name,
                enabled: g.enabled,
                environmentName: cms?.environmentName,
                environmentId: cms?.environmentId,
              };
            }),
          localOnly: cmsGroups
            .filter(g => !engineGroupIds.has(g.groupId))
            .map(g => ({
              id: g.groupId,
              name: g.name,
              enabled: g.enabled,
              environmentName: g.environmentName,
              environmentId: g.environmentId,
            })),
          engineOnly: engineGroupsResp.groups
            .filter(g => !cmsGroupIds.has(g.id))
            .map(g => ({ id: g.id, name: g.name, enabled: g.enabled })),
        };

        const response: SyncStatusResponse = {
          flows: flowsDiff,
          jdms: jdmsDiff,
          connections: connectionsDiff,
          webhooks: webhooksDiff,
          schedules: schedulesDiff,
          groups: groupsDiff,
          environment: envDocumentId ?? null,
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
        const [
          engineFlowsResp,
          engineJdmsResp,
          engineConnectionsResp,
          engineWebhooksResp,
          engineSchedulesResp,
          engineGroupsResp,
        ] = await Promise.all([
          client.listFlows(),
          client.listJdms(),
          client.listConnections(),
          client.listWebhooks(),
          client.listSchedules(),
          client.listGroups(),
        ]);

        // Fetch current CMS state
        const [cmsFlows, cmsJdms, cmsConnections, cmsWebhooks, cmsSchedules, cmsGroups] =
          await Promise.all([
            getCmsFlows(),
            getCmsJdms(),
            getCmsConnections(),
            getCmsWebhooks(),
            getCmsSchedules(),
            getCmsGroups(),
          ]);

        const cmsFlowMap = new Map(cmsFlows.map(f => [f.flowId, f.documentId]));
        const cmsJdmMap = new Map(cmsJdms.map(j => [j.jdmId, j.documentId]));
        const cmsConnectionMap = new Map(cmsConnections.map(c => [c.key, c.documentId]));
        const cmsWebhookMap = new Map(cmsWebhooks.map(w => [w.webhookId, w.documentId]));
        const cmsScheduleMap = new Map(cmsSchedules.map(s => [s.scheduleId, s.documentId]));
        const cmsGroupMap = new Map(cmsGroups.map(g => [g.groupId, g.documentId]));

        let flowsImported = 0;
        let jdmsImported = 0;
        let connectionsImported = 0;
        let webhooksImported = 0;
        let schedulesImported = 0;
        let groupsImported = 0;

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

        // Import webhooks (nested mapping/filter maps -> component arrays, flowId relation)
        for (const whSummary of engineWebhooksResp.webhooks) {
          const whDetail = await client.getWebhook(whSummary.id);
          const data = engineWebhookToCmsData(whDetail);
          const flowDocId = await resolveFlowDocumentId(whDetail.flowId);
          if (flowDocId) data.flowId = flowDocId;
          const existingDocId = cmsWebhookMap.get(whSummary.id);
          if (existingDocId) {
            await strapi.documents('api::webhook.webhook').update({ documentId: existingDocId, data });
          } else {
            await strapi.documents('api::webhook.webhook').create({ data });
          }
          webhooksImported++;
        }

        // Import schedules (straight field copy, flowId relation)
        for (const schedSummary of engineSchedulesResp.schedules) {
          const data = engineScheduleToCmsData(schedSummary);
          const flowDocId = await resolveFlowDocumentId(schedSummary.flowId);
          if (flowDocId) data.flowId = flowDocId;
          const existingDocId = cmsScheduleMap.get(schedSummary.id);
          if (existingDocId) {
            await strapi.documents('api::schedule.schedule').update({ documentId: existingDocId, data });
          } else {
            await strapi.documents('api::schedule.schedule').create({ data });
          }
          schedulesImported++;
        }

        // Import groups (nested ScalingConfig -> flat fields, connections relation)
        for (const groupSummary of engineGroupsResp.groups) {
          const groupDetail = await client.getGroup(groupSummary.id);
          const { data, connectionKeys } = engineGroupToCmsData(groupDetail);
          const connDocIds = await resolveConnectionDocumentIds(connectionKeys);
          if (connDocIds.length) data.connections = connDocIds;
          const existingDocId = cmsGroupMap.get(groupSummary.id);
          if (existingDocId) {
            await strapi.documents('api::group.group').update({ documentId: existingDocId, data });
          } else {
            await strapi.documents('api::group.group').create({ data });
          }
          groupsImported++;
        }

        const response: ImportAllResponse = {
          imported: {
            flows: flowsImported,
            jdms: jdmsImported,
            connections: connectionsImported,
            webhooks: webhooksImported,
            schedules: schedulesImported,
            groups: groupsImported,
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
        ctx.badRequest(
          'invalid sync type: must be flow, jdm, connection, webhook, schedule, or group'
        );
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
        } else if (type === 'webhook') {
          const whDetail = await client.getWebhook(id);
          const data = engineWebhookToCmsData(whDetail);
          const flowDocId = await resolveFlowDocumentId(whDetail.flowId);
          if (flowDocId) data.flowId = flowDocId;
          const cmsWebhooks = await getCmsWebhooks();
          const existing = cmsWebhooks.find(w => w.webhookId === id);
          if (existing) {
            await strapi.documents('api::webhook.webhook').update({ documentId: existing.documentId, data });
          } else {
            await strapi.documents('api::webhook.webhook').create({ data });
          }
        } else if (type === 'schedule') {
          const schedDetail = await client.getSchedule(id);
          const data = engineScheduleToCmsData(schedDetail);
          const flowDocId = await resolveFlowDocumentId(schedDetail.flowId);
          if (flowDocId) data.flowId = flowDocId;
          const cmsSchedules = await getCmsSchedules();
          const existing = cmsSchedules.find(s => s.scheduleId === id);
          if (existing) {
            await strapi.documents('api::schedule.schedule').update({ documentId: existing.documentId, data });
          } else {
            await strapi.documents('api::schedule.schedule').create({ data });
          }
        } else if (type === 'group') {
          const groupDetail = await client.getGroup(id);
          const { data, connectionKeys } = engineGroupToCmsData(groupDetail);
          const connDocIds = await resolveConnectionDocumentIds(connectionKeys);
          if (connDocIds.length) data.connections = connDocIds;
          const cmsGroups = await getCmsGroups();
          const existing = cmsGroups.find(g => g.groupId === id);
          if (existing) {
            await strapi.documents('api::group.group').update({ documentId: existing.documentId, data });
          } else {
            await strapi.documents('api::group.group').create({ data });
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
