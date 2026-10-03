// ValidationPanel (design §5.3/§5.4) — renders the last /admin/flows/validate
// result ({structural[], fixtures[]}) inline on the Flow edit view: the
// structural error codes and the per-fixture pass/fail diffs. This is a
// read-only surface over the Flow entry's `lastValidation` json field; it does
// not call the engine itself (the publish sequence owns that).

import * as React from 'react';
import { Box, Flex, Typography } from '@strapi/design-system';

import { safeStringify } from '../../lib/parseStored';

/** One fixture result as the engine returns it (slice-d §5 / design §5.1). */
export interface FixtureResult {
  name?: string;
  passed: boolean;
  diff?: unknown;
}

/** The lastValidation shape stored on the Flow entry. */
export interface LastValidation {
  ok?: boolean;
  structural?: unknown[];
  fixtures?: FixtureResult[];
}

interface ValidationPanelProps {
  /** The Flow entry's `lastValidation` json value (may be null/undefined). */
  value?: LastValidation | null;
}

/** Render a structural error entry as a readable code/line. */
function structuralLine(entry: unknown): string {
  if (typeof entry === 'string') return entry;
  if (entry && typeof entry === 'object') {
    const e = entry as Record<string, unknown>;
    const code = e.code ?? e.rule ?? e.type;
    const msg = e.message ?? e.detail ?? e.path;
    if (code || msg) return [code, msg].filter(Boolean).join(': ');
  }
  return safeStringify(entry);
}

export const ValidationPanel: React.FC<ValidationPanelProps> = ({ value }) => {
  if (!value) {
    return (
      <Box padding={3} background="neutral100" hasRadius>
        <Typography variant="pi" textColor="neutral600">
          No validation has been run for this flow yet.
        </Typography>
      </Box>
    );
  }

  const structural = Array.isArray(value.structural) ? value.structural : [];
  const fixtures = Array.isArray(value.fixtures) ? value.fixtures : [];
  const ok = value.ok ?? (structural.length === 0 && fixtures.every((f) => f.passed));

  return (
    <Box padding={3} background="neutral100" hasRadius>
      <Flex direction="column" alignItems="stretch" gap={3}>
        <Typography variant="delta" textColor={ok ? 'success600' : 'danger600'}>
          {ok ? 'Validation passed' : 'Validation blocked publish'}
        </Typography>

        <Flex direction="column" alignItems="stretch" gap={1}>
          <Typography variant="sigma">Structural ({structural.length})</Typography>
          {structural.length === 0 ? (
            <Typography variant="pi" textColor="neutral600">
              No structural errors.
            </Typography>
          ) : (
            structural.map((entry, i) => (
              <Typography key={i} variant="pi" textColor="danger600">
                {structuralLine(entry)}
              </Typography>
            ))
          )}
        </Flex>

        <Flex direction="column" alignItems="stretch" gap={1}>
          <Typography variant="sigma">Fixtures ({fixtures.length})</Typography>
          {fixtures.length === 0 ? (
            <Typography variant="pi" textColor="neutral600">
              No fixtures were evaluated.
            </Typography>
          ) : (
            fixtures.map((f, i) => (
              <Box key={i} padding={2} background="neutral0" hasRadius>
                <Flex direction="column" alignItems="stretch" gap={1}>
                  <Typography variant="omega" textColor={f.passed ? 'success600' : 'danger600'}>
                    {f.passed ? 'PASS' : 'FAIL'} · {f.name ?? `fixture ${i + 1}`}
                  </Typography>
                  {!f.passed && f.diff !== undefined ? (
                    <Box
                      as="pre"
                      padding={2}
                      background="neutral150"
                      hasRadius
                      style={{ margin: 0, overflowX: 'auto' }}
                    >
                      <Typography variant="pi" fontFamily="monospace">
                        {safeStringify(f.diff)}
                      </Typography>
                    </Box>
                  ) : null}
                </Flex>
              </Box>
            ))
          )}
        </Flex>
      </Flex>
    </Box>
  );
};

export default ValidationPanel;
