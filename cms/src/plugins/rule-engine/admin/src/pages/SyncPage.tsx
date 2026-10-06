// SyncPage — page wrapper for the SyncPanel component with standard Strapi
// admin page layout. This creates the page component only; registration in
// admin routes is a separate integration task.
//
// Follows Strapi 5 admin page conventions with @strapi/design-system layout.

import * as React from 'react';
import { Box, Main, HeaderLayout, ContentLayout } from '@strapi/design-system';

import { SyncPanel } from '../components/SyncPanel';

export interface SyncPageProps {
  /** Optional callback when an import completes (for integration). */
  onImportComplete?: () => void;
}

export const SyncPage: React.FC<SyncPageProps> = ({ onImportComplete }) => {
  return (
    <Main>
      <HeaderLayout
        title="Engine Sync"
        subtitle="Compare and sync configuration between CMS and the rule engine"
      />
      <ContentLayout>
        <Box padding={4}>
          <SyncPanel onImportComplete={onImportComplete} />
        </Box>
      </ContentLayout>
    </Main>
  );
};

export default SyncPage;
