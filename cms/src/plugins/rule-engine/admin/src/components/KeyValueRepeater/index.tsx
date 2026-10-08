// KeyValueRepeater — Component for REST's dynamic secret refs.
// Renders key-value pairs with add/remove buttons.
// Used when the connector has a null schema (dynamic secrets like REST headers).

import * as React from 'react';
import {
  Box,
  Button,
  TextInput,
  Typography,
  Flex,
  IconButton,
  Field,
} from '@strapi/design-system';
import { Plus, Trash } from '@strapi/icons';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface KeyValuePair {
  key: string;
  value: string;
}

export interface KeyValueRepeaterProps {
  /** Current key-value pairs. */
  value: KeyValuePair[];
  /** Callback when pairs change. */
  onChange: (pairs: KeyValuePair[]) => void;
  /** Label for the key input (default: "Key"). */
  keyLabel?: string;
  /** Label for the value input (default: "Value"). */
  valueLabel?: string;
  /** Placeholder for the key input. */
  keyPlaceholder?: string;
  /** Placeholder for the value input. */
  valuePlaceholder?: string;
  /** Whether value inputs should be password fields (default: true). */
  isSecret?: boolean;
  /** Whether the component is disabled. */
  disabled?: boolean;
  /** Hint text to display below the component. */
  hint?: string;
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export const KeyValueRepeater: React.FC<KeyValueRepeaterProps> = ({
  value,
  onChange,
  keyLabel = 'Key',
  valueLabel = 'Value',
  keyPlaceholder = 'Enter key (e.g., Authorization)',
  valuePlaceholder = 'Enter value',
  isSecret = true,
  disabled = false,
  hint,
}) => {
  const handleKeyChange = (index: number, newKey: string) => {
    const updated = [...value];
    updated[index] = { ...updated[index], key: newKey };
    onChange(updated);
  };

  const handleValueChange = (index: number, newValue: string) => {
    const updated = [...value];
    updated[index] = { ...updated[index], value: newValue };
    onChange(updated);
  };

  const handleAdd = () => {
    onChange([...value, { key: '', value: '' }]);
  };

  const handleRemove = (index: number) => {
    const updated = value.filter((_, i) => i !== index);
    onChange(updated);
  };

  return (
    <Box>
      {value.length === 0 ? (
        <Box padding={3} background="neutral100" hasRadius>
          <Typography variant="pi" textColor="neutral600">
            No secret headers configured. Click "Add" to add one.
          </Typography>
        </Box>
      ) : (
        <Flex direction="column" gap={3}>
          {/* Header row */}
          <Flex gap={2}>
            <Box flex="1">
              <Typography variant="pi" fontWeight="bold" textColor="neutral600">
                {keyLabel}
              </Typography>
            </Box>
            <Box flex="1">
              <Typography variant="pi" fontWeight="bold" textColor="neutral600">
                {valueLabel}
              </Typography>
            </Box>
            <Box style={{ width: '40px' }} />
          </Flex>

          {/* Rows */}
          {value.map((pair, index) => (
            <Flex key={index} gap={2} alignItems="flex-start">
              <Box flex="1">
                <TextInput
                  aria-label={`${keyLabel} ${index + 1}`}
                  placeholder={keyPlaceholder}
                  value={pair.key}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                    handleKeyChange(index, e.target.value)
                  }
                  disabled={disabled}
                />
              </Box>
              <Box flex="1">
                <TextInput
                  aria-label={`${valueLabel} ${index + 1}`}
                  type={isSecret ? 'password' : 'text'}
                  placeholder={valuePlaceholder}
                  value={pair.value}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) =>
                    handleValueChange(index, e.target.value)
                  }
                  disabled={disabled}
                />
              </Box>
              <IconButton
                label="Remove"
                variant="ghost"
                onClick={() => handleRemove(index)}
                disabled={disabled}
              >
                <Trash />
              </IconButton>
            </Flex>
          ))}
        </Flex>
      )}

      {/* Add button */}
      <Box marginTop={3}>
        <Button
          variant="secondary"
          startIcon={<Plus />}
          onClick={handleAdd}
          disabled={disabled}
          size="S"
        >
          Add Secret Header
        </Button>
      </Box>

      {/* Hint */}
      {hint && (
        <Box marginTop={2}>
          <Field.Hint>{hint}</Field.Hint>
        </Box>
      )}
    </Box>
  );
};

export default KeyValueRepeater;
