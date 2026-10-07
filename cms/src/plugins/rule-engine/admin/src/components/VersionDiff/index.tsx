// VersionDiff — modal component showing JSON diff between two flow versions.
// Computes diff between two version trees and displays side-by-side view with
// visual highlighting (green=added, red=removed, yellow=changed).
// Follows the AuditViewer pattern for layout.

import * as React from 'react';
import { Box, Flex, Typography, Badge } from '@strapi/design-system';

import type { FlowDetail } from '../VersionHistory/types';
import { jsonDiff, formatValue, groupDiffsBySection, type DiffEntry } from './diff';

export { jsonDiff, formatValue, groupDiffsBySection, type DiffEntry };

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

interface DiffLineProps {
  diff: DiffEntry;
}

function DiffLine({ diff }: DiffLineProps): React.JSX.Element {
  const getBadgeVariant = (type: DiffEntry['type']) => {
    switch (type) {
      case 'added':
        return 'success';
      case 'removed':
        return 'danger';
      case 'changed':
        return 'secondary';
      default:
        return 'neutral';
    }
  };

  const getBackgroundColor = (type: DiffEntry['type']) => {
    switch (type) {
      case 'added':
        return 'success100';
      case 'removed':
        return 'danger100';
      case 'changed':
        return 'warning100';
      default:
        return 'neutral100';
    }
  };

  return (
    <Box
      padding={2}
      background={getBackgroundColor(diff.type)}
      hasRadius
      style={{ marginBottom: 4 }}
    >
      <Flex direction="column" alignItems="stretch" gap={1}>
        <Flex direction="row" alignItems="center" gap={2}>
          <Badge variant={getBadgeVariant(diff.type)}>
            {diff.type.charAt(0).toUpperCase() + diff.type.slice(1)}
          </Badge>
          <Typography variant="omega" fontWeight="semiBold" style={{ fontFamily: 'monospace' }}>
            {diff.path}
          </Typography>
        </Flex>

        {diff.type === 'changed' && (
          <Flex direction="row" gap={4} wrap="wrap">
            <Box style={{ flex: 1, minWidth: 200 }}>
              <Typography variant="pi" textColor="danger600" style={{ fontWeight: 600 }}>
                Old:
              </Typography>
              <pre
                style={{
                  margin: '4px 0',
                  padding: 8,
                  backgroundColor: 'rgba(220, 38, 38, 0.1)',
                  borderRadius: 4,
                  fontSize: 12,
                  overflow: 'auto',
                  maxHeight: 150,
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                }}
              >
                {formatValue(diff.oldValue)}
              </pre>
            </Box>
            <Box style={{ flex: 1, minWidth: 200 }}>
              <Typography variant="pi" textColor="success600" style={{ fontWeight: 600 }}>
                New:
              </Typography>
              <pre
                style={{
                  margin: '4px 0',
                  padding: 8,
                  backgroundColor: 'rgba(34, 197, 94, 0.1)',
                  borderRadius: 4,
                  fontSize: 12,
                  overflow: 'auto',
                  maxHeight: 150,
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                }}
              >
                {formatValue(diff.newValue)}
              </pre>
            </Box>
          </Flex>
        )}

        {diff.type === 'added' && (
          <Box>
            <Typography variant="pi" textColor="success600" style={{ fontWeight: 600 }}>
              Value:
            </Typography>
            <pre
              style={{
                margin: '4px 0',
                padding: 8,
                backgroundColor: 'rgba(34, 197, 94, 0.1)',
                borderRadius: 4,
                fontSize: 12,
                overflow: 'auto',
                maxHeight: 150,
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
              }}
            >
              {formatValue(diff.newValue)}
            </pre>
          </Box>
        )}

        {diff.type === 'removed' && (
          <Box>
            <Typography variant="pi" textColor="danger600" style={{ fontWeight: 600 }}>
              Value:
            </Typography>
            <pre
              style={{
                margin: '4px 0',
                padding: 8,
                backgroundColor: 'rgba(220, 38, 38, 0.1)',
                borderRadius: 4,
                fontSize: 12,
                overflow: 'auto',
                maxHeight: 150,
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
              }}
            >
              {formatValue(diff.oldValue)}
            </pre>
          </Box>
        )}
      </Flex>
    </Box>
  );
}

interface DiffSectionProps {
  title: string;
  diffs: DiffEntry[];
}

