// WebhooksPage — basic webhook management page (FEAT-004). Lists webhooks with
// their name, provider, flowId, and sync status. Clicking a row navigates to
// the Strapi content manager for editing (no custom editor needed initially).
//
// Follows Strapi 5 admin page conventions with @strapi/design-system layout.

import * as React from 'react';
import {
  Box,
  Main,
  HeaderLayout,
  ContentLayout,
  Table,
  Thead,
  Tbody,
  Tr,
  Th,
  Td,
  Typography,
  Button,
  Badge,
  Flex,
  EmptyStateLayout,
  Loader,
} from '@strapi/design-system';
import { Plus, Refresh } from '@strapi/icons';
import { useFetchClient } from '@strapi/admin/strapi-admin';

interface WebhookEntry {
  id: number;
  documentId: string;
  webhookId: string;
  name: string;
  provider: 'stripe' | 'github' | 'generic';
  flowId?: { flowId: string } | null;
  lastSyncStatus: 'none' | 'synced' | 'failed';
}

const providerBadgeVariant: Record<string, 'primary' | 'secondary' | 'alternative'> = {
  stripe: 'primary',
  github: 'secondary',
  generic: 'alternative',
};

const statusBadgeVariant: Record<string, 'success' | 'danger' | 'neutral'> = {
  synced: 'success',
  failed: 'danger',
  none: 'neutral',
};

export interface WebhooksPageProps {
  /** Base URL path for the content manager (defaults to /admin/content-manager) */
  contentManagerBasePath?: string;
}

export const WebhooksPage: React.FC<WebhooksPageProps> = ({
  contentManagerBasePath = '/admin/content-manager',
}) => {
  const { get } = useFetchClient();
  const [webhooks, setWebhooks] = React.useState<WebhookEntry[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);

  const fetchWebhooks = React.useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await get('/api/webhooks?populate=flowId');
      const data = response.data?.data ?? [];
      setWebhooks(data);
    } catch (err) {
      setError((err as Error).message || 'Failed to load webhooks');
    } finally {
      setLoading(false);
    }
  }, [get]);

  React.useEffect(() => {
    fetchWebhooks();
  }, [fetchWebhooks]);

  const handleRowClick = (documentId: string) => {
    // Navigate to the content manager edit page for this webhook
    window.location.href = `${contentManagerBasePath}/collection-types/api::webhook.webhook/${documentId}`;
  };

  const handleCreate = () => {
    window.location.href = `${contentManagerBasePath}/collection-types/api::webhook.webhook/create`;
  };

  return (
    <Main>
      <HeaderLayout
        title="Webhooks"
        subtitle="Manage webhook configurations for external event sources"
        primaryAction={
          <Flex gap={2}>
            <Button
              variant="secondary"
              startIcon={<Refresh />}
              onClick={fetchWebhooks}
              disabled={loading}
            >
              Refresh
            </Button>
            <Button startIcon={<Plus />} onClick={handleCreate}>
              Create Webhook
            </Button>
          </Flex>
        }
      />
      <ContentLayout>
        <Box padding={4}>
          {loading && (
            <Flex justifyContent="center" padding={8}>
              <Loader>Loading webhooks...</Loader>
            </Flex>
          )}

          {error && !loading && (
            <Box padding={4} background="danger100" hasRadius>
              <Typography textColor="danger600">{error}</Typography>
            </Box>
          )}

          {!loading && !error && webhooks.length === 0 && (
            <EmptyStateLayout
              content="No webhooks configured yet. Create one to start receiving external events."
              action={
                <Button startIcon={<Plus />} onClick={handleCreate}>
                  Create Webhook
                </Button>
              }
            />
          )}

          {!loading && !error && webhooks.length > 0 && (
            <Table colCount={5} rowCount={webhooks.length + 1}>
              <Thead>
                <Tr>
                  <Th>
                    <Typography variant="sigma">Webhook ID</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Name</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Provider</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Flow</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Sync Status</Typography>
                  </Th>
                </Tr>
              </Thead>
              <Tbody>
                {webhooks.map((webhook) => (
                  <Tr
                    key={webhook.documentId}
                    onClick={() => handleRowClick(webhook.documentId)}
                    style={{ cursor: 'pointer' }}
                  >
                    <Td>
                      <Typography>{webhook.webhookId}</Typography>
                    </Td>
                    <Td>
                      <Typography fontWeight="bold">{webhook.name}</Typography>
                    </Td>
                    <Td>
                      <Badge variant={providerBadgeVariant[webhook.provider] || 'alternative'}>
                        {webhook.provider}
                      </Badge>
                    </Td>
                    <Td>
                      <Typography textColor={webhook.flowId ? undefined : 'neutral500'}>
                        {webhook.flowId?.flowId || '—'}
                      </Typography>
                    </Td>
                    <Td>
                      <Badge variant={statusBadgeVariant[webhook.lastSyncStatus] || 'neutral'}>
                        {webhook.lastSyncStatus}
                      </Badge>
                    </Td>
                  </Tr>
                ))}
              </Tbody>
            </Table>
          )}
        </Box>
      </ContentLayout>
    </Main>
  );
};

export default WebhooksPage;
