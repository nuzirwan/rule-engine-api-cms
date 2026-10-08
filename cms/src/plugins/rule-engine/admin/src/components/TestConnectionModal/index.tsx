// TestConnectionModal — Modal for testing database/service connections (FEAT-003).
// Displays read-only connection settings, provides password input fields based on
// connector schema (single for postgres, multiple for kafka/rabbitmq, dynamic for REST),
// and calls the test endpoint. Passwords are never stored — only used for the test.

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
import { Plus, Trash } from '@strapi/icons';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import type {
  ConnectorSchemaResponse,
  SecretField,
  TestConnectionResponse,
} from '../../../../../../../types/engine';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface TestConnectionModalProps {
  /** Whether the modal is open. */
  isOpen: boolean;
  /** Callback to close the modal. */
  onClose: () => void;
  /** Connection type (e.g., 'postgres', 'mysql', 'valkey', 'rest', 'kafka'). */
  connectionType: string;
  /** Connection settings (host, port, database, user, etc. based on type). */
  settings: Record<string, unknown>;
  /** Connection key/name for display. */
  connectionKey?: string;
  /** Existing secretRefs for the connection (keys used for REST dynamic secrets). */
  secretRefs?: Record<string, string> | null;
}

interface TestResult {
  success: boolean;
  message?: string;
  error?: string;
}

