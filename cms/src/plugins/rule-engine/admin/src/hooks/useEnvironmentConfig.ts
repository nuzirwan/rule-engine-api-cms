// useEnvironmentConfig — helper hook that returns the resolved AdminClientConfig
// for the selected environment. Used by validation panel and other components
// that need to call environment-specific engine APIs.
//
// FEAT-003: Environment-aware validation and sync.

import * as React from 'react';
import { useFetchClient } from '@strapi/admin/strapi-admin';

import { useEnvironment, type Environment } from '../contexts/EnvironmentContext';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Resolved config for calling engine APIs against a specific environment. */
export interface EnvironmentConfig {
  /** Environment documentId (null if using default/global config). */
  environmentId: string | null;
  /** Environment name (null if using default). */
  environmentName: string | null;
  /** Whether an environment is selected. */
  hasEnvironment: boolean;
}

/** State shape for useEnvironmentConfig. */
export interface EnvironmentConfigState {
  /** The resolved environment config. */
  config: EnvironmentConfig;
  /** The selected environment (or null for default). */
  selectedEnv: Environment | null;
  /** Whether environments are loading. */
  isLoading: boolean;
  /** Error message if environment fetch failed. */
  error: string | null;
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

/**
 * Hook that returns the resolved environment config for the selected environment.
 * 
 * Usage:
 * ```tsx
 * const { config, selectedEnv, isLoading, error } = useEnvironmentConfig();
 * 
 * // Use config.environmentId to pass to API calls that support env filtering
 * const url = config.environmentId
 *   ? `/api/validate?env=${config.environmentId}`
 *   : '/api/validate';
 * ```
 */
export function useEnvironmentConfig(): EnvironmentConfigState {
  const { selectedEnv, isLoading, error } = useEnvironment();

  const config = React.useMemo<EnvironmentConfig>(() => ({
    environmentId: selectedEnv?.documentId ?? null,
    environmentName: selectedEnv?.name ?? null,
    hasEnvironment: selectedEnv !== null,
  }), [selectedEnv]);

  return {
    config,
    selectedEnv,
    isLoading,
    error,
  };
}

// ---------------------------------------------------------------------------
// Utility functions
// ---------------------------------------------------------------------------

/**
 * Build a URL with optional environment query parameter.
 * 
 * @param baseUrl - The base URL path
 * @param envDocumentId - The environment documentId (null for no filter)
 * @returns URL with ?env= query param if envDocumentId is provided
 */
export function buildEnvUrl(baseUrl: string, envDocumentId: string | null): string {
  if (!envDocumentId) {
    return baseUrl;
  }
  const separator = baseUrl.includes('?') ? '&' : '?';
  return `${baseUrl}${separator}env=${encodeURIComponent(envDocumentId)}`;
}

export default useEnvironmentConfig;
