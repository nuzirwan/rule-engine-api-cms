// VersionHistory types — mirrors the engine version contract for the admin-side
// component. Kept local to the admin bundle rather than importing from the
// root types/engine.ts because the admin bundle is compiled separately by
// Strapi's vite build (same pattern as AuditViewer/types.ts, SyncPanel/types.ts).

/** A single version summary in the flow version history. */
export interface VersionSummary {
  version: number;
  validated: boolean;
  createdAt: string;
  createdBy: string;
}

/** Response shape from GET /rule-engine/flows/:id/versions. */
export interface FlowVersionsResponse {
  flowId: string;
  versions: VersionSummary[];
}

/** Response shape from POST /rule-engine/flows/:id/rollback. */
export interface RollbackResponse {
  flowId: string;
  activeVersion: number;
  action: string;
}

/** Full flow detail for version preview — matches engine GetFlowResponse shape. */
export interface FlowDetail {
  flowId: string;
  version: number;
  method: string;
  path: string;
  tree: EngineNode;
  fixtures?: EngineFixture[];
}

/** A recursive engine flow node (local copy of types/engine.ts EngineNode). */
export interface EngineNode {
  id: string;
  type: string;
  spec: Record<string, unknown>;
  children?: EngineNode[];
}

/** A flow fixture entry (local copy of types/engine.ts EngineFixture). */
export interface EngineFixture {
  name: string;
  input: unknown;
  mocks?: unknown;
  expect?: unknown;
}
