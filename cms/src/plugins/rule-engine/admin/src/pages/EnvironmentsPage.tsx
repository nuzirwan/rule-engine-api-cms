// EnvironmentsPage — page wrapper for the EnvironmentManager component.
// Follows the SyncPage pattern with standard Strapi admin page layout.
//
// Provides full CRUD management for environments with test connection capability.

import * as React from 'react';
import { Box } from '@strapi/design-system';
import { Layouts } from '@strapi/admin/strapi-admin';

import { EnvironmentManager } from '../components/EnvironmentManager';

export interface EnvironmentsPageProps {
  /** Optional callback when any environment is updated (for integration). */
  onUpdate?: () => void;
}

export const EnvironmentsPage: React.FC<EnvironmentsPageProps> = ({ onUpdate }) => {
  return (
    <Layouts.Root>
      <Layouts.Header
        title="Environments"
        subtitle="Manage engine deployments and their connection settings"
      />
      <Layouts.Content>
        <Box padding={4}>
          <EnvironmentManager onUpdate={onUpdate} />
        </Box>
      </Layouts.Content>
    </Layouts.Root>
  );
};

export default EnvironmentsPage;
