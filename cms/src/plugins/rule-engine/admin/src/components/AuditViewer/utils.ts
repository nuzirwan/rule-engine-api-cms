// AuditViewer pure utility helpers — extracted to a separate module so they
// can be unit-tested in a node environment without pulling in the React/Strapi
// admin runtime (which would fail in vitest's node environment because
// @strapi/admin/strapi-admin is a browser/admin bundle).

/** Human-readable label for each audit action code. */
export function labelForAction(action: string): string {
  switch (action) {
    case 'create_version':
      return 'Created version';
    case 'publish':
      return 'Published';
    case 'rollback':
      return 'Rolled back';
    case 'validate':
      return 'Validated';
    default:
      return action;
  }
}

/**
 * Format an ISO timestamp as a relative string (e.g., "2 hours ago") with the
 * full ISO value returned as a title attribute for hover precision.
 * Uses Intl.RelativeTimeFormat for locale-aware output.
 */
export function relativeTime(iso: string): { label: string; title: string } {
  const now = Date.now();
  const then = new Date(iso).getTime();
  const diffMs = then - now; // negative for past timestamps
  const diffSec = Math.round(diffMs / 1_000);
  const diffMin = Math.round(diffSec / 60);
  const diffHour = Math.round(diffMin / 60);
  const diffDay = Math.round(diffHour / 24);

  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

  let label: string;
  if (Math.abs(diffSec) < 60) {
    label = rtf.format(diffSec, 'second');
  } else if (Math.abs(diffMin) < 60) {
    label = rtf.format(diffMin, 'minute');
  } else if (Math.abs(diffHour) < 24) {
    label = rtf.format(diffHour, 'hour');
  } else {
    label = rtf.format(diffDay, 'day');
  }

  return { label, title: iso };
}

/**
 * Format the version delta: "v{from} → v{to}", or "→ v{to}" when from is null.
 * Returns null when both versions are null or toVersion is null.
 */
export function versionDelta(
  fromVersion: number | null,
  toVersion: number | null
): string | null {
  if (toVersion === null) return null;
  if (fromVersion === null) return `→ v${toVersion}`;
  return `v${fromVersion} → v${toVersion}`;
}
