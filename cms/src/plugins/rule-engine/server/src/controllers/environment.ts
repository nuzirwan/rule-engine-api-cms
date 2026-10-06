// environment.ts — controller for environment management.
// Provides endpoints:
//   * GET /environments — list all Environment content type entries

/**
 * Environment controller factory.
 * Returns handlers for environment-related endpoints.
 */
export default function environmentController({ strapi }: { strapi: any }) {
  return {
    /**
     * GET /environments — list all Environment entries from the CMS.
     * Returns { environments: Environment[] }.
     */
    async list(ctx: any) {
      const results = await strapi.documents('api::environment.environment').findMany({
        fields: ['name', 'adminApiBaseUrl', 'operatorTokenRef', 'payloadEnv'],
      });

      ctx.body = {
        environments: results.map((r: any) => ({
          documentId: r.documentId,
          name: r.name,
          adminApiBaseUrl: r.adminApiBaseUrl ?? null,
          operatorTokenRef: r.operatorTokenRef ?? null,
          payloadEnv: r.payloadEnv ?? '',
        })),
      };
    },
  };
}
