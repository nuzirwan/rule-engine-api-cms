// Type definitions for the Environment context.
// Matches the Strapi Environment content type from api::environment.environment.

/**
 * Environment entity as stored in Strapi.
 * Maps to the schema defined in cms/src/api/environment/content-types/environment/schema.json
 */
export interface Environment {
  /** Strapi document ID (UUID). */
  documentId: string;
  /** Unique environment name (uid field). */
  name: string;
  /** Override base URL for the admin API (null = use default). */
  adminApiBaseUrl: string | null;
  /** Reference to a secret containing the operator token (null = use default). */
  operatorTokenRef: string | null;
  /** Value sent in the admin body's `env` field (default empty string). */
  payloadEnv: string;
}
