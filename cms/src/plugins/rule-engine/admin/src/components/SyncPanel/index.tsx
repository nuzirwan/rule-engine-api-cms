// SyncPanel — displays sync status between CMS and engine, with import actions.
// Follows the AuditViewer pattern:
//   * @strapi/design-system components for layout and typography
//   * Fetch-on-mount via useFetchClient (Strapi 5 admin hook)
//   * Loading / error / success states with FetchState union
//
// Shows diff status (synced/localOnly/engineOnly) and provides:
//   * Import All button to pull all engine config
//   * Per-item import buttons for engine-only items

import * as React from 'react';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import {
  Box,
  Button,
  Flex,
  Loader,
  Typography,
  Badge,
  Table,
  Thead,
  Tbody,
  Tr,
  Th,
  Td,
} from '@strapi/design-system';

import type { SyncStatus, SyncItem, ImportResult } from './types';

export type { SyncStatus, SyncItem, ImportResult };

// ---------------------------------------------------------------------------
// FetchState discriminated union
// ---------------------------------------------------------------------------

type FetchState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'error'; message: string; recoverable: boolean }
  | { status: 'ok'; data: SyncStatus };

type ImportState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'success'; result: ImportResult }
  | { status: 'error'; message: string };

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

/** Convert sync status to a flat list of sync items for display. */
function flattenSyncStatus(status: SyncStatus): SyncItem[] {
  const items: SyncItem[] = [];

  // Flows
  for (const flow of status.flows.synced) {
    items.push({
      type: 'flow',
      id: flow.id,
      label: `${flow.method} ${flow.path}`,
      status: 'synced',
    });
  }
  for (const flow of status.flows.localOnly) {
    items.push({
      type: 'flow',
      id: flow.id,
      label: flow.id,
      status: 'localOnly',
    });
  }
  for (const flow of status.flows.engineOnly) {
    items.push({
      type: 'flow',
      id: flow.id,
      label: `${flow.method} ${flow.path}`,
      status: 'engineOnly',
    });
  }

  // JDMs
  for (const jdm of status.jdms.synced) {
    items.push({
      type: 'jdm',
      id: jdm.id,
      label: jdm.id,
      status: 'synced',
    });
  }
  for (const jdm of status.jdms.localOnly) {
    items.push({
      type: 'jdm',
      id: jdm.id,
      label: jdm.id,
      status: 'localOnly',
    });
  }
  for (const jdm of status.jdms.engineOnly) {
    items.push({
      type: 'jdm',
      id: jdm.id,
      label: jdm.id,
      status: 'engineOnly',
    });
  }

  // Connections
  for (const conn of status.connections.synced) {
    items.push({
      type: 'connection',
      id: conn.key,
      label: conn.key,
      status: 'synced',
    });
  }
  for (const conn of status.connections.localOnly) {
    items.push({
      type: 'connection',
      id: conn.key,
      label: conn.key,
      status: 'localOnly',
    });
  }
  for (const conn of status.connections.engineOnly) {
    items.push({
      type: 'connection',
      id: conn.key,
      label: conn.key,
      status: 'engineOnly',
    });
  }

  return items;
}

/** Get badge variant for sync status. */
function getBadgeVariant(status: SyncItem['status']): string {
  switch (status) {
    case 'synced':
      return 'success';
    case 'localOnly':
      return 'secondary';
    case 'engineOnly':
      return 'alternative';
    default:
      return 'neutral';
  }
}

