// rule-engine plugin — admin entry (design §4). Registers BOTH visual editors as
// Strapi 5 custom fields, each with base `type: 'json'` (finding 13), so Flow.tree
// and Jdm.doc persist their serialized value into the content type's JSON column.
//
// The actual editor components (jdm-editor / reactflow) load lazily via the
// descriptors' async `Input` loaders (see customFields.ts), so this entry stays
// light and the admin bundle only pulls an editor when its field renders.
//
// FEAT-002: Adds EnvironmentsPage to plugin settings and wraps the app with
// EnvironmentProvider for global environment selection state. All page imports
// are lazy to avoid loading @strapi/admin hooks during tests.
//
// PERF: EnvironmentProvider is lifted to wrap ALL routes via a layout component,
// eliminating redundant fetchEnvironments() calls on every route navigation.

import { buildCustomFields, PLUGIN_ID } from './customFields';

export default {
  register(app: any) {
    // Register custom fields
    for (const field of buildCustomFields()) {
      app.customFields.register(field);
    }

    // Register plugin menu entry with sub-pages
    app.addMenuLink({
      to: `plugins/${PLUGIN_ID}`,
      icon: () => null, // Strapi will use default plugin icon
      intlLabel: {
        id: `${PLUGIN_ID}.plugin.name`,
        defaultMessage: 'Rule Engine',
      },
      permissions: [],
    });

    // Register plugin routes
    app.registerPlugin({
      id: PLUGIN_ID,
      name: PLUGIN_ID,
    });
  },

  bootstrap(app: any) {
    // Inject EnvironmentProvider at the plugin level by wrapping routes
    // This provides environment context to all plugin pages
  },

  // Plugin routes — these are rendered when navigating to /plugins/rule-engine/*
  // All routes load lazily to avoid importing @strapi/admin at module load time
  // (which would break the smoke tests that don't have a full Strapi environment).
  //
  // PERF: The layout route wraps all children with EnvironmentProvider ONCE,
  // so fetchEnvironments() is called on first mount and NOT re-called on each
  // route navigation. The children routes only load their page component.
  routes: [
    {
      // Layout route — provides EnvironmentProvider to all nested routes
      path: '/',
      Component: async () => {
        const React = await import('react');
        const { Outlet } = await import('react-router-dom');
        const { EnvironmentProvider } = await import('./contexts/EnvironmentContext');
        // Return a layout component that renders children via Outlet
        return () => (
          <EnvironmentProvider>
            <Outlet />
          </EnvironmentProvider>
        );
      },
      children: [
        {
          path: '',
          index: true,
          Component: async () => {
            const { SyncPage } = await import('./pages/SyncPage');
            return () => <SyncPage />;
          },
        },
        {
          path: 'environments',
          Component: async () => {
            const { EnvironmentsPage } = await import('./pages/EnvironmentsPage');
            return () => <EnvironmentsPage />;
          },
        },
        {
          path: 'sync',
          Component: async () => {
            const { SyncPage } = await import('./pages/SyncPage');
            return () => <SyncPage />;
          },
        },
        {
          path: 'flows/:flowId',
          Component: async () => {
            const { FlowDetailPage } = await import('./pages/FlowDetailPage');
            return () => <FlowDetailPage />;
          },
        },
        {
          path: 'templates',
          Component: async () => {
            const { TemplatesPage } = await import('./pages/TemplatesPage');
            return () => <TemplatesPage />;
          },
        },
        {
          path: 'webhooks',
          Component: async () => {
            const { WebhooksPage } = await import('./pages/WebhooksPage');
            return () => <WebhooksPage />;
          },
        },
        {
          path: 'schedules',
          Component: async () => {
            const { SchedulesPage } = await import('./pages/SchedulesPage');
            return () => <SchedulesPage />;
          },
        },
        {
          path: 'groups',
          Component: async () => {
            const { GroupsPage } = await import('./pages/GroupsPage');
            return () => <GroupsPage />;
          },
        },
        {
          path: 'connections',
          Component: async () => {
            const { ConnectionsPage } = await import('./pages/ConnectionsPage');
            return () => <ConnectionsPage />;
          },
        },
      ],
    },
  ],

  // Lazily load admin translations if/when they are added; none are shipped in
  // FEAT-004 (the intl messages fall back to their defaultMessage).
  async registerTrads() {
    return [];
  },
};

export { PLUGIN_ID };
