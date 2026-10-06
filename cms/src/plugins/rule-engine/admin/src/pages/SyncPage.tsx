// SyncPage — page wrapper for the SyncPanel component with standard Strapi
// admin page layout. This creates the page component only; registration in
// admin routes is a separate integration task.
//
// Follows Strapi 5 admin page conventions with @strapi/admin Layouts.

import * as React from 'react';
import { Box } from '@strapi/design-system';
import { Layouts } from '@strapi/admin/strapi-admin';

import { SyncPanel } from '../components/SyncPanel';

export interface SyncPageProps {
  /** Optional callback when an import completes (for integration). */
  onImportComplete?: () => void;
}

export const SyncPage: React.FC<SyncPageProps> = ({ onImportComplete }) => {
  return (
    <Layouts.Root>
      <Layouts.Header
        title="Engine Sync"
        subtitle="Compare and sync configuration between CMS and the rule engine"
      />
      <Layouts.Content>
        <Box padding={4}>
          <SyncPanel onImportComplete={onImportComplete} />
        </Box>
      </Layouts.Content>
    </Layouts.Root>
  );
};

export default SyncPage;
