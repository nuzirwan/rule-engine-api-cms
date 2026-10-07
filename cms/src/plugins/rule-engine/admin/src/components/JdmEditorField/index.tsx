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
//
// PERF: Loading state shown while the JDM editor initializes to give immediate
// visual feedback and prevent perceived freezes during component mount.

import * as React from 'react';
import { DecisionGraph, JdmConfigProvider, type DecisionGraphType } from '@gorules/jdm-editor';
import '@gorules/jdm-editor/dist/style.css';
import { Field, Flex } from '@strapi/design-system';

import { parseStoredJson, safeStringify } from '../../lib/parseStored';
import { RawJsonFallback } from '../RawJsonFallback';
import { EditorSkeleton } from '../EditorSkeleton';

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

  // PERF: Loading state while the JDM editor initializes
  const [isReady, setIsReady] = React.useState(false);
  React.useEffect(() => {
    // Defer ready state to next frame to allow React to finish mounting
    const frame = requestAnimationFrame(() => setIsReady(true));
    return () => cancelAnimationFrame(frame);
  }, []);

  // Local working copy of the graph while editing (so the editor stays
  // responsive and the debounced writer flushes to Strapi).
  const [graph, setGraph] = React.useState<DecisionGraphType>(parsed.value ?? EMPTY_GRAPH);
  
  // Sync external value changes into local state. Use stringified value as the
  // dependency because parsed.value may be the same EMPTY_GRAPH reference when
  // value is undefined, which wouldn't trigger the effect when real data arrives.
  const valueKey = React.useMemo(() => safeStringify(value), [value]);
  React.useEffect(() => {
    if (parsed.ok && parsed.value) setGraph(parsed.value);
  }, [valueKey, parsed.ok, parsed.value]);

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

  // Show skeleton while initializing
  if (!isReady) {
    return (
      <Field.Root name={name} hint={hint} error={error} required={required}>
        <Field.Label>{label}</Field.Label>
        <EditorSkeleton height={520} label="Initializing decision editor…" />
      </Field.Root>
    );
  }

  return (
    <Field.Root name={name} hint={hint} error={error ?? (parsed.ok ? undefined : parsed.error)} required={required}>
      <Field.Label>{label}</Field.Label>
      {parsed.ok ? (
        // @gorules/jdm-editor's <DecisionGraph> is a reactflow surface, so it needs
        // its container to resolve a non-zero MEASURED width AND height. A bare
        // width:'100%' inside the content-manager form column can measure 0 while the
        // flex parent is sizing, which leaves the graph unusable. Pin an explicit
        // minWidth and make the direct graph parent fill the box (width/height 100%).
        <Flex
          ref={ref}
          direction="column"
          alignItems="stretch"
          style={{ height: 520, width: '100%', minWidth: 480 }}
        >
          <div style={{ flex: '1 1 0%', minHeight: 0, width: '100%', height: '100%' }}>
            <JdmConfigProvider>
              <DecisionGraph value={graph} onChange={handleGraphChange} disabled={disabled} />
            </JdmConfigProvider>
          </div>
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
