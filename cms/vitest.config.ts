import { defineConfig } from 'vitest/config';

// Scoped to the rule-engine plugin's server-side test suite. These are pure
// unit/integration tests (publish-transform, validation/publish-sequence,
// reconcile) with the admin HTTP API mocked via nock — no Strapi runtime, no
// real engine, no DB.
export default defineConfig({
  test: {
    globals: true,
    environment: 'node',
    include: ['src/plugins/rule-engine/server/tests/**/*.{test,spec}.ts'],
  },
});
