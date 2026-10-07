// template.ts — controller for flow template operations. Provides endpoints:
//   * GET  /templates           — list all available templates
//   * GET  /templates/:id       — get a single template by ID
//   * POST /templates/:id/preview    — preview substituted flows without creating
//   * POST /templates/:id/instantiate — create flows from template
//
// Templates are in-memory (loaded from embedded JSON), so no AdminClient is used.
// The instantiate endpoint creates Strapi api::flow.flow documents directly.

import {
  listTemplates,
  getTemplate,
  substituteTemplate,
  TemplateVarError,
  type TemplateDefinition,
  type InstantiatedFlow,
} from '../services/template-service';

/** Reference to a created flow document. */
interface CreatedFlowRef {
  flowId: string;
  documentId: string;
}

/** Error info for a flow that failed to create. */
interface CreateFlowError {
  flowId: string;
  message: string;
}

/**
 * Template controller factory.
 */
export default function templateController({ strapi }: { strapi: any }) {
  return {
    /**
     * GET /templates — list all available built-in templates.
     */
    async listTemplates(ctx: any) {
      const templates = listTemplates();
      ctx.body = { templates };
    },

    /**
     * GET /templates/:id — get a single template by ID.
     */
    async getTemplate(ctx: any) {
      const { id } = ctx.params as { id: string };
      const template = getTemplate(id);

      if (!template) {
        ctx.status = 404;
        ctx.body = { error: 'template not found' };
        return;
      }

      ctx.body = { template };
    },

    /**
     * POST /templates/:id/preview — preview substituted flows without creating.
     * Body: { variables: Record<string, string> }
     */
    async previewTemplate(ctx: any) {
      const { id } = ctx.params as { id: string };
      const { variables = {} } = ctx.request.body as { variables?: Record<string, string> };

      const template = getTemplate(id);
      if (!template) {
        ctx.status = 404;
        ctx.body = { error: 'template not found' };
        return;
      }

      try {
        const flows = substituteTemplate(template, variables);
        ctx.body = { flows };
      } catch (err) {
        if (err instanceof TemplateVarError) {
          ctx.status = 400;
          ctx.body = { error: err.message };
          return;
        }
        throw err;
      }
    },

    /**
     * POST /templates/:id/instantiate — create flows from template.
     * Body: { variables: Record<string, string>, environmentDocumentId?: string }
     *
     * Creates api::flow.flow documents via Strapi Document Service.
     * On partial failure: returns 200 with created + errors arrays.
     * On all failed: returns 500.
     */
    async instantiateTemplate(ctx: any) {
      const { id } = ctx.params as { id: string };
      const { variables = {}, environmentDocumentId } = ctx.request.body as {
        variables?: Record<string, string>;
        environmentDocumentId?: string;
      };

      const template = getTemplate(id);
      if (!template) {
        ctx.status = 404;
        ctx.body = { error: 'template not found' };
        return;
      }

      // Substitute variables
      let flows: InstantiatedFlow[];
      try {
        flows = substituteTemplate(template, variables);
      } catch (err) {
        if (err instanceof TemplateVarError) {
          ctx.status = 400;
          ctx.body = { error: err.message };
          return;
        }
        throw err;
      }

      // Create each flow as a Strapi document
      const created: CreatedFlowRef[] = [];
      const errors: CreateFlowError[] = [];

      for (const flow of flows) {
        try {
          // Build the data payload
          const data: Record<string, unknown> = {
            flowId: flow.flowId,
            method: flow.method,
            path: flow.path,
            tree: flow.tree,
          };

          // Attach environment relation if provided (Strapi 5 connect syntax)
          if (environmentDocumentId) {
            data.environment = { connect: [{ documentId: environmentDocumentId }] };
          }

          const doc = await strapi.documents('api::flow.flow').create({ data });
          created.push({ flowId: flow.flowId, documentId: doc.documentId });
        } catch (err) {
          const message = err instanceof Error ? err.message : String(err);
          errors.push({ flowId: flow.flowId, message });
          strapi.log.warn(
            `[template] Failed to create flow "${flow.flowId}" from template "${id}": ${message}`
          );
        }
      }

      // Determine response based on outcome
      if (created.length === 0 && errors.length > 0) {
        // All failed
        ctx.status = 500;
        ctx.body = {
          error: 'all flows failed to create',
          created: [],
          errors,
          count: 0,
        };
        return;
      }

      if (errors.length > 0) {
        // Partial success
        ctx.status = 200;
        ctx.body = { created, errors, count: created.length };
        return;
      }

      // Full success
      ctx.status = 201;
      ctx.body = { created, count: created.length };
    },
  };
}
