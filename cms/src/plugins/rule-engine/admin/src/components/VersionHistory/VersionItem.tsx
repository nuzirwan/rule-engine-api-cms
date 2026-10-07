// VersionItem — single version row component for the VersionHistory panel.
// Shows version number, createdAt (relative time), createdBy, validation status badge,
// active badge. Includes 'View', 'Compare' checkbox, and 'Rollback' button (disabled if active).
// Follows the EntryCard pattern from AuditViewer.

import * as React from 'react';
import {
  Badge,
  Box,
  Button,
  Checkbox,
  Flex,
  Typography,
} from '@strapi/design-system';

import type { VersionSummary } from './types';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Format an ISO timestamp as a relative string (e.g., "2 hours ago") with the
 * full ISO value returned as a title attribute for hover precision.
 */
export function relativeTime(iso: string): { label: string; title: string } {
  const now = Date.now();
  const then = new Date(iso).getTime();
  const diffMs = then - now;
  const diffSec = Math.round(diffMs / 1_000);
  const diffMin = Math.round(diffSec / 60);
  const diffHour = Math.round(diffMin / 60);
  const diffDay = Math.round(diffHour / 24);

  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

  let label: string;
  if (Math.abs(diffSec) < 60) {
    label = rtf.format(diffSec, 'second');
  } else if (Math.abs(diffMin) < 60) {
    label = rtf.format(diffMin, 'minute');
  } else if (Math.abs(diffHour) < 24) {
    label = rtf.format(diffHour, 'hour');
  } else {
    label = rtf.format(diffDay, 'day');
  }

  return { label, title: iso };
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export interface VersionItemProps {
  version: VersionSummary;
  /** Whether this version is the currently active version. */
  isActive: boolean;
  /** Whether this version is selected for comparison. */
  isSelected: boolean;
  /** Whether comparison checkbox should be disabled (max selections reached). */
  compareDisabled: boolean;
  /** Called when 'View' button is clicked. */
  onView: (version: number) => void;
  /** Called when comparison checkbox is toggled. */
  onToggleCompare: (version: number) => void;
  /** Called when 'Rollback' button is clicked. */
  onRollback: (version: number) => void;
  /** Whether actions are disabled (e.g., during rollback). */
  actionsDisabled?: boolean;
}

export const VersionItem: React.FC<VersionItemProps> = ({
  version,
  isActive,
  isSelected,
  compareDisabled,
  onView,
  onToggleCompare,
  onRollback,
  actionsDisabled = false,
}) => {
  const { label: timeLabel, title: timeTitle } = relativeTime(version.createdAt);

  return (
    <Box
      padding={3}
      background={isActive ? 'primary100' : 'neutral0'}
      hasRadius
      borderColor={isActive ? 'primary200' : 'neutral200'}
      borderWidth="1px"
      borderStyle="solid"
    >
      <Flex direction="row" justifyContent="space-between" alignItems="center" gap={3}>
        {/* Version info section */}
        <Flex direction="column" alignItems="flex-start" gap={1} style={{ flex: 1 }}>
          <Flex direction="row" alignItems="center" gap={2}>
            <Typography variant="omega" fontWeight="bold">
              Version {version.version}
            </Typography>
            {isActive && (
              <Badge variant="success">Active</Badge>
            )}
            <Badge variant={version.validated ? 'success' : 'secondary'}>
              {version.validated ? 'Validated' : 'Unvalidated'}
            </Badge>
          </Flex>

          <Flex direction="row" alignItems="center" gap={2}>
            <Typography variant="pi" textColor="neutral600">
              by {version.createdBy}
            </Typography>
            <Typography
              variant="pi"
              textColor="neutral500"
              title={timeTitle}
              style={{ cursor: 'default' }}
            >
              {timeLabel}
            </Typography>
          </Flex>
        </Flex>

        {/* Actions section */}
        <Flex direction="row" alignItems="center" gap={2}>
          <Checkbox
            checked={isSelected}
            disabled={actionsDisabled || (!isSelected && compareDisabled)}
            onCheckedChange={() => onToggleCompare(version.version)}
            aria-label={`Select version ${version.version} for comparison`}
          />

          <Button
            variant="tertiary"
            size="S"
            onClick={() => onView(version.version)}
            disabled={actionsDisabled}
          >
            View
          </Button>

          <Button
            variant="secondary"
            size="S"
            onClick={() => onRollback(version.version)}
            disabled={actionsDisabled || isActive}
          >
            Rollback
          </Button>
        </Flex>
      </Flex>
    </Box>
  );
};

export default VersionItem;
