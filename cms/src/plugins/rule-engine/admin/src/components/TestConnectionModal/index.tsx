// TestConnectionModal — Modal for testing database/service connections (FEAT-003).
// Displays read-only connection settings, provides a password input field, and
// calls the test endpoint. The password is never stored — only used for the test.

import * as React from 'react';
import {
  Dialog,
  Button,
  TextInput,
  Typography,
  Loader,
  Badge,
  Box,
  Flex,
  Field,
} from '@strapi/design-system';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import type { TestConnectionResponse } from '../../../../../../../types/engine';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface TestConnectionModalProps {
  /** Whether the modal is open. */
  isOpen: boolean;
  /** Callback to close the modal. */
  onClose: () => void;
  /** Connection type (e.g., 'postgres', 'mysql', 'valkey'). */
  connectionType: string;
  /** Connection settings (host, port, database, user, etc. based on type). */
  settings: Record<string, unknown>;
  /** Connection key/name for display. */
  connectionKey?: string;
}

interface TestResult {
  success: boolean;
  message?: string;
  error?: string;
}

// ---------------------------------------------------------------------------
// Helper: Format settings for display
// ---------------------------------------------------------------------------

/**
 * Extract display-friendly settings from the connection settings object.
 * Handles both flat objects and Strapi dynamic zone arrays.
 */
function extractDisplaySettings(
  settings: Record<string, unknown>
): Array<{ label: string; value: string }> {
  const result: Array<{ label: string; value: string }> = [];

  // If settings is an array (dynamic zone), extract from first item
  const settingsObj = Array.isArray(settings)
    ? (settings[0] as Record<string, unknown> | undefined) ?? {}
    : settings;

  // Common fields to display (order matters)
  const displayFields = [
    { key: 'host', label: 'Host' },
    { key: 'port', label: 'Port' },
    { key: 'database', label: 'Database' },
    { key: 'user', label: 'User' },
    { key: 'username', label: 'User' },
    { key: 'baseUrl', label: 'Base URL' },
    { key: 'url', label: 'URL' },
    { key: 'brokers', label: 'Brokers' },
    { key: 'addresses', label: 'Addresses' },
    { key: 'sslMode', label: 'SSL Mode' },
    { key: 'tls', label: 'TLS' },
  ];

  for (const { key, label } of displayFields) {
    if (key in settingsObj && settingsObj[key] != null) {
      const value = settingsObj[key];
      // Format arrays nicely
      const displayValue = Array.isArray(value) ? value.join(', ') : String(value);
      result.push({ label, value: displayValue });
    }
  }

  return result;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export const TestConnectionModal: React.FC<TestConnectionModalProps> = ({
  isOpen,
  onClose,
  connectionType,
  settings,
  connectionKey,
}) => {
  const { post } = useFetchClient();

  const [password, setPassword] = React.useState('');
  const [isLoading, setIsLoading] = React.useState(false);
  const [result, setResult] = React.useState<TestResult | null>(null);

  // Reset state when modal opens/closes
  React.useEffect(() => {
    if (isOpen) {
      setPassword('');
      setResult(null);
      setIsLoading(false);
    }
  }, [isOpen]);

  // Flatten settings for the API call (handle dynamic zone)
  const flatSettings = React.useMemo(() => {
    if (Array.isArray(settings)) {
      const first = settings[0] as Record<string, unknown> | undefined;
      if (first) {
        // Remove __component metadata
        const { __component, id, ...rest } = first as Record<string, unknown>;
        return rest;
      }
      return {};
    }
    return settings;
  }, [settings]);

  const handleTest = async () => {
    setIsLoading(true);
    setResult(null);

    try {
      const response = await post<TestConnectionResponse>(
        '/rule-engine/connections/test',
        {
          type: connectionType,
          settings: flatSettings,
          secret: password,
        }
      );

      setResult({
        success: response.data.success,
        message: response.data.message,
        error: response.data.error,
      });
    } catch (err: unknown) {
      const body = (err as { response?: { data?: { error?: string } } })?.response?.data;
      const message = body?.error ?? (err instanceof Error ? err.message : 'Test failed');
      setResult({
        success: false,
        error: message,
      });
    } finally {
      setIsLoading(false);
    }
  };

  const displaySettings = extractDisplaySettings(settings);

  return (
    <Dialog.Root open={isOpen} onOpenChange={(open) => !open && onClose()}>
      <Dialog.Content>
        <Dialog.Header>
          Test Connection{connectionKey ? `: ${connectionKey}` : ''}
        </Dialog.Header>
        <Dialog.Body>
          <Flex direction="column" gap={4}>
            {/* Connection Type */}
            <Box>
              <Typography variant="omega" fontWeight="bold">
                Type
              </Typography>
              <Badge variant="secondary" marginTop={1}>
                {connectionType}
              </Badge>
            </Box>

            {/* Connection Settings (read-only) */}
            {displaySettings.length > 0 && (
              <Box>
                <Typography variant="omega" fontWeight="bold" marginBottom={2}>
                  Settings
                </Typography>
                <Box
                  padding={3}
                  background="neutral100"
                  hasRadius
                  borderColor="neutral200"
                >
                  <Flex direction="column" gap={2}>
                    {displaySettings.map(({ label, value }) => (
                      <Flex key={label} justifyContent="space-between">
                        <Typography variant="pi" textColor="neutral600">
                          {label}:
                        </Typography>
                        <Typography variant="pi" fontWeight="semiBold">
                          {value}
                        </Typography>
                      </Flex>
                    ))}
                  </Flex>
                </Box>
              </Box>
            )}

            {/* Password Input */}
            <Field.Root>
              <Field.Label>Password / Secret</Field.Label>
              <TextInput
                type="password"
                placeholder="Enter password to test connection"
                value={password}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                  setPassword(e.target.value)
                }
                disabled={isLoading}
              />
              <Field.Hint>
                The password is used only for this test and is not stored.
              </Field.Hint>
            </Field.Root>

            {/* Result Display */}
            {result && (
              <Box
                padding={3}
                background={result.success ? 'success100' : 'danger100'}
                hasRadius
              >
                <Flex direction="row" alignItems="center" gap={2}>
                  <Badge variant={result.success ? 'success' : 'danger'}>
                    {result.success ? 'Success' : 'Failed'}
                  </Badge>
                  <Typography
                    variant="omega"
                    textColor={result.success ? 'success600' : 'danger600'}
                  >
                    {result.success
                      ? result.message ?? 'Connection successful'
                      : result.error ?? 'Connection failed'}
                  </Typography>
                </Flex>
              </Box>
            )}
          </Flex>
        </Dialog.Body>
        <Dialog.Footer>
          <Dialog.Cancel>
            <Button variant="tertiary" onClick={onClose}>
              Close
            </Button>
          </Dialog.Cancel>
          <Button
            variant="default"
            onClick={handleTest}
            disabled={isLoading}
            startIcon={isLoading ? <Loader small /> : undefined}
          >
            {isLoading ? 'Testing...' : 'Test Connection'}
          </Button>
        </Dialog.Footer>
      </Dialog.Content>
    </Dialog.Root>
  );
};

export default TestConnectionModal;
