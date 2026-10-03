import { defineConfig } from 'vitest/config';

// Scoped to the rule-engine plugin's server-side test suite. These are pure
// unit/integration tests (publish-transform, validation/publish-sequence,
// reconcile) with the admin HTTP API mocked via nock — no Strapi runtime, no
// real engine, no DB.
export default defineConfig({
  test: {
    globals: true,
    environment: 'node',
    include: [
      'src/plugins/rule-engine/server/tests/**/*.{test,spec}.ts',
      // The pure flow-canvas serializer test lives alongside serialize.ts on
      // the admin side (§6.2); include it so one `npm run test` run covers both.
      'src/plugins/rule-engine/admin/src/**/*.{test,spec}.ts',
    ],
  },
});
