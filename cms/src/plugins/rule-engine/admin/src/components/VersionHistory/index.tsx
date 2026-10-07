// VersionHistory — main panel showing flow version history.
// Follows the SyncPanel pattern:
//   * @strapi/design-system components for layout and typography
//   * Fetch-on-mount via useFetchClient (Strapi 5 admin hook)
//   * Loading / error / success states with FetchState union
//
// Features:
//   * Collapsible panel with version list
//   * Pagination (show first 10, 'Load more' button)
//   * Track selectedVersions[] for comparison (max 2)
//   * Rollback with confirmation modal
//   * View opens preview modal
//   * Compare opens diff modal

import * as React from 'react';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import {
  Box,
  Button,
  Flex,
  Loader,
  Modal,
  Typography,
} from '@strapi/design-system';

import type { FlowVersionsResponse, RollbackResponse, VersionSummary, FlowDetail } from './types';
import { VersionItem } from './VersionItem';
import { VersionDiff } from '../VersionDiff';
import { VersionPreview } from '../VersionPreview';

export type { FlowVersionsResponse, RollbackResponse, VersionSummary };

// ---------------------------------------------------------------------------
// FetchState discriminated union
// ---------------------------------------------------------------------------

type FetchState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'error'; message: string; recoverable: boolean }
  | { status: 'ok'; data: FlowVersionsResponse };

type ActionState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'success'; message: string }
  | { status: 'error'; message: string };

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const PAGE_SIZE = 10;

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface VersionHistoryProps {
  /** The flow ID to display version history for. */
  flowId: string;
  /** The currently active version (for highlighting). */
  activeVersion?: number | null;
  /** Called after a successful rollback to allow parent to refresh. */
  onRollbackComplete?: () => void;
}

