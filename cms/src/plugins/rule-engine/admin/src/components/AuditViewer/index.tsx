// AuditViewer — read-only panel that displays the audit trail for a Flow, JDM,
// or Connection by fetching from the CMS proxy route
// GET /rule-engine/audit/:type/:id. Follows the ValidationPanel pattern:
//   * @strapi/design-system components for layout and typography
//   * Fetch-on-mount via useFetchClient (Strapi 5 admin hook)
//   * Loading / error / empty / list states — no implicit null crash paths
//
// Placement in the content-manager edit view is a separate task; this component
// is exported and ready to integrate into Flow/JDM/Connection edit views.

import * as React from 'react';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import { Box, Flex, Loader, Typography } from '@strapi/design-system';

import type { AuditEntry, AuditObjectType, AuditTrailResponse } from './types';
import { labelForAction, relativeTime, versionDelta } from './utils';

export { labelForAction, relativeTime, versionDelta };

export interface AuditViewerProps {
  type: AuditObjectType;
  /** The entity's documentId or engine id (e.g. the Flow's flowId). */
  id: string;
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

interface EntryCardProps {
  entry: AuditEntry;
  index: number;
}

function EntryCard({ entry, index }: EntryCardProps): React.JSX.Element {
  const { label: timeLabel, title: timeTitle } = relativeTime(entry.at);
  const delta = versionDelta(entry.fromVersion, entry.toVersion);

  return (
    <Box key={index} padding={3} background="neutral0" hasRadius>
      <Flex direction="column" alignItems="stretch" gap={1}>
        {/* Action + version delta */}
        <Flex direction="row" justifyContent="space-between" alignItems="center">
          <Typography variant="omega" fontWeight="bold" textColor="primary600">
            {labelForAction(entry.action)}
          </Typography>
          {delta !== null && (
            <Typography variant="pi" textColor="neutral600">
              {delta}
            </Typography>
          )}
        </Flex>

        {/* Actor */}
        <Typography variant="pi" textColor="neutral700">
          {entry.actor}
        </Typography>

        {/* Timestamp — relative label with full ISO on hover */}
        <Typography
          variant="pi"
          textColor="neutral500"
          title={timeTitle}
          style={{ cursor: 'default' }}
        >
          {timeLabel}
        </Typography>

        {/* Reason (optional) */}
        {entry.reason ? (
          <Typography variant="pi" textColor="neutral600">
            {entry.reason}
          </Typography>
        ) : null}
      </Flex>
    </Box>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

type FetchState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'error'; message: string; recoverable: boolean }
  | { status: 'ok'; data: AuditTrailResponse };

export const AuditViewer: React.FC<AuditViewerProps> = ({ type, id }) => {
  const { get } = useFetchClient();
  const [state, setState] = React.useState<FetchState>({ status: 'idle' });

  React.useEffect(() => {
    if (!type || !id) return;

    setState({ status: 'loading' });

    get<AuditTrailResponse>(`/rule-engine/audit/${encodeURIComponent(type)}/${encodeURIComponent(id)}`)
      .then(({ data }) => {
        setState({ status: 'ok', data });
      })
      .catch((err: unknown) => {
        const body = (err as any)?.response?.data;
        const recoverable = body?.recoverable ?? true;
        const message =
          body?.error ??
          (err instanceof Error ? err.message : 'Failed to load audit trail');
        setState({ status: 'error', message, recoverable });
      });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [type, id]);

  return (
    <Box padding={4} background="neutral100" hasRadius>
      <Flex direction="column" alignItems="stretch" gap={3}>
        {/* Header */}
        <Typography variant="delta">Audit Trail</Typography>

        {/* Loading */}
        {state.status === 'loading' || state.status === 'idle' ? (
          <Flex justifyContent="center" padding={4}>
            <Loader small />
          </Flex>
        ) : null}

        {/* Error */}
        {state.status === 'error' ? (
          <Box padding={3} background="danger100" hasRadius>
            <Flex direction="column" alignItems="stretch" gap={1}>
              <Typography variant="omega" textColor="danger600">
                {state.recoverable
                  ? 'Unable to load audit trail — the engine may be temporarily unavailable.'
                  : 'Unable to load audit trail.'}
              </Typography>
              <Typography variant="pi" textColor="danger500">
                {state.message}
              </Typography>
              {state.recoverable ? (
                <Typography variant="pi" textColor="neutral600">
                  Refresh the page to retry.
                </Typography>
              ) : null}
            </Flex>
          </Box>
        ) : null}

        {/* Empty */}
        {state.status === 'ok' && state.data.entries.length === 0 ? (
          <Typography variant="pi" textColor="neutral600">
            No audit history for this {type}.
          </Typography>
        ) : null}

        {/* Entry list */}
        {state.status === 'ok' && state.data.entries.length > 0 ? (
          <Flex direction="column" alignItems="stretch" gap={2}>
            {state.data.entries.map((entry, i) => (
              <EntryCard key={i} entry={entry} index={i} />
            ))}
          </Flex>
        ) : null}
      </Flex>
    </Box>
  );
};

export default AuditViewer;