interface DynamicSecret {
  key: string;
  value: string;
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
    { key: 'vhost', label: 'VHost' },
    { key: 'saslMechanism', label: 'SASL Mechanism' },
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
  secretRefs,
}) => {
  const { get, post } = useFetchClient();

  // Schema state
  const [schema, setSchema] = React.useState<SecretField[] | null>(null);
  const [schemaLoading, setSchemaLoading] = React.useState(false);
  const [schemaError, setSchemaError] = React.useState<string | null>(null);

  // Fixed secrets state (for connectors with defined schema)
  const [secrets, setSecrets] = React.useState<Record<string, string>>({});

  // Dynamic secrets state (for REST)
  const [dynamicSecrets, setDynamicSecrets] = React.useState<DynamicSecret[]>([]);

  // Test state
  const [isLoading, setIsLoading] = React.useState(false);
  const [result, setResult] = React.useState<TestResult | null>(null);

  // Fetch schema when modal opens
  React.useEffect(() => {
    if (!isOpen) return;

    let cancelled = false;

    const fetchSchema = async () => {
      setSchemaLoading(true);
      setSchemaError(null);
      try {
        const response = await get<ConnectorSchemaResponse>(
          '/rule-engine/connectors/schema'
        );
        if (cancelled) return;

        const connectorSchema = response.data?.connectors?.[connectionType];
        if (connectorSchema) {
          setSchema(connectorSchema.secrets);
        } else {
          // Unknown connector - default to single password
          setSchema([{ name: 'password', required: false, label: 'Password' }]);
        }
      } catch (err) {
        if (cancelled) return;
        setSchemaError((err as Error).message || 'Failed to load connector schema');
        // Fallback to single password
        setSchema([{ name: 'password', required: false, label: 'Password' }]);
      } finally {
        if (!cancelled) {
          setSchemaLoading(false);
        }
      }
    };

    fetchSchema();
    return () => {
      cancelled = true;
    };
  }, [isOpen, connectionType, get]);

  // Reset state when modal opens/closes
  React.useEffect(() => {
    if (isOpen) {
      setSecrets({});
      setResult(null);
      setIsLoading(false);
      // Initialize dynamic secrets from secretRefs keys (for REST)
      if (secretRefs && Object.keys(secretRefs).length > 0) {
        setDynamicSecrets(
          Object.keys(secretRefs).map((key) => ({ key, value: '' }))
        );
      } else {
        setDynamicSecrets([]);
      }
    }
  }, [isOpen, secretRefs]);

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

  const handleFixedSecretChange = (name: string, value: string) => {
    setSecrets((prev) => ({ ...prev, [name]: value }));
  };

  const handleDynamicKeyChange = (index: number, key: string) => {
    setDynamicSecrets((prev) => {
      const updated = [...prev];
      updated[index] = { ...updated[index], key };
      return updated;
    });
  };

  const handleDynamicValueChange = (index: number, value: string) => {
    setDynamicSecrets((prev) => {
      const updated = [...prev];
      updated[index] = { ...updated[index], value };
      return updated;
    });
  };

  const handleAddDynamicSecret = () => {
    setDynamicSecrets((prev) => [...prev, { key: '', value: '' }]);
  };

  const handleRemoveDynamicSecret = (index: number) => {
    setDynamicSecrets((prev) => prev.filter((_, i) => i !== index));
  };

  const handleTest = async () => {
    setIsLoading(true);
    setResult(null);

    try {
      // Build secrets map
      let secretsMap: Record<string, string>;
      if (schema === null) {
        // Dynamic (REST) - use dynamic secrets
        secretsMap = {};
        for (const ds of dynamicSecrets) {
          if (ds.key && ds.value) {
            secretsMap[ds.key] = ds.value;
          }
        }
      } else {
        // Fixed schema - use fixed secrets
        secretsMap = secrets;
      }

      const response = await post<TestConnectionResponse>(
        '/rule-engine/connections/test',
        {
          type: connectionType,
          settings: flatSettings,
          secrets: secretsMap,
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

  // Render secret inputs based on schema
  const renderSecretInputs = () => {
    if (schemaLoading) {
      return (
        <Flex justifyContent="center" padding={4}>
          <Loader small>Loading schema...</Loader>
        </Flex>
      );
    }

    if (schemaError) {
      return (
        <Box padding={3} background="warning100" hasRadius marginBottom={2}>
          <Typography variant="pi" textColor="warning600">
            {schemaError}
          </Typography>
        </Box>
      );
    }

    // Dynamic schema (REST) - show key-value pairs
    if (schema === null) {
      return (
        <Box>
          <Typography variant="omega" fontWeight="bold" marginBottom={2}>
            Secret Headers
          </Typography>
          <Field.Hint>
            Enter the secret values for each header. These are used only for this test.
          </Field.Hint>
          <Box marginTop={2}>
            {dynamicSecrets.length === 0 ? (
              <Box padding={3} background="neutral100" hasRadius>
                <Typography variant="pi" textColor="neutral600">
                  No secret headers configured. Click "Add Header" to add one.
                </Typography>
              </Box>
            ) : (
              <Flex direction="column" gap={2}>
                {dynamicSecrets.map((ds, index) => (
                  <Flex key={index} gap={2} alignItems="flex-end">
                    <Box flex="1">
                      <Field.Root>
                        <Field.Label>Header Name</Field.Label>
                        <TextInput
                          placeholder="e.g., Authorization"
                          value={ds.key}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                            handleDynamicKeyChange(index, e.target.value)
                          }
                          disabled={isLoading}
                        />
                      </Field.Root>
                    </Box>
                    <Box flex="1">
                      <Field.Root>
                        <Field.Label>Value</Field.Label>
                        <TextInput
                          type="password"
                          placeholder="Enter secret value"
                          value={ds.value}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                            handleDynamicValueChange(index, e.target.value)
                          }
                          disabled={isLoading}
                        />
                      </Field.Root>
                    </Box>
                    <Button
                      variant="ghost"
                      onClick={() => handleRemoveDynamicSecret(index)}
                      disabled={isLoading}
                      startIcon={<Trash />}
                      size="S"
                    >
                      Remove
                    </Button>
                  </Flex>
                ))}
              </Flex>
            )}
            <Box marginTop={2}>
              <Button
                variant="secondary"
                size="S"
                startIcon={<Plus />}
                onClick={handleAddDynamicSecret}
                disabled={isLoading}
              >
                Add Header
              </Button>
            </Box>
          </Box>
        </Box>
      );
    }

    // Fixed schema - show labeled inputs
    if (schema.length === 0) {
      return (
        <Box padding={3} background="neutral100" hasRadius>
          <Typography variant="pi" textColor="neutral600">
            This connector does not require any secrets for testing.
          </Typography>
        </Box>
      );
    }

    return (
      <Flex direction="column" gap={4}>
        {schema.map((field) => (
          <Field.Root key={field.name}>
            <Field.Label>
              {field.label}
              {field.required && <span style={{ color: 'red' }}> *</span>}
            </Field.Label>
            <TextInput
              type="password"
              placeholder={`Enter ${field.label.toLowerCase()}`}
              value={secrets[field.name] || ''}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                handleFixedSecretChange(field.name, e.target.value)
              }
              disabled={isLoading}
            />
            <Field.Hint>
              The {field.label.toLowerCase()} is used only for this test and is not stored.
            </Field.Hint>
          </Field.Root>
        ))}
      </Flex>
    );
  };

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

            {/* Secret Inputs (based on schema) */}
            {renderSecretInputs()}

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
            disabled={isLoading || schemaLoading}
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
