// ConnectionsPage — Connection management page with Test Connection support (FEAT-003).
// Lists connections stored in Strapi with their type, host (from settings), and
// provides a Test Connection button that opens a modal to test connectivity.
//
// Unlike EnvironmentManager which has its own test endpoint (the environment already
// has the secret stored via secretRef), connections require the user to enter the
// password at test time since the CMS only stores a secretRef, not the actual secret.

import * as React from 'react';
import {
  Box,
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
import { Plus, ArrowClockwise, Play } from '@strapi/icons';
import { Layouts, useFetchClient } from '@strapi/admin/strapi-admin';
import { TestConnectionModal } from '../components/TestConnectionModal';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ConnectionSettings {
  __component?: string;
  id?: number;
  host?: string;
  port?: number;
  database?: string;
  user?: string;
  username?: string;
  baseUrl?: string;
  url?: string;
  brokers?: string[];
  addresses?: string[];
  [key: string]: unknown;
}

interface ConnectionEntry {
  id: number;
  documentId: string;
  key: string;
  type: 'postgres' | 'mysql' | 'valkey' | 'rest' | 'http' | 'kafka' | 'rabbitmq';
  settings: ConnectionSettings[];
  secretRef: string | null;
}

// Badge variants per connection type
const typeBadgeVariant: Record<string, 'primary' | 'secondary' | 'alternative' | 'success' | 'danger'> = {
  postgres: 'primary',
  mysql: 'primary',
  valkey: 'secondary',
  rest: 'alternative',
  http: 'alternative',
  kafka: 'success',
  rabbitmq: 'success',
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Extract a display-friendly host/endpoint from connection settings.
 */
function getConnectionHost(settings: ConnectionSettings[]): string {
  if (!settings || settings.length === 0) {
    return '—';
  }

  const first = settings[0];
  if (!first) {
    return '—';
  }

  // Try common host fields
  if (first.host) {
    const port = first.port ? `:${first.port}` : '';
    const db = first.database ? `/${first.database}` : '';
    return `${first.host}${port}${db}`;
  }

  if (first.baseUrl) {
    return first.baseUrl;
  }

  if (first.url) {
    return first.url;
  }

  if (first.brokers && Array.isArray(first.brokers)) {
    return first.brokers.join(', ');
  }

  if (first.addresses && Array.isArray(first.addresses)) {
    return first.addresses.join(', ');
  }

  return '—';
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export interface ConnectionsPageProps {
  /** Base URL path for the content manager (defaults to /admin/content-manager) */
  contentManagerBasePath?: string;
}

export const ConnectionsPage: React.FC<ConnectionsPageProps> = ({
  contentManagerBasePath = '/admin/content-manager',
}) => {
  const { get } = useFetchClient();
  
  const [connections, setConnections] = React.useState<ConnectionEntry[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);

  // Test connection modal state
  const [testModalOpen, setTestModalOpen] = React.useState(false);
  const [selectedConnection, setSelectedConnection] = React.useState<ConnectionEntry | null>(null);

  const fetchConnections = React.useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await get('/api/connections?populate=settings');
      const data = response.data?.data ?? [];
      setConnections(data);
    } catch (err) {
      setError((err as Error).message || 'Failed to load connections');
    } finally {
      setLoading(false);
    }
  }, [get]);

  React.useEffect(() => {
    fetchConnections();
  }, [fetchConnections]);

  const handleRowClick = (documentId: string) => {
    window.location.href = `${contentManagerBasePath}/collection-types/api::connection.connection/${documentId}`;
  };

  const handleCreate = () => {
    window.location.href = `${contentManagerBasePath}/collection-types/api::connection.connection/create`;
  };

  const handleTestConnection = (connection: ConnectionEntry) => {
    setSelectedConnection(connection);
    setTestModalOpen(true);
  };

  const handleCloseTestModal = () => {
    setTestModalOpen(false);
    setSelectedConnection(null);
  };

  return (
    <Layouts.Root>
      <Layouts.Header
        title="Connections"
        subtitle="Manage database and service connections. Test connectivity before syncing to the engine."
        primaryAction={
          <Flex gap={2}>
            <Button
              variant="secondary"
              startIcon={<ArrowClockwise />}
              onClick={fetchConnections}
              disabled={loading}
            >
              Refresh
            </Button>
            <Button startIcon={<Plus />} onClick={handleCreate}>
              Create Connection
            </Button>
          </Flex>
        }
      />
      <Layouts.Content>
        <Box padding={4}>
          {loading && (
            <Flex justifyContent="center" padding={8}>
              <Loader>Loading connections...</Loader>
            </Flex>
          )}

          {error && !loading && (
            <Box padding={4} background="danger100" hasRadius>
              <Typography textColor="danger600">{error}</Typography>
            </Box>
          )}

          {!loading && !error && connections.length === 0 && (
            <EmptyStateLayout
              content="No connections configured yet. Create one to connect to databases and external services."
              action={
                <Button startIcon={<Plus />} onClick={handleCreate}>
                  Create Connection
                </Button>
              }
            />
          )}

          {!loading && !error && connections.length > 0 && (
            <Table colCount={5} rowCount={connections.length + 1}>
              <Thead>
                <Tr>
                  <Th>
                    <Typography variant="sigma">Key</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Type</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Host / Endpoint</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Secret Ref</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Actions</Typography>
                  </Th>
                </Tr>
              </Thead>
              <Tbody>
                {connections.map((connection) => (
                  <Tr key={connection.documentId}>
                    <Td
                      onClick={() => handleRowClick(connection.documentId)}
                      style={{ cursor: 'pointer' }}
                    >
                      <Typography fontWeight="bold">{connection.key}</Typography>
                    </Td>
                    <Td>
                      <Badge variant={typeBadgeVariant[connection.type] || 'alternative'}>
                        {connection.type}
                      </Badge>
                    </Td>
                    <Td
                      onClick={() => handleRowClick(connection.documentId)}
                      style={{ cursor: 'pointer' }}
                    >
                      <Typography textColor="neutral600">
                        {getConnectionHost(connection.settings)}
                      </Typography>
                    </Td>
                    <Td>
                      <Typography textColor={connection.secretRef ? undefined : 'neutral500'}>
                        {connection.secretRef || '—'}
                      </Typography>
                    </Td>
                    <Td>
                      <Flex gap={2}>
                        <Button
                          variant="secondary"
                          size="S"
                          startIcon={<Play />}
                          onClick={() => handleTestConnection(connection)}
                        >
                          Test
                        </Button>
                      </Flex>
                    </Td>
                  </Tr>
                ))}
              </Tbody>
            </Table>
          )}
        </Box>
      </Layouts.Content>

      {/* Test Connection Modal */}
      {selectedConnection && (
        <TestConnectionModal
          isOpen={testModalOpen}
          onClose={handleCloseTestModal}
          connectionType={selectedConnection.type}
          settings={selectedConnection.settings as unknown as Record<string, unknown>}
          connectionKey={selectedConnection.key}
        />
      )}
    </Layouts.Root>
  );
};

export default ConnectionsPage;