/** Get human-readable label for sync status. */
function getStatusLabel(status: SyncItem['status']): string {
  switch (status) {
    case 'synced':
      return 'Synced';
    case 'localOnly':
      return 'CMS Only';
    case 'engineOnly':
      return 'Engine Only';
    default:
      return status;
  }
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

interface SyncItemRowProps {
  item: SyncItem;
  onImport: (type: SyncItem['type'], id: string) => void;
  importing: boolean;
}

function SyncItemRow({ item, onImport, importing }: SyncItemRowProps): React.JSX.Element {
  return (
    <Tr>
      <Td>
        <Typography variant="omega">{item.type}</Typography>
      </Td>
      <Td>
        <Typography variant="omega">{item.label}</Typography>
      </Td>
      <Td>
        <Badge variant={getBadgeVariant(item.status)}>{getStatusLabel(item.status)}</Badge>
      </Td>
      <Td>
        {item.status === 'engineOnly' && (
          <Button
            variant="secondary"
            size="S"
            onClick={() => onImport(item.type, item.id)}
            disabled={importing}
          >
            Import
          </Button>
        )}
      </Td>
    </Tr>
  );
}

interface SyncSummaryProps {
  status: SyncStatus;
}

function SyncSummary({ status }: SyncSummaryProps): React.JSX.Element {
  const totalSynced =
    status.flows.synced.length +
    status.jdms.synced.length +
    status.connections.synced.length;
  const totalLocalOnly =
    status.flows.localOnly.length +
    status.jdms.localOnly.length +
    status.connections.localOnly.length;
  const totalEngineOnly =
    status.flows.engineOnly.length +
    status.jdms.engineOnly.length +
    status.connections.engineOnly.length;

  return (
    <Flex direction="row" gap={4} wrap="wrap">
      <Box padding={3} background="success100" hasRadius>
        <Flex direction="column" alignItems="center" gap={1}>
          <Typography variant="delta" textColor="success600">
            {totalSynced}
          </Typography>
          <Typography variant="pi" textColor="success700">
            Synced
          </Typography>
        </Flex>
      </Box>
      <Box padding={3} background="neutral100" hasRadius>
        <Flex direction="column" alignItems="center" gap={1}>
          <Typography variant="delta" textColor="neutral600">
            {totalLocalOnly}
          </Typography>
          <Typography variant="pi" textColor="neutral700">
            CMS Only
          </Typography>
        </Flex>
      </Box>
      <Box padding={3} background="alternative100" hasRadius>
        <Flex direction="column" alignItems="center" gap={1}>
          <Typography variant="delta" textColor="alternative600">
            {totalEngineOnly}
          </Typography>
          <Typography variant="pi" textColor="alternative700">
            Engine Only
          </Typography>
        </Flex>
      </Box>
    </Flex>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface SyncPanelProps {
  /** Called after a successful import to allow parent to refresh. */
  onImportComplete?: () => void;
}

export const SyncPanel: React.FC<SyncPanelProps> = ({ onImportComplete }) => {
  const { get, post } = useFetchClient();
  const [state, setState] = React.useState<FetchState>({ status: 'idle' });
  const [importState, setImportState] = React.useState<ImportState>({ status: 'idle' });

  const fetchStatus = React.useCallback(async () => {
    setState({ status: 'loading' });

    try {
      const { data } = await get<SyncStatus>('/rule-engine/sync/status');
      setState({ status: 'ok', data });
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const recoverable = body?.recoverable ?? true;
      const message =
        body?.error ?? (err instanceof Error ? err.message : 'Failed to load sync status');
      setState({ status: 'error', message, recoverable });
    }
  }, [get]);

  React.useEffect(() => {
    fetchStatus();
  }, [fetchStatus]);

  const handleImportAll = async () => {
    setImportState({ status: 'loading' });

    try {
      const { data } = await post<ImportResult>('/rule-engine/sync/import', {});
      setImportState({ status: 'success', result: data });
      // Refresh status after import
      await fetchStatus();
      onImportComplete?.();
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const message =
        body?.error ?? (err instanceof Error ? err.message : 'Import failed');
      setImportState({ status: 'error', message });
    }
  };

  const handleImportOne = async (type: SyncItem['type'], id: string) => {
    setImportState({ status: 'loading' });

    try {
      await post(`/rule-engine/sync/import/${encodeURIComponent(type)}/${encodeURIComponent(id)}`, {});
      // Refresh status after import
      await fetchStatus();
      setImportState({ status: 'idle' });
      onImportComplete?.();
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const message =
        body?.error ?? (err instanceof Error ? err.message : 'Import failed');
      setImportState({ status: 'error', message });
    }
  };

  const items = state.status === 'ok' ? flattenSyncStatus(state.data) : [];
  const hasEngineOnlyItems = items.some((item) => item.status === 'engineOnly');
  const isImporting = importState.status === 'loading';

  return (
    <Box padding={4} background="neutral100" hasRadius>
      <Flex direction="column" alignItems="stretch" gap={4}>
        {/* Header */}
        <Flex direction="row" justifyContent="space-between" alignItems="center">
          <Typography variant="delta">Engine Sync</Typography>
          <Flex direction="row" gap={2}>
            <Button variant="tertiary" onClick={fetchStatus} disabled={isImporting}>
              Refresh
            </Button>
            {state.status === 'ok' && hasEngineOnlyItems && (
              <Button variant="default" onClick={handleImportAll} disabled={isImporting}>
                {isImporting ? 'Importing...' : 'Import All'}
              </Button>
            )}
          </Flex>
        </Flex>

        {/* Loading */}
        {(state.status === 'loading' || state.status === 'idle') && (
          <Flex justifyContent="center" padding={4}>
            <Loader small />
          </Flex>
        )}

        {/* Error */}
        {state.status === 'error' && (
          <Box padding={3} background="danger100" hasRadius>
            <Flex direction="column" alignItems="stretch" gap={1}>
              <Typography variant="omega" textColor="danger600">
                {state.recoverable
                  ? 'Unable to load sync status — the engine may be temporarily unavailable.'
                  : 'Unable to load sync status.'}
              </Typography>
              <Typography variant="pi" textColor="danger500">
                {state.message}
              </Typography>
              {state.recoverable && (
                <Typography variant="pi" textColor="neutral600">
                  Click Refresh to retry.
                </Typography>
              )}
            </Flex>
          </Box>
        )}

        {/* Import success message */}
        {importState.status === 'success' && (
          <Box padding={3} background="success100" hasRadius>
            <Typography variant="omega" textColor="success600">
              Import complete: {importState.result.imported.flows} flows,{' '}
              {importState.result.imported.jdms} JDMs,{' '}
              {importState.result.imported.connections} connections imported.
            </Typography>
          </Box>
        )}

        {/* Import error */}
        {importState.status === 'error' && (
          <Box padding={3} background="danger100" hasRadius>
            <Typography variant="omega" textColor="danger600">
              Import failed: {importState.message}
            </Typography>
          </Box>
        )}

        {/* Summary */}
        {state.status === 'ok' && <SyncSummary status={state.data} />}

        {/* Items table */}
        {state.status === 'ok' && items.length > 0 && (
          <Table>
            <Thead>
              <Tr>
                <Th>
                  <Typography variant="sigma">Type</Typography>
                </Th>
                <Th>
                  <Typography variant="sigma">Name</Typography>
                </Th>
                <Th>
                  <Typography variant="sigma">Status</Typography>
                </Th>
                <Th>
                  <Typography variant="sigma">Actions</Typography>
                </Th>
              </Tr>
            </Thead>
            <Tbody>
              {items.map((item, i) => (
                <SyncItemRow
                  key={`${item.type}-${item.id}-${i}`}
                  item={item}
                  onImport={handleImportOne}
                  importing={isImporting}
                />
              ))}
            </Tbody>
          </Table>
        )}

        {/* Empty state */}
        {state.status === 'ok' && items.length === 0 && (
          <Typography variant="pi" textColor="neutral600">
            No flows, JDMs, or connections found in either CMS or engine.
          </Typography>
        )}
      </Flex>
    </Box>
  );
};

export default SyncPanel;
