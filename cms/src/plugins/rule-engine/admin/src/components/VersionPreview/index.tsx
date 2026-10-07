// VersionPreview — modal component showing a read-only flow canvas for a specific version.
// Fetches the full flow detail and renders FlowCanvasField in read-only mode.
// Shows validation status/errors from version metadata.
//
// PERF: FlowCanvasField is wrapped with React.Suspense to handle lazy-loaded chunks.

import * as React from 'react';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import { Box, Flex, Loader, Typography, Badge } from '@strapi/design-system';

import type { FlowDetail } from '../VersionHistory/types';
import FlowCanvasField from '../FlowCanvasField';
import { EditorSkeleton } from '../EditorSkeleton';

// ---------------------------------------------------------------------------
// FetchState
// ---------------------------------------------------------------------------

type FetchState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'error'; message: string; recoverable: boolean }
  | { status: 'ok'; data: FlowDetail };

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface VersionPreviewProps {
  /** The flow ID to preview. */
  flowId: string;
  /** The version number to preview. */
  version: number;
}

export const VersionPreview: React.FC<VersionPreviewProps> = ({ flowId, version }) => {
  const { get } = useFetchClient();
  const [state, setState] = React.useState<FetchState>({ status: 'idle' });

  React.useEffect(() => {
    if (!flowId || !version) return;

    setState({ status: 'loading' });

    get<FlowDetail>(`/rule-engine/flows/${encodeURIComponent(flowId)}?version=${version}`)
      .then(({ data }) => {
        setState({ status: 'ok', data });
      })
      .catch((err: unknown) => {
        const body = (err as any)?.response?.data;
        const recoverable = body?.recoverable ?? true;
        const message =
          body?.error ??
          (err instanceof Error ? err.message : 'Failed to load flow version');
        setState({ status: 'error', message, recoverable });
      });
  }, [flowId, version, get]);

  // Dummy onChange handler for read-only mode
  const noop = React.useCallback(() => {}, []);

  return (
    <Box padding={4} background="neutral100" hasRadius style={{ minHeight: 400 }}>
      <Flex direction="column" alignItems="stretch" gap={4}>
        {/* Loading */}
        {(state.status === 'loading' || state.status === 'idle') && (
          <Flex justifyContent="center" alignItems="center" padding={8}>
            <Loader />
          </Flex>
        )}

        {/* Error */}
        {state.status === 'error' && (
          <Box padding={3} background="danger100" hasRadius>
            <Flex direction="column" alignItems="stretch" gap={1}>
              <Typography variant="omega" textColor="danger600">
                {state.recoverable
                  ? 'Unable to load flow version — the engine may be temporarily unavailable.'
                  : 'Unable to load flow version.'}
              </Typography>
              <Typography variant="pi" textColor="danger500">
                {state.message}
              </Typography>
            </Flex>
          </Box>
        )}

        {/* Flow preview */}
        {state.status === 'ok' && (
          <>
            {/* Header with metadata */}
            <Flex direction="row" justifyContent="space-between" alignItems="center">
              <Flex direction="column" alignItems="flex-start" gap={1}>
                <Flex direction="row" alignItems="center" gap={2}>
                  <Typography variant="delta">
                    {state.data.method} {state.data.path}
                  </Typography>
                  <Badge variant="neutral">v{state.data.version}</Badge>
                </Flex>
                <Typography variant="pi" textColor="neutral600">
                  Flow ID: {state.data.flowId}
                </Typography>
              </Flex>
            </Flex>

            {/* Flow canvas in read-only mode, wrapped with Suspense */}
            <Box style={{ minHeight: 500 }}>
              <React.Suspense fallback={<EditorSkeleton height={500} label="Loading flow preview…" />}>
                <FlowCanvasField
                  name="tree-preview"
                  value={state.data.tree}
                  onChange={noop}
                  disabled={true}
                  intlLabel={{ defaultMessage: 'Flow Tree (Read-only)' }}
                />
              </React.Suspense>
            </Box>

            {/* Fixtures summary */}
            {state.data.fixtures && state.data.fixtures.length > 0 && (
              <Box padding={3} background="neutral0" hasRadius>
                <Typography variant="sigma" style={{ marginBottom: 8 }}>
                  Fixtures ({state.data.fixtures.length})
                </Typography>
                <Flex direction="column" alignItems="stretch" gap={2}>
                  {state.data.fixtures.map((fixture, i) => (
                    <Box key={i} padding={2} background="neutral100" hasRadius>
                      <Typography variant="omega" fontWeight="semiBold">
                        {fixture.name || `Fixture ${i + 1}`}
                      </Typography>
                      <pre
                        style={{
                          margin: '4px 0 0 0',
                          padding: 8,
                          backgroundColor: 'rgba(0,0,0,0.02)',
                          borderRadius: 4,
                          fontSize: 11,
                          overflow: 'auto',
                          maxHeight: 100,
                        }}
                      >
                        {JSON.stringify(fixture.input, null, 2)}
                      </pre>
                    </Box>
                  ))}
                </Flex>
              </Box>
            )}
          </>
        )}
      </Flex>
    </Box>
  );
};

export default VersionPreview;
