// SyncPanel types — mirrors the sync controller response shapes for the
// admin-side component. Kept local to the admin bundle rather than importing
// from the root types/engine.ts (same pattern as AuditViewer/types.ts).

/** A flow summary for sync status display. */
export interface FlowSummary {
  id: string;
  method: string;
  path: string;
  activeVersion: number | null;
  updatedAt: string;
}

/** A JDM summary for sync status display. */
export interface JDMSummary {
  id: string;
  updatedAt: string;
}

/** A connection summary for sync status display. */
export interface ConnectionSummary {
  key: string;
}

/** Sync status diff for one category. */
export interface SyncDiff<T> {
  /** Items present in both CMS and engine. */
  synced: T[];
  /** Items only in CMS (not in engine). */
  localOnly: T[];
  /** Items only in engine (not in CMS). */
  engineOnly: T[];
}

/** Response shape from GET /sync/status. */
export interface SyncStatus {
  flows: SyncDiff<FlowSummary>;
  jdms: SyncDiff<JDMSummary>;
  connections: SyncDiff<ConnectionSummary>;
}

/** A sync item for display with type information. */
export interface SyncItem {
  type: 'flow' | 'jdm' | 'connection';
  id: string;
  label: string;
  status: 'synced' | 'localOnly' | 'engineOnly';
}

/** Result of an import operation. */
export interface ImportResult {
  imported: {
    flows: number;
    jdms: number;
    connections: number;
  };
}

/** Result of a single item import. */
export interface ImportOneResult {
  imported: boolean;
  type: 'flow' | 'jdm' | 'connection';
  id: string;
}
