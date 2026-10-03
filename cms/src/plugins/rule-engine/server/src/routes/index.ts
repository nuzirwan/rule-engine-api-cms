// Admin-scoped route for the publish controller (design §5.4). The route is
// mounted on the plugin's admin router so it sits behind Strapi's admin auth +
// RBAC; the handler (controllers/publish.ts) runs the ordered, publish-blocking
// sequence through the engine admin HTTP API.
//
// The plugin stays disabled until FEAT-004 wires the admin UI; this file
// completes the server module graph so the controller is reachable the moment
// the plugin is enabled.

export default {
  admin: {
    type: 'admin',
    routes: [
      {
        method: 'POST',
        path: '/flows/:id/publish',
        handler: 'publish.publish',
        config: {
          // Guarded by Strapi admin auth; a dedicated RBAC action can be added
          // when the admin UI (FEAT-004) ships its permission wiring.
          policies: [],
        },
      },
    ],
  },
};
