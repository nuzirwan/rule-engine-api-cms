// GroupsPage — basic worker-group management page (FEAT-002). Lists groups with
// their scaling mode, replica range, enabled flag, and offers a per-row
// Publish/Sync action that pushes the group to the engine via the plugin route
// POST /rule-engine/groups/:id/publish.
//
// Follows the WebhooksPage / SyncPage pattern: Layouts.* from
// '@strapi/admin/strapi-admin', @strapi/design-system Table, useFetchClient.
// Editing is delegated to the Strapi content manager (no custom editor here).

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
import { Plus, ArrowClockwise } from '@strapi/icons';
import { Layouts, useFetchClient } from '@strapi/admin/strapi-admin';

interface GroupEntry {
  id: number;
  documentId: string;
  groupId: string;
  name: string;
  enabled: boolean;
  scalingMode: 'static' | 'dynamic' | 'ephemeral';
  minReplicas: number;
  maxReplicas: number;
}

const modeBadgeVariant: Record<string, 'primary' | 'secondary' | 'alternative'> = {
  static: 'secondary',
  dynamic: 'primary',
  ephemeral: 'alternative',
};

export interface GroupsPageProps {
  /** Base URL path for the content manager (defaults to /admin/content-manager) */
  contentManagerBasePath?: string;
}

export const GroupsPage: React.FC<GroupsPageProps> = ({
  contentManagerBasePath = '/admin/content-manager',
}) => {
  const { get, post } = useFetchClient();
  const [groups, setGroups] = React.useState<GroupEntry[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);
  const [publishingId, setPublishingId] = React.useState<string | null>(null);
  const [publishError, setPublishError] = React.useState<string | null>(null);

  const fetchGroups = React.useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await get('/api/groups');
      const data = response.data?.data ?? [];
      setGroups(data);
    } catch (err) {
      setError((err as Error).message || 'Failed to load groups');
    } finally {
      setLoading(false);
    }
  }, [get]);

  React.useEffect(() => {
    fetchGroups();
  }, [fetchGroups]);

  const handleRowClick = (documentId: string) => {
    window.location.href = `${contentManagerBasePath}/collection-types/api::group.group/${documentId}`;
  };

  const handleCreate = () => {
    window.location.href = `${contentManagerBasePath}/collection-types/api::group.group/create`;
  };

  const handlePublish = async (groupId: string) => {
    setPublishingId(groupId);
    setPublishError(null);
    try {
      await post(`/rule-engine/groups/${encodeURIComponent(groupId)}/publish`, {});
      await fetchGroups();
    } catch (err) {
      const body = (err as any)?.response?.data;
      setPublishError(body?.error ?? (err as Error).message ?? 'Publish failed');
    } finally {
      setPublishingId(null);
    }
  };

  return (
    <Layouts.Root>
      <Layouts.Header
        title="Groups"
        subtitle="Manage worker groups with shared scaling policy and connection pool"
        primaryAction={
          <Flex gap={2}>
            <Button
              variant="secondary"
              startIcon={<ArrowClockwise />}
              onClick={fetchGroups}
              disabled={loading}
            >
              Refresh
            </Button>
            <Button startIcon={<Plus />} onClick={handleCreate}>
              Create Group
            </Button>
          </Flex>
        }
      />
      <Layouts.Content>
        <Box padding={4}>
          {loading && (
            <Flex justifyContent="center" padding={8}>
              <Loader>Loading groups...</Loader>
            </Flex>
          )}

          {error && !loading && (
            <Box padding={4} background="danger100" hasRadius>
              <Typography textColor="danger600">{error}</Typography>
            </Box>
          )}

          {publishError && !loading && (
            <Box padding={4} background="danger100" hasRadius marginBottom={4}>
              <Typography textColor="danger600">Publish failed: {publishError}</Typography>
            </Box>
          )}

          {!loading && !error && groups.length === 0 && (
            <EmptyStateLayout
              content="No groups configured yet. Create one to define a scaling policy."
              action={
                <Button startIcon={<Plus />} onClick={handleCreate}>
                  Create Group
                </Button>
              }
            />
          )}

          {!loading && !error && groups.length > 0 && (
            <Table colCount={6} rowCount={groups.length + 1}>
              <Thead>
                <Tr>
                  <Th>
                    <Typography variant="sigma">Group ID</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Name</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Mode</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Replicas</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Enabled</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Actions</Typography>
                  </Th>
                </Tr>
              </Thead>
              <Tbody>
                {groups.map((group) => (
                  <Tr key={group.documentId}>
                    <Td onClick={() => handleRowClick(group.documentId)} style={{ cursor: 'pointer' }}>
                      <Typography>{group.groupId}</Typography>
                    </Td>
                    <Td onClick={() => handleRowClick(group.documentId)} style={{ cursor: 'pointer' }}>
                      <Typography fontWeight="bold">{group.name}</Typography>
                    </Td>
                    <Td>
                      <Badge variant={modeBadgeVariant[group.scalingMode] || 'alternative'}>
                        {group.scalingMode}
                      </Badge>
                    </Td>
                    <Td>
                      <Typography>
                        {group.minReplicas}–{group.maxReplicas}
                      </Typography>
                    </Td>
                    <Td>
                      <Badge variant={group.enabled ? 'success' : 'neutral'}>
                        {group.enabled ? 'enabled' : 'disabled'}
                      </Badge>
                    </Td>
                    <Td>
                      <Button
                        variant="secondary"
                        size="S"
                        onClick={() => handlePublish(group.groupId)}
                        loading={publishingId === group.groupId}
                        disabled={publishingId !== null}
                      >
                        Publish
                      </Button>
                    </Td>
                  </Tr>
                ))}
              </Tbody>
            </Table>
          )}
        </Box>
      </Layouts.Content>
    </Layouts.Root>
  );
};

export default GroupsPage;
