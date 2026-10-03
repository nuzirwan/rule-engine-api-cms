// RawJsonFallback (design §4) — the shared degrade-gracefully editor both custom
// fields fall back to when a stored value fails to parse as a JSON object. It
// shows the raw text in a textarea so a hand-edited or engine-returned value can
// always be inspected and repaired, and commits back only when the text parses
// cleanly (so a bad value never silently overwrites with junk).

import * as React from 'react';
import { Box, Field, Flex, Textarea, Typography } from '@strapi/design-system';

interface RawJsonFallbackProps {
  initial: string;
  disabled?: boolean;
  /** Called with the PARSED value once the raw text is valid JSON. */
  onCommit: (value: unknown) => void;
}

export const RawJsonFallback = React.forwardRef<HTMLDivElement, RawJsonFallbackProps>(
  ({ initial, disabled, onCommit }, ref) => {
    const [text, setText] = React.useState<string>(initial);
    const [parseError, setParseError] = React.useState<string | null>(null);

    React.useEffect(() => setText(initial), [initial]);

    const handleChange = React.useCallback(
      (e: React.ChangeEvent<HTMLTextAreaElement>) => {
        const next = e.currentTarget.value;
        setText(next);
        if (next.trim() === '') {
          setParseError(null);
          return;
        }
        try {
          const value = JSON.parse(next);
          setParseError(null);
          onCommit(value);
        } catch (err) {
          setParseError((err as Error).message);
        }
      },
      [onCommit]
    );

    return (
      <Box ref={ref} padding={2} background="neutral100" hasRadius>
        <Flex direction="column" alignItems="stretch" gap={2}>
          <Typography variant="pi" textColor="danger600">
            The stored value could not be parsed as a JSON object. Edit the raw JSON below; it is
            saved once it parses.
          </Typography>
          <Field.Root error={parseError ?? undefined}>
            <Textarea
              name="raw-json-fallback"
              value={text}
              disabled={disabled}
              onChange={handleChange}
              rows={16}
            />
            <Field.Error />
          </Field.Root>
        </Flex>
      </Box>
    );
  }
);

RawJsonFallback.displayName = 'RawJsonFallback';

export default RawJsonFallback;
