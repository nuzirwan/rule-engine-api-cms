// Type definitions for EnvironmentManager component.

import type { Environment } from '../../contexts/types';

/**
 * Form data for creating or editing an environment.
 * Matches the Environment interface but without documentId (assigned by Strapi).
 */
export interface EnvironmentFormData {
  /** Unique environment name (uid field). */
  name: string;
  /** Override base URL for the admin API (empty string = use default). */
  adminApiBaseUrl: string;
  /** Reference to a secret containing the operator token (empty string = use default). */
  operatorTokenRef: string;
  /** Value sent in the admin body's `env` field. */
  payloadEnv: string;
}

/**
 * Result of a test connection operation.
 */
export interface TestConnectionResult {
  success: boolean;
  message: string;
  /** Response time in milliseconds (only on success). */
  responseTimeMs?: number;
}

/**
 * Environment with editing state.
 */
export interface EnvironmentWithState extends Environment {
  /** True if currently being deleted. */
  isDeleting?: boolean;
  /** True if currently being tested. */
  isTesting?: boolean;
  /** Last test result (cleared on edit). */
  testResult?: TestConnectionResult;
}

/**
 * Form validation errors.
 */
export interface EnvironmentFormErrors {
  name?: string;
  adminApiBaseUrl?: string;
  operatorTokenRef?: string;
  payloadEnv?: string;
}

/**
 * Convert Environment to form data for editing.
 */
export function environmentToFormData(env: Environment): EnvironmentFormData {
  return {
    name: env.name,
    adminApiBaseUrl: env.adminApiBaseUrl ?? '',
    operatorTokenRef: env.operatorTokenRef ?? '',
    payloadEnv: env.payloadEnv ?? '',
  };
}

/**
 * Create empty form data for new environment.
 */
export function emptyFormData(): EnvironmentFormData {
  return {
    name: '',
    adminApiBaseUrl: '',
    operatorTokenRef: '',
    payloadEnv: '',
  };
}

/**
 * Validate environment form data.
 * Returns errors object (empty if valid).
 */
export function validateFormData(
  data: EnvironmentFormData,
  existingNames: string[],
  editingName?: string
): EnvironmentFormErrors {
  const errors: EnvironmentFormErrors = {};

  // Name is required
  if (!data.name.trim()) {
    errors.name = 'Name is required';
  } else if (!/^[a-z0-9-_]+$/i.test(data.name)) {
    errors.name = 'Name must contain only alphanumeric characters, hyphens, and underscores';
  } else if (
    existingNames.includes(data.name.toLowerCase()) &&
    data.name.toLowerCase() !== editingName?.toLowerCase()
  ) {
    errors.name = 'An environment with this name already exists';
  }

  // Validate URL format if provided
  if (data.adminApiBaseUrl.trim()) {
    try {
      new URL(data.adminApiBaseUrl);
    } catch {
      errors.adminApiBaseUrl = 'Invalid URL format';
    }
  }

  return errors;
}
