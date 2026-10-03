// JdmEditorField (design §4.1) — the Strapi custom-field Input that wraps
// @gorules/jdm-editor's <JdmConfigProvider>/<DecisionGraph> for authoring a ZEN
// decision graph ({nodes, edges}). Registered with base type:'json' (finding 13)
// so the graph persists into the Jdm.doc JSON column with no double-encode.
//
// Contract:
//   * value/onChange ride through Strapi: onChange({target:{name,value,type:'json'}}).
//   * onChange is DEBOUNCED so a drag/keystroke storm does not thrash the form.
//   * if the stored value fails to parse, we render a raw-JSON fallback editor +
//     an error, never a blank/broken graph (design §4). The pure parse decision
//     lives in ../../lib/parseStored (unit-tested by the §6.4 smoke).

import * as React from 'react';
import { DecisionGraph, JdmConfigProvider, type DecisionGraphType } from '@gorules/jdm-editor';
import '@gorules/jdm-editor/dist/style.css';
import { Field, Flex } from '@strapi/design-system';

import { parseStoredJson, safeStringify } from '../../lib/parseStored';
import { RawJsonFallback } from '../RawJsonFallback';

const EMPTY_GRAPH: DecisionGraphType = { nodes: [], edges: [] };
const DEBOUNCE_MS = 300;

interface InputProps {
  name: string;
  value?: unknown;
  onChange: (e: { target: { name: string; value: unknown; type: string } }) => void;
  intlLabel?: { defaultMessage?: string };
  hint?: string;
  required?: boolean;
  error?: string;
  disabled?: boolean;
  attribute?: { type?: string };
}

const JdmEditorField = React.forwardRef<HTMLDivElement, InputProps>((props, ref) => {
  const { name, value, onChange, intlLabel, hint, required, error, disabled } = props;

  // Decide parsed-vs-fallback once per incoming value.
  const parsed = React.useMemo(() => parseStoredJson<DecisionGraphType>(value, EMPTY_GRAPH), [value]);

  // Local working copy of the graph while editing (so the editor stays
  // responsive and the debounced writer flushes to Strapi).
  const [graph, setGraph] = React.useState<DecisionGraphType>(parsed.value ?? EMPTY_GRAPH);
  React.useEffect(() => {
    if (parsed.ok && parsed.value) setGraph(parsed.value);
  }, [parsed.ok, parsed.value]);

  const emit = React.useCallback(
    (next: unknown) => {
      onChange({ target: { name, value: next, type: 'json' } });
    },
    [name, onChange]
  );

  const timer = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  const flushDebounced = React.useCallback(
    (next: DecisionGraphType) => {
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => emit(next), DEBOUNCE_MS);
    },
    [emit]
  );
  React.useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  const handleGraphChange = React.useCallback(
    (next: DecisionGraphType) => {
      setGraph(next);
      flushDebounced(next);
    },
    [flushDebounced]
  );

  const label = intlLabel?.defaultMessage ?? name;

  return (
    <Field.Root name={name} hint={hint} error={error ?? (parsed.ok ? undefined : parsed.error)} required={required}>
      <Field.Label>{label}</Field.Label>
      {parsed.ok ? (
        <Flex ref={ref} direction="column" alignItems="stretch" style={{ height: 520, width: '100%' }}>
          <JdmConfigProvider>
            <DecisionGraph value={graph} onChange={handleGraphChange} disabled={disabled} />
          </JdmConfigProvider>
        </Flex>
      ) : (
        <RawJsonFallback
          ref={ref}
          initial={parsed.raw || safeStringify(EMPTY_GRAPH)}
          disabled={disabled}
          onCommit={(parsedValue) => emit(parsedValue)}
        />
      )}
      <Field.Hint />
      <Field.Error />
    </Field.Root>
  );
});

JdmEditorField.displayName = 'JdmEditorField';

export default JdmEditorField;