export const VersionHistory: React.FC<VersionHistoryProps> = ({
  flowId,
  activeVersion,
  onRollbackComplete,
}) => {
  const { get, post } = useFetchClient();
  const [state, setState] = React.useState<FetchState>({ status: 'idle' });
  const [actionState, setActionState] = React.useState<ActionState>({ status: 'idle' });
  const [collapsed, setCollapsed] = React.useState(false);

  // Pagination
  const [displayCount, setDisplayCount] = React.useState(PAGE_SIZE);

  // Comparison selection (max 2 versions)
  const [selectedVersions, setSelectedVersions] = React.useState<number[]>([]);

  // Modals
  const [rollbackVersion, setRollbackVersion] = React.useState<number | null>(null);
  const [previewVersion, setPreviewVersion] = React.useState<number | null>(null);
  const [showDiff, setShowDiff] = React.useState(false);

  // Version data for diff/preview (fetched when opening modals)
  const [diffData, setDiffData] = React.useState<{ older: FlowDetail; newer: FlowDetail } | null>(null);

  // ---------------------------------------------------------------------------
  // Fetch version list
  // ---------------------------------------------------------------------------

  const fetchVersions = React.useCallback(async () => {
    setState({ status: 'loading' });

    try {
      const { data } = await get<FlowVersionsResponse>(
        `/rule-engine/flows/${encodeURIComponent(flowId)}/versions`
      );
      setState({ status: 'ok', data });
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const recoverable = body?.recoverable ?? true;
      const message =
        body?.error ?? (err instanceof Error ? err.message : 'Failed to load version history');
      setState({ status: 'error', message, recoverable });
    }
  }, [flowId, get]);

  React.useEffect(() => {
    if (flowId) {
      fetchVersions();
    }
  }, [flowId, fetchVersions]);

  // ---------------------------------------------------------------------------
  // Rollback
  // ---------------------------------------------------------------------------

  const handleRollback = async () => {
    if (rollbackVersion === null) return;

    setActionState({ status: 'loading' });

    try {
      const { data } = await post<RollbackResponse>(
        `/rule-engine/flows/${encodeURIComponent(flowId)}/rollback`,
        { version: rollbackVersion }
      );
      setActionState({
        status: 'success',
        message: `Rolled back to version ${data.activeVersion}`,
      });
      setRollbackVersion(null);
      await fetchVersions();
      onRollbackComplete?.();
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const message =
        body?.error ?? (err instanceof Error ? err.message : 'Rollback failed');
      setActionState({ status: 'error', message });
    }
  };

  // ---------------------------------------------------------------------------
  // Compare versions
  // ---------------------------------------------------------------------------

  const handleToggleCompare = (version: number) => {
    setSelectedVersions((prev) => {
      if (prev.includes(version)) {
        return prev.filter((v) => v !== version);
      }
      if (prev.length >= 2) {
        // Replace oldest selection
        return [...prev.slice(1), version];
      }
      return [...prev, version];
    });
  };

  const handleOpenDiff = async () => {
    if (selectedVersions.length !== 2) return;

    setActionState({ status: 'loading' });

    try {
      const [v1, v2] = selectedVersions.sort((a, b) => a - b);
      const [resp1, resp2] = await Promise.all([
        get<FlowDetail>(`/rule-engine/flows/${encodeURIComponent(flowId)}?version=${v1}`),
        get<FlowDetail>(`/rule-engine/flows/${encodeURIComponent(flowId)}?version=${v2}`),
      ]);
      setDiffData({ older: resp1.data, newer: resp2.data });
      setShowDiff(true);
      setActionState({ status: 'idle' });
    } catch (err: unknown) {
      const body = (err as any)?.response?.data;
      const message =
        body?.error ?? (err instanceof Error ? err.message : 'Failed to load versions for comparison');
      setActionState({ status: 'error', message });
    }
  };

  // ---------------------------------------------------------------------------
  // Render helpers
  // ---------------------------------------------------------------------------

  const versions = state.status === 'ok' ? state.data.versions : [];
  const displayedVersions = versions.slice(0, displayCount);
  const hasMore = versions.length > displayCount;
  const isActionLoading = actionState.status === 'loading';

  // Determine active version from prop or first version's number if recent
  const effectiveActiveVersion = activeVersion ?? (versions.length > 0 ? versions[0].version : null);

  return (
    <Box padding={4} background="neutral100" hasRadius>
      <Flex direction="column" alignItems="stretch" gap={4}>
        {/* Header */}
        <Flex direction="row" justifyContent="space-between" alignItems="center">
          <Flex direction="row" alignItems="center" gap={2}>
            <Typography variant="delta">Version History</Typography>
            {state.status === 'ok' && (
              <Typography variant="pi" textColor="neutral600">
                ({versions.length} version{versions.length !== 1 ? 's' : ''})
              </Typography>
            )}
          </Flex>
          <Flex direction="row" gap={2}>
            {selectedVersions.length === 2 && (
              <Button
                variant="secondary"
                size="S"
                onClick={handleOpenDiff}
                disabled={isActionLoading}
              >
                Compare Selected
              </Button>
            )}
            <Button
              variant="tertiary"
              size="S"
              onClick={fetchVersions}
              disabled={isActionLoading}
            >
              Refresh
            </Button>
            <Button
              variant="ghost"
              size="S"
              onClick={() => setCollapsed(!collapsed)}
            >
              {collapsed ? 'Expand' : 'Collapse'}
            </Button>
          </Flex>
        </Flex>

        {/* Collapsed state */}
        {collapsed && (
          <Typography variant="pi" textColor="neutral600">
            Click Expand to view version history.
          </Typography>
        )}

        {!collapsed && (
          <>
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
                      ? 'Unable to load version history — the engine may be temporarily unavailable.'
                      : 'Unable to load version history.'}
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

            {/* Action success message */}
            {actionState.status === 'success' && (
              <Box padding={3} background="success100" hasRadius>
                <Typography variant="omega" textColor="success600">
                  {actionState.message}
                </Typography>
              </Box>
            )}

            {/* Action error */}
            {actionState.status === 'error' && (
              <Box padding={3} background="danger100" hasRadius>
                <Typography variant="omega" textColor="danger600">
                  {actionState.message}
                </Typography>
              </Box>
            )}

            {/* Version list */}
            {state.status === 'ok' && versions.length > 0 && (
              <Flex direction="column" alignItems="stretch" gap={2}>
                {displayedVersions.map((version) => (
                  <VersionItem
                    key={version.version}
                    version={version}
                    isActive={version.version === effectiveActiveVersion}
                    isSelected={selectedVersions.includes(version.version)}
                    compareDisabled={
                      selectedVersions.length >= 2 && !selectedVersions.includes(version.version)
                    }
                    onView={(v) => setPreviewVersion(v)}
                    onToggleCompare={handleToggleCompare}
                    onRollback={(v) => setRollbackVersion(v)}
                    actionsDisabled={isActionLoading}
                  />
                ))}

                {/* Load more */}
                {hasMore && (
                  <Button
                    variant="tertiary"
                    onClick={() => setDisplayCount((prev) => prev + PAGE_SIZE)}
                    disabled={isActionLoading}
                  >
                    Load more ({versions.length - displayCount} remaining)
                  </Button>
                )}
              </Flex>
            )}

            {/* Empty state */}
            {state.status === 'ok' && versions.length === 0 && (
              <Typography variant="pi" textColor="neutral600">
                No versions found for this flow.
              </Typography>
            )}
          </>
        )}
      </Flex>

      {/* Rollback confirmation modal */}
      <Modal.Root open={rollbackVersion !== null} onOpenChange={() => setRollbackVersion(null)}>
        <Modal.Content>
          <Modal.Header>
            <Modal.Title>Confirm Rollback</Modal.Title>
          </Modal.Header>
          <Modal.Body>
            <Typography variant="omega">
              Rollback flow to version {rollbackVersion}?
            </Typography>
            <Typography variant="pi" textColor="neutral600" style={{ marginTop: 8 }}>
              The active version will change
              {effectiveActiveVersion !== null && ` from version ${effectiveActiveVersion}`} to version{' '}
              {rollbackVersion}.
            </Typography>
          </Modal.Body>
          <Modal.Footer>
            <Modal.Close>
              <Button variant="tertiary">Cancel</Button>
            </Modal.Close>
            <Button
              variant="danger"
              onClick={handleRollback}
              disabled={isActionLoading}
            >
              {isActionLoading ? 'Rolling back...' : 'Rollback'}
            </Button>
          </Modal.Footer>
        </Modal.Content>
      </Modal.Root>

      {/* Version preview modal */}
      <Modal.Root open={previewVersion !== null} onOpenChange={() => setPreviewVersion(null)}>
        <Modal.Content style={{ maxWidth: '90vw', width: 1200 }}>
          <Modal.Header>
            <Modal.Title>Version {previewVersion} Preview</Modal.Title>
          </Modal.Header>
          <Modal.Body>
            {previewVersion !== null && (
              <VersionPreview flowId={flowId} version={previewVersion} />
            )}
          </Modal.Body>
          <Modal.Footer>
            <Modal.Close>
              <Button variant="tertiary">Close</Button>
            </Modal.Close>
          </Modal.Footer>
        </Modal.Content>
      </Modal.Root>

      {/* Version diff modal */}
      <Modal.Root open={showDiff} onOpenChange={() => setShowDiff(false)}>
        <Modal.Content style={{ maxWidth: '95vw', width: 1400 }}>
          <Modal.Header>
            <Modal.Title>
              Compare Versions {selectedVersions.sort((a, b) => a - b).join(' → ')}
            </Modal.Title>
          </Modal.Header>
          <Modal.Body>
            {diffData && (
              <VersionDiff olderVersion={diffData.older} newerVersion={diffData.newer} />
            )}
          </Modal.Body>
          <Modal.Footer>
            <Modal.Close>
              <Button variant="tertiary">Close</Button>
            </Modal.Close>
          </Modal.Footer>
        </Modal.Content>
      </Modal.Root>
    </Box>
  );
};

export default VersionHistory;
