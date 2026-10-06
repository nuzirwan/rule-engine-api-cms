// EnvironmentContext — provides environment selection state across the admin UI.
// Fetches available environments from the server on mount and persists the
// selected environment name to localStorage for restoration across page loads.

import * as React from 'react';
import { useFetchClient } from '@strapi/admin/strapi-admin';

import type { Environment } from './types';

export type { Environment };

// ---------------------------------------------------------------------------
// Context value interface
// ---------------------------------------------------------------------------

export interface EnvironmentContextValue {
  /** List of available environments. */
  environments: Environment[];
  /** Currently selected environment (null if none selected or loading). */
  selectedEnv: Environment | null;
  /** Select an environment by name. Persists to localStorage. */
  selectEnv: (name: string) => void;
  /** True while fetching environments from the server. */
  isLoading: boolean;
  /** Error message if fetch failed (null otherwise). */
  error: string | null;
  /** Refresh environments from the server. */
  refresh: () => void;
}

const STORAGE_KEY = 'rule-engine-selected-env';

// ---------------------------------------------------------------------------
// Context
// ---------------------------------------------------------------------------

const EnvironmentContext = React.createContext<EnvironmentContextValue | null>(null);

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

export interface EnvironmentProviderProps {
  children: React.ReactNode;
}

export const EnvironmentProvider: React.FC<EnvironmentProviderProps> = ({ children }) => {
  const { get } = useFetchClient();
  const [environments, setEnvironments] = React.useState<Environment[]>([]);
  const [selectedEnv, setSelectedEnv] = React.useState<Environment | null>(null);
  const [isLoading, setIsLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);

  const fetchEnvironments = React.useCallback(async () => {
    setIsLoading(true);
    setError(null);

    try {
      const { data } = await get<{ environments: Environment[] }>('/rule-engine/environments');
      const envList = data.environments ?? [];
      setEnvironments(envList);

      // Restore selected environment from localStorage
      const storedName = localStorage.getItem(STORAGE_KEY);
      if (storedName) {
        const restored = envList.find((e) => e.name === storedName);
        if (restored) {
          setSelectedEnv(restored);
        } else {
          // Stored name no longer exists, clear it
          localStorage.removeItem(STORAGE_KEY);
        }
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Failed to load environments';
      setError(message);
    } finally {
      setIsLoading(false);
    }
  }, [get]);

  React.useEffect(() => {
    fetchEnvironments();
  }, [fetchEnvironments]);

  const selectEnv = React.useCallback(
    (name: string) => {
      const env = environments.find((e) => e.name === name);
      if (env) {
        setSelectedEnv(env);
        localStorage.setItem(STORAGE_KEY, name);
      } else {
        setSelectedEnv(null);
        localStorage.removeItem(STORAGE_KEY);
      }
    },
    [environments]
  );

  const value: EnvironmentContextValue = React.useMemo(
    () => ({
      environments,
      selectedEnv,
      selectEnv,
      isLoading,
      error,
      refresh: fetchEnvironments,
    }),
    [environments, selectedEnv, selectEnv, isLoading, error, fetchEnvironments]
  );

  return <EnvironmentContext.Provider value={value}>{children}</EnvironmentContext.Provider>;
};

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

/**
 * Hook to access environment context. Must be used within an EnvironmentProvider.
 * @throws Error if used outside of EnvironmentProvider.
 */
export function useEnvironment(): EnvironmentContextValue {
  const context = React.useContext(EnvironmentContext);
  if (!context) {
    throw new Error('useEnvironment must be used within an EnvironmentProvider');
  }
  return context;
}
