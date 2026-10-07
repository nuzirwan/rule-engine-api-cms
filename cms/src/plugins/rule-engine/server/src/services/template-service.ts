/**
 * template-service.ts — Loads built-in flow templates and substitutes variables
 * to produce concrete flow definitions ready for Strapi document creation.
 *
 * Templates are static JSON files embedded at build time. The service provides:
 * - listTemplates(): enumerate all available templates
 * - getTemplate(id): retrieve a single template by ID
 * - substituteTemplate(template, vars): resolve placeholders and return flows
 */

// Import template JSON files (requires resolveJsonModule: true in tsconfig)
import crudTemplate from '../templates/crud-rest-postgres.json';
import webhookTemplate from '../templates/webhook-handler.json';
import dataSyncTemplate from '../templates/data-sync.json';

// --- Types ---

export interface TemplateVariable {
  name: string;
  type: 'string' | 'int' | 'boolean';
  description: string;
  default?: string;
  optional?: boolean;
}

export interface TemplateFlowDef {
  flowId: string;
  method: string;
  path: string;
  tree: unknown;
}

export interface TemplateDefinition {
  id: string;
  name: string;
  description: string;
  category: string;
  variables: TemplateVariable[];
  flows: TemplateFlowDef[];
}

export interface InstantiatedFlow {
  flowId: string;
  method: string;
  path: string;
  tree: unknown;
}

/** Thrown when template variable resolution fails. */
export class TemplateVarError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'TemplateVarError';
  }
}

// --- Built-in templates ---

const BUILTIN_TEMPLATES: TemplateDefinition[] = [
  crudTemplate as unknown as TemplateDefinition,
  webhookTemplate as unknown as TemplateDefinition,
  dataSyncTemplate as unknown as TemplateDefinition,
];

// --- Variable resolution helpers ---

/**
 * Resolve all template variables from supplied values and defaults.
 * Throws TemplateVarError if a required variable is missing or contains
 * unsafe JSON characters.
 */
function resolveVars(
  variables: TemplateVariable[],
  supplied: Record<string, string>
): Map<string, string> {
  const resolved = new Map<string, string>();

  for (const v of variables) {
    let value = supplied[v.name];
    if (value === undefined || value === '') {
      value = v.default ?? '';
    }

    if (!value && !v.optional) {
      throw new TemplateVarError(`required variable "${v.name}" is not set`);
    }

    // Validate JSON-string safety (no raw " or \ or control chars)
    if (/["\\\x00-\x1f]/.test(value)) {
      throw new TemplateVarError(
        `variable "${v.name}" contains characters unsafe for JSON strings`
      );
    }

    resolved.set(v.name, value);
  }

  return resolved;
}

/**
 * Replace all {{VAR}} placeholders in a string with resolved values.
 * Placeholders are UPPER_SNAKE_CASE template variables; runtime expressions
 * like {{input.id}} are lowercase with dots and are NOT replaced.
 */
function applyVars(str: string, resolved: Map<string, string>): string {
  let out = str;
  for (const [k, v] of resolved) {
    out = out.replaceAll(`{{${k}}}`, v);
  }
  return out;
}

// --- Public API ---

/**
 * List all available built-in templates.
 */
export function listTemplates(): TemplateDefinition[] {
  return BUILTIN_TEMPLATES;
}

/**
 * Get a template by ID, or null if not found.
 */
export function getTemplate(id: string): TemplateDefinition | null {
  return BUILTIN_TEMPLATES.find((t) => t.id === id) ?? null;
}

/**
 * Substitute variables into a template and return instantiated flows.
 * Throws TemplateVarError if resolution fails or the resulting JSON is invalid.
 *
 * @param template The template definition
 * @param vars Caller-supplied variable values (name → value)
 * @returns Array of flows ready for Strapi creation
 */
export function substituteTemplate(
  template: TemplateDefinition,
  vars: Record<string, string>
): InstantiatedFlow[] {
  const resolved = resolveVars(template.variables, vars);
  const flows: InstantiatedFlow[] = [];

  for (const fd of template.flows) {
    // Substitute in flow metadata
    const flowId = applyVars(fd.flowId, resolved);
    const method = applyVars(fd.method, resolved);
    const path = applyVars(fd.path, resolved);

    // Substitute in tree: serialize → replace → parse
    const treeStr = JSON.stringify(fd.tree);
    const substitutedStr = applyVars(treeStr, resolved);

    let tree: unknown;
    try {
      tree = JSON.parse(substitutedStr);
    } catch (err) {
      throw new TemplateVarError(
        `template "${template.id}" flow "${fd.flowId}" tree invalid after substitution: ${(err as Error).message}`
      );
    }

    flows.push({ flowId, method, path, tree });
  }

  return flows;
}
