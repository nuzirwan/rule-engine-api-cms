// AuditViewer types — mirrors the engine audit contract for the admin-side
// component. Kept local to the admin bundle rather than importing from the
// root types/engine.ts because the admin bundle is compiled separately by
// Strapi's vite build and should not rely on a deep relative path to the CMS
// root. The types must stay in sync with types/engine.ts AuditEntry /
// AuditTrailResponse / AuditObjectType.

/** Valid object types for the audit endpoint. */
export type AuditObjectType = 'flow' | 'jdm' | 'connection';

/** One entry in the audit trail returned by GET /rule-engine/audit/:type/:id. */
export interface AuditEntry {
  action: string;
  fromVersion: number | null;
  toVersion: number | null;
  actor: string;
  /** ISO 8601 timestamp (from engine). */
  at: string;
  reason: string;
}

/** Shape returned by the CMS audit proxy route. */
export interface AuditTrailResponse {
  objectType: AuditObjectType;
  objectId: string;
  entries: AuditEntry[];
}
