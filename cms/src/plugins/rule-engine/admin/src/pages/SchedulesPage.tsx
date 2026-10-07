// SchedulesPage — basic schedule management page (FEAT-002). Lists schedules
// with their cron expression, timezone, flow, and sync status, and offers a
// per-row Publish/Sync action that pushes the schedule to the engine via the
// plugin route POST /rule-engine/schedules/:id/publish.
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

interface ScheduleEntry {
  id: number;
  documentId: string;
  scheduleId: string;
  name: string;
  schedule: string;
  timezone?: string;
  enabled: boolean;
  flowId?: { flowId: string } | null;
  lastSyncStatus: 'none' | 'synced' | 'failed';
}

const statusBadgeVariant: Record<string, 'success' | 'danger' | 'neutral'> = {
  synced: 'success',
  failed: 'danger',
  none: 'neutral',
};

export interface SchedulesPageProps {
  /** Base URL path for the content manager (defaults to /admin/content-manager) */
  contentManagerBasePath?: string;
}

export const SchedulesPage: React.FC<SchedulesPageProps> = ({
  contentManagerBasePath = '/admin/content-manager',
}) => {
  const { get, post } = useFetchClient();
  const [schedules, setSchedules] = React.useState<ScheduleEntry[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);
  const [publishingId, setPublishingId] = React.useState<string | null>(null);
  const [publishError, setPublishError] = React.useState<string | null>(null);

  const fetchSchedules = React.useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await get('/api/schedules?populate=flowId');
      const data = response.data?.data ?? [];
      setSchedules(data);
    } catch (err) {
      setError((err as Error).message || 'Failed to load schedules');
    } finally {
      setLoading(false);
    }
  }, [get]);

  React.useEffect(() => {
    fetchSchedules();
  }, [fetchSchedules]);

  const handleRowClick = (documentId: string) => {
    window.location.href = `${contentManagerBasePath}/collection-types/api::schedule.schedule/${documentId}`;
  };

  const handleCreate = () => {
    window.location.href = `${contentManagerBasePath}/collection-types/api::schedule.schedule/create`;
  };

  const handlePublish = async (scheduleId: string) => {
    setPublishingId(scheduleId);
    setPublishError(null);
    try {
      await post(`/rule-engine/schedules/${encodeURIComponent(scheduleId)}/publish`, {});
      await fetchSchedules();
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
        title="Schedules"
        subtitle="Manage cron-based schedules that trigger flows"
        primaryAction={
          <Flex gap={2}>
            <Button
              variant="secondary"
              startIcon={<ArrowClockwise />}
              onClick={fetchSchedules}
              disabled={loading}
            >
              Refresh
            </Button>
            <Button startIcon={<Plus />} onClick={handleCreate}>
              Create Schedule
            </Button>
          </Flex>
        }
      />
      <Layouts.Content>
        <Box padding={4}>
          {loading && (
            <Flex justifyContent="center" padding={8}>
              <Loader>Loading schedules...</Loader>
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

          {!loading && !error && schedules.length === 0 && (
            <EmptyStateLayout
              content="No schedules configured yet. Create one to trigger a flow on a cron."
              action={
                <Button startIcon={<Plus />} onClick={handleCreate}>
                  Create Schedule
                </Button>
              }
            />
          )}

          {!loading && !error && schedules.length > 0 && (
            <Table colCount={6} rowCount={schedules.length + 1}>
              <Thead>
                <Tr>
                  <Th>
                    <Typography variant="sigma">Schedule ID</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Name</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Cron</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Flow</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Sync Status</Typography>
                  </Th>
                  <Th>
                    <Typography variant="sigma">Actions</Typography>
                  </Th>
                </Tr>
              </Thead>
              <Tbody>
                {schedules.map((schedule) => (
                  <Tr key={schedule.documentId}>
                    <Td onClick={() => handleRowClick(schedule.documentId)} style={{ cursor: 'pointer' }}>
                      <Typography>{schedule.scheduleId}</Typography>
                    </Td>
                    <Td onClick={() => handleRowClick(schedule.documentId)} style={{ cursor: 'pointer' }}>
                      <Typography fontWeight="bold">{schedule.name}</Typography>
                    </Td>
                    <Td>
                      <Typography>{schedule.schedule}</Typography>
                    </Td>
                    <Td>
                      <Typography textColor={schedule.flowId ? undefined : 'neutral500'}>
                        {schedule.flowId?.flowId || '—'}
                      </Typography>
                    </Td>
                    <Td>
                      <Badge variant={statusBadgeVariant[schedule.lastSyncStatus] || 'neutral'}>
                        {schedule.lastSyncStatus}
                      </Badge>
                    </Td>
                    <Td>
                      <Button
                        variant="secondary"
                        size="S"
                        onClick={() => handlePublish(schedule.scheduleId)}
                        loading={publishingId === schedule.scheduleId}
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

export default SchedulesPage;
