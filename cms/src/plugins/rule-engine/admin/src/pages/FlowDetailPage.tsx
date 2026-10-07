// FlowDetailPage — page that accepts flowId param, shows flow info header,
// and embeds the VersionHistory panel.
// Follows Strapi 5 admin page conventions with @strapi/admin Layouts.

import * as React from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import { Box, Button, Flex, Loader, Typography, Badge } from '@strapi/design-system';
import { Layouts } from '@strapi/admin/strapi-admin';

import { VersionHistory } from '../components/VersionHistory';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface FlowInfo {
  flowId: string;
  method: string;
  path: string;
  activeVersion: number | null;
}

type FetchState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'error'; message: string }
  | { status: 'ok'; data: FlowInfo };

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface FlowDetailPageProps {}

export const FlowDetailPage: React.FC<FlowDetailPageProps> = () => {
  const { flowId } = useParams<{ flowId: string }>();
  const navigate = useNavigate();
  const { get } = useFetchClient();
  const [state, setState] = React.useState<FetchState>({ status: 'idle' });

  // Fetch flow info
  React.useEffect(() => {
    if (!flowId) return;

    setState({ status: 'loading' });

    get<FlowInfo>(`/rule-engine/flows/${encodeURIComponent(flowId)}`)
      .then(({ data }) => {
        setState({ status: 'ok', data });
      })
      .catch((err: unknown) => {
        const body = (err as any)?.response?.data;
        const message =
          body?.error ?? (err instanceof Error ? err.message : 'Failed to load flow');
        setState({ status: 'error', message });
      });
  }, [flowId, get]);

  const handleBack = () => {
    navigate(-1);
  };

  const handleRollbackComplete = () => {
    // Refresh flow info after rollback
    if (flowId) {
      get<FlowInfo>(`/rule-engine/flows/${encodeURIComponent(flowId)}`)
        .then(({ data }) => {
          setState({ status: 'ok', data });
        })
        .catch(() => {
          // Ignore errors on refresh, the version list will show updated state
        });
    }
  };

  if (!flowId) {
    return (
      <Layouts.Root>
        <Layouts.Header title="Flow Detail" />
        <Layouts.Content>
          <Box padding={4}>
            <Typography variant="omega" textColor="danger600">
              No flow ID provided.
            </Typography>
          </Box>
        </Layouts.Content>
      </Layouts.Root>
    );
  }

  return (
    <Layouts.Root>
      <Layouts.Header
        title={state.status === 'ok' ? `${state.data.method} ${state.data.path}` : 'Flow Detail'}
        subtitle={state.status === 'ok' ? `Flow ID: ${state.data.flowId}` : undefined}
        navigationAction={
          <Button variant="tertiary" onClick={handleBack}>
            ← Back
          </Button>
        }
      />
      <Layouts.Content>
        <Box padding={4}>
          <Flex direction="column" alignItems="stretch" gap={4}>
            {/* Loading */}
            {(state.status === 'loading' || state.status === 'idle') && (
              <Flex justifyContent="center" padding={8}>
                <Loader />
              </Flex>
            )}

            {/* Error */}
            {state.status === 'error' && (
              <Box padding={3} background="danger100" hasRadius>
                <Typography variant="omega" textColor="danger600">
                  {state.message}
                </Typography>
              </Box>
            )}

            {/* Flow info header */}
            {state.status === 'ok' && (
              <Box padding={4} background="neutral0" hasRadius>
                <Flex direction="row" justifyContent="space-between" alignItems="center">
                  <Flex direction="column" alignItems="flex-start" gap={2}>
                    <Flex direction="row" alignItems="center" gap={2}>
                      <Typography variant="beta">
                        {state.data.method} {state.data.path}
                      </Typography>
                      {state.data.activeVersion !== null && (
                        <Badge variant="success">Active: v{state.data.activeVersion}</Badge>
                      )}
                      {state.data.activeVersion === null && (
                        <Badge variant="secondary">No active version</Badge>
                      )}
                    </Flex>
                    <Typography variant="pi" textColor="neutral600">
                      Flow ID: {state.data.flowId}
                    </Typography>
                  </Flex>
                </Flex>
              </Box>
            )}

            {/* Version history panel */}
            {state.status === 'ok' && (
              <VersionHistory
                flowId={state.data.flowId}
                activeVersion={state.data.activeVersion}
                onRollbackComplete={handleRollbackComplete}
              />
            )}
          </Flex>
        </Box>
      </Layouts.Content>
    </Layouts.Root>
  );
};

export default FlowDetailPage;
