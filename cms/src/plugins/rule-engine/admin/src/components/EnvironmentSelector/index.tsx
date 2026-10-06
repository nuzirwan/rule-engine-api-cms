// EnvironmentSelector — dropdown component for selecting the active environment.
// Uses @strapi/design-system SingleSelect with color-coded badges for env type.
// Consumes useEnvironment hook for state management.
//
// Environment badge colors:
//   * success (green): 'production', 'prod', or 'default' patterns
//   * alternative (blue): 'staging', 'stage', 'stg' patterns
//   * warning (orange): 'dev', 'development', 'local' patterns

import * as React from 'react';
import {
  SingleSelect,
  SingleSelectOption,
  Flex,
  Badge,
  Typography,
  Loader,
} from '@strapi/design-system';

import { useEnvironment, type Environment } from '../../contexts/EnvironmentContext';

// ---------------------------------------------------------------------------
// Badge color logic
// ---------------------------------------------------------------------------

type BadgeVariant = 'success' | 'alternative' | 'warning' | 'neutral';

/** Determine badge variant based on environment name pattern. */
function getBadgeVariant(name: string): BadgeVariant {
  const lower = name.toLowerCase();

  // Production patterns
  if (lower.includes('prod') || lower === 'default' || lower === 'live') {
    return 'success';
  }

  // Staging patterns
  if (lower.includes('stag') || lower.includes('stg') || lower === 'qa' || lower === 'uat') {
    return 'alternative';
  }

  // Development patterns
  if (lower.includes('dev') || lower === 'local' || lower === 'test') {
    return 'warning';
  }

  return 'neutral';
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

interface EnvOptionLabelProps {
  env: Environment;
}

function EnvOptionLabel({ env }: EnvOptionLabelProps): React.JSX.Element {
  return (
    <Flex direction="row" alignItems="center" gap={2}>
      <Typography variant="omega">{env.name}</Typography>
      <Badge variant={getBadgeVariant(env.name)} size="S">
        {env.name}
      </Badge>
    </Flex>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface EnvironmentSelectorProps {
  /** Placeholder text when no environment is selected. */
  placeholder?: string;
  /** Whether to show the "All Environments" option (default: true). */
  showAllOption?: boolean;
  /** Additional CSS class for the container. */
  className?: string;
}

export const EnvironmentSelector: React.FC<EnvironmentSelectorProps> = ({
  placeholder = 'Select environment',
  showAllOption = true,
  className,
}) => {
  const { environments, selectedEnv, selectEnv, isLoading, error } = useEnvironment();

  // Handle selection change
  const handleChange = React.useCallback(
    (value: string | number) => {
      const stringValue = String(value);
      if (stringValue === '__all__') {
        // Clear selection by selecting a non-existent name
        selectEnv('');
      } else {
        selectEnv(stringValue);
      }
    },
    [selectEnv]
  );

  // Loading state
  if (isLoading) {
    return (
      <Flex direction="row" alignItems="center" gap={2} className={className}>
        <Loader small />
        <Typography variant="pi" textColor="neutral600">
          Loading environments...
        </Typography>
      </Flex>
    );
  }

  // Error state
  if (error) {
    return (
      <Typography variant="pi" textColor="danger600" className={className}>
        Failed to load environments
      </Typography>
    );
  }

  // No environments available
  if (environments.length === 0) {
    return (
      <Typography variant="pi" textColor="neutral600" className={className}>
        No environments configured
      </Typography>
    );
  }

  const currentValue = selectedEnv?.name ?? (showAllOption ? '__all__' : '');

  return (
    <SingleSelect
      value={currentValue}
      onChange={handleChange}
      placeholder={placeholder}
      className={className}
      size="S"
    >
      {showAllOption && (
        <SingleSelectOption value="__all__">
          <Typography variant="omega">All Environments</Typography>
        </SingleSelectOption>
      )}
      {environments.map((env) => (
        <SingleSelectOption key={env.documentId} value={env.name}>
          <EnvOptionLabel env={env} />
        </SingleSelectOption>
      ))}
    </SingleSelect>
  );
};

export default EnvironmentSelector;
