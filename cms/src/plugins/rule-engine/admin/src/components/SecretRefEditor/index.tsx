// SecretRefEditor — Component that displays secret inputs based on connector schema.
// For fixed-schema connectors (postgres, kafka, etc.), shows labeled TextInputs.
// For REST (null schema), shows a KeyValueRepeater for dynamic secret headers.
//
// The component fetches connector schema on mount or when connection type changes.
// This is used in the connection edit form to configure secretRefs.

import * as React from 'react';
import {
  Box,
  TextInput,
  Typography,
  Flex,
  Loader,
  Field,
} from '@strapi/design-system';
import { useFetchClient } from '@strapi/admin/strapi-admin';
import type { ConnectorSchemaResponse, SecretField } from '../../../../../../../types/engine';
import { KeyValueRepeater, type KeyValuePair } from '../KeyValueRepeater';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface SecretRefEditorProps {
  /** Connection type (e.g., 'postgres', 'rest', 'kafka'). */
  connectionType: string;
  /** Current secretRefs value (map of key -> ref string). */
  value: Record<string, string>;
  /** Callback when secretRefs change. */
  onChange: (refs: Record<string, string>) => void;
  /** Whether the editor is disabled. */
  disabled?: boolean;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export const SecretRefEditor: React.FC<SecretRefEditorProps> = ({
  connectionType,
  value,
  onChange,
  disabled = false,
}) => {
  const { get } = useFetchClient();

  const [schema, setSchema] = React.useState<SecretField[] | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);

  // Fetch connector schemas on mount or when connection type changes
  React.useEffect(() => {
    let cancelled = false;

    const fetchSchema = async () => {
      setLoading(true);
      setError(null);
      try {
        const response = await get<ConnectorSchemaResponse>(
          '/rule-engine/connectors/schema'
        );
        if (cancelled) return;

        const connectorSchema = response.data?.connectors?.[connectionType];
        if (connectorSchema) {
          setSchema(connectorSchema.secrets);
        } else {
          // Unknown connector type - treat as dynamic
          setSchema(null);
        }
      } catch (err) {
        if (cancelled) return;
        setError((err as Error).message || 'Failed to load connector schema');
        setSchema(null);
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    };

    fetchSchema();
    return () => {
      cancelled = true;
    };
  }, [connectionType, get]);

  // Handle fixed schema field change
  const handleFieldChange = (name: string, refValue: string) => {
    onChange({ ...value, [name]: refValue });
  };

  // Handle dynamic (REST) key-value change
  const handleDynamicChange = (pairs: KeyValuePair[]) => {
    const newRefs: Record<string, string> = {};
    for (const pair of pairs) {
      if (pair.key) {
        newRefs[pair.key] = pair.value;
      }
    }
    onChange(newRefs);
  };

  // Convert current value to key-value pairs for REST
  const dynamicPairs: KeyValuePair[] = React.useMemo(() => {
    return Object.entries(value).map(([key, val]) => ({ key, value: val }));
  }, [value]);

  if (loading) {
    return (
      <Flex justifyContent="center" padding={4}>
        <Loader small>Loading schema...</Loader>
      </Flex>
    );
  }

  if (error) {
    return (
      <Box padding={3} background="danger100" hasRadius>
        <Typography textColor="danger600">{error}</Typography>
      </Box>
    );
  }

  // Dynamic schema (REST) - show KeyValueRepeater
  if (schema === null) {
    return (
      <Box>
        <Typography variant="omega" fontWeight="bold" marginBottom={2}>
          Secret Headers
        </Typography>
        <KeyValueRepeater
          value={dynamicPairs}
          onChange={handleDynamicChange}
          keyLabel="Header Name"
          valueLabel="Secret Ref"
          keyPlaceholder="e.g., Authorization"
          valuePlaceholder="e.g., env:MY_API_KEY"
          isSecret={false}
          disabled={disabled}
          hint="Secret refs are resolved at runtime. Use format: env:VAR_NAME or vault:path/to/secret"
        />
      </Box>
    );
  }

  // Fixed schema - show labeled inputs
  if (schema.length === 0) {
    return (
      <Box padding={3} background="neutral100" hasRadius>
        <Typography variant="pi" textColor="neutral600">
          This connector type does not require any secrets.
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
            placeholder={`e.g., env:${field.name.toUpperCase()}_SECRET`}
            value={value[field.name] || ''}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
              handleFieldChange(field.name, e.target.value)
            }
            disabled={disabled}
          />
          <Field.Hint>
            Reference format: env:VAR_NAME or vault:path/to/secret
          </Field.Hint>
        </Field.Root>
      ))}
    </Flex>
  );
};

export default SecretRefEditor;