function DiffSection({ title, diffs }: DiffSectionProps): React.JSX.Element {
  const addedCount = diffs.filter((d) => d.type === 'added').length;
  const removedCount = diffs.filter((d) => d.type === 'removed').length;
  const changedCount = diffs.filter((d) => d.type === 'changed').length;

  return (
    <Box padding={3} background="neutral0" hasRadius style={{ marginBottom: 8 }}>
      <Flex direction="column" alignItems="stretch" gap={2}>
        <Flex direction="row" alignItems="center" gap={2}>
          <Typography variant="delta">{title}</Typography>
          <Flex direction="row" gap={1}>
            {addedCount > 0 && (
              <Badge variant="success">+{addedCount}</Badge>
            )}
            {removedCount > 0 && (
              <Badge variant="danger">-{removedCount}</Badge>
            )}
            {changedCount > 0 && (
              <Badge variant="secondary">~{changedCount}</Badge>
            )}
          </Flex>
        </Flex>
        {diffs.map((diff, i) => (
          <DiffLine key={`${diff.path}-${i}`} diff={diff} />
        ))}
      </Flex>
    </Box>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface VersionDiffProps {
  /** The older version (left side). */
  olderVersion: FlowDetail;
  /** The newer version (right side). */
  newerVersion: FlowDetail;
}

export const VersionDiff: React.FC<VersionDiffProps> = ({
  olderVersion,
  newerVersion,
}) => {
  // Compute diffs for tree and fixtures separately
  const treeDiffs = React.useMemo(
    () => jsonDiff(olderVersion.tree, newerVersion.tree, 'tree'),
    [olderVersion.tree, newerVersion.tree]
  );

  const fixturesDiffs = React.useMemo(
    () => jsonDiff(olderVersion.fixtures ?? [], newerVersion.fixtures ?? [], 'fixtures'),
    [olderVersion.fixtures, newerVersion.fixtures]
  );

  const metaDiffs = React.useMemo(() => {
    const diffs: DiffEntry[] = [];
    if (olderVersion.method !== newerVersion.method) {
      diffs.push({
        path: 'method',
        type: 'changed',
        oldValue: olderVersion.method,
        newValue: newerVersion.method,
      });
    }
    if (olderVersion.path !== newerVersion.path) {
      diffs.push({
        path: 'path',
        type: 'changed',
        oldValue: olderVersion.path,
        newValue: newerVersion.path,
      });
    }
    return diffs;
  }, [olderVersion, newerVersion]);

  const allDiffs = [...metaDiffs, ...treeDiffs, ...fixturesDiffs];
  const groupedDiffs = groupDiffsBySection(allDiffs);

  const totalAdded = allDiffs.filter((d) => d.type === 'added').length;
  const totalRemoved = allDiffs.filter((d) => d.type === 'removed').length;
  const totalChanged = allDiffs.filter((d) => d.type === 'changed').length;

  return (
    <Box padding={4} background="neutral100" hasRadius>
      <Flex direction="column" alignItems="stretch" gap={4}>
        {/* Header */}
        <Flex direction="row" justifyContent="space-between" alignItems="center">
          <Flex direction="row" alignItems="center" gap={2}>
            <Typography variant="delta">
              Version {olderVersion.version} → Version {newerVersion.version}
            </Typography>
          </Flex>
          <Flex direction="row" gap={2}>
            <Badge variant="success">+{totalAdded} added</Badge>
            <Badge variant="danger">-{totalRemoved} removed</Badge>
            <Badge variant="secondary">~{totalChanged} changed</Badge>
          </Flex>
        </Flex>

        {/* No changes */}
        {allDiffs.length === 0 && (
          <Box padding={4} background="neutral0" hasRadius>
            <Typography variant="omega" textColor="neutral600">
              No differences found between these versions.
            </Typography>
          </Box>
        )}

        {/* Diff sections */}
        {groupedDiffs.size > 0 && (
          <Flex direction="column" alignItems="stretch" gap={2}>
            {Array.from(groupedDiffs.entries()).map(([section, diffs]) => (
              <DiffSection key={section} title={section} diffs={diffs} />
            ))}
          </Flex>
        )}

        {/* Fixtures section if there are fixture diffs */}
        {fixturesDiffs.length > 0 && !groupedDiffs.has('fixtures') && (
          <Box padding={3} background="neutral0" hasRadius>
            <Typography variant="delta" style={{ marginBottom: 8 }}>
              Fixtures
            </Typography>
            {fixturesDiffs.map((diff, i) => (
              <DiffLine key={`fixture-${i}`} diff={diff} />
            ))}
          </Box>
        )}
      </Flex>
    </Box>
  );
};

export default VersionDiff;
