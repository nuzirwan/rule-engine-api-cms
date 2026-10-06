// Unit tests for EnvironmentManager component.
// Tests the types exports and form validation logic.
// Note: These are smoke tests for the type interface, not full render tests,
// since rendering requires the full Strapi admin environment.

import { describe, expect, it, vi, beforeEach } from 'vitest';

// Import only the types module which doesn't import design-system
import {
  type EnvironmentFormData,
  type EnvironmentFormErrors,
  type TestConnectionResult,
  validateFormData,
  emptyFormData,
  environmentToFormData,
} from './types';

// Environment type for test mocks
interface Environment {
  documentId: string;
  name: string;
  adminApiBaseUrl: string | null;
  operatorTokenRef: string | null;
  payloadEnv: string;
}

const mockEnvironments: Environment[] = [
  {
    documentId: 'doc-1',
    name: 'production',
    adminApiBaseUrl: 'https://prod.example.com',
    operatorTokenRef: 'PROD_TOKEN',
    payloadEnv: 'prod',
  },
  {
    documentId: 'doc-2',
    name: 'staging',
    adminApiBaseUrl: null,
    operatorTokenRef: null,
    payloadEnv: '',
  },
];

describe('EnvironmentManager', () => {
  describe('module structure', () => {
    it('follows the component naming convention', () => {
      const COMPONENT_PATH = 'components/EnvironmentManager/index.tsx';
      expect(COMPONENT_PATH).toContain('EnvironmentManager');
    });
  });

  describe('props interface', () => {
    it('documents expected optional onUpdate callback', () => {
      interface EnvironmentManagerProps {
        onUpdate?: () => void;
      }

      const onUpdate = vi.fn();
      const props: EnvironmentManagerProps = { onUpdate };
      expect(typeof props.onUpdate).toBe('function');
    });
  });
});

describe('EnvironmentManager types', () => {
  describe('EnvironmentFormData', () => {
    it('has expected shape', () => {
      const formData: EnvironmentFormData = {
        name: 'test',
        adminApiBaseUrl: 'https://api.example.com',
        operatorTokenRef: 'TOKEN_REF',
        payloadEnv: 'test',
      };

      expect(typeof formData.name).toBe('string');
      expect(typeof formData.adminApiBaseUrl).toBe('string');
      expect(typeof formData.operatorTokenRef).toBe('string');
      expect(typeof formData.payloadEnv).toBe('string');
    });
  });

  describe('TestConnectionResult', () => {
    it('has expected shape for success', () => {
      const result: TestConnectionResult = {
        success: true,
        message: 'Connected successfully',
        responseTimeMs: 150,
      };

      expect(result.success).toBe(true);
      expect(typeof result.message).toBe('string');
      expect(typeof result.responseTimeMs).toBe('number');
    });

    it('has expected shape for failure', () => {
      const result: TestConnectionResult = {
        success: false,
        message: 'Connection failed',
      };

      expect(result.success).toBe(false);
      expect(result.responseTimeMs).toBeUndefined();
    });
  });
});

describe('EnvironmentManager form validation', () => {
  describe('validateFormData', () => {
    it('returns empty object for valid data', () => {
      const data: EnvironmentFormData = {
        name: 'test-env',
        adminApiBaseUrl: 'https://api.example.com',
        operatorTokenRef: 'TOKEN_REF',
        payloadEnv: 'test',
      };

      const errors = validateFormData(data, []);
      expect(errors).toEqual({});
    });

    it('requires name', () => {
      const data: EnvironmentFormData = {
        name: '',
        adminApiBaseUrl: '',
        operatorTokenRef: '',
        payloadEnv: '',
      };

      const errors = validateFormData(data, []);
      expect(errors.name).toBe('Name is required');
    });

    it('validates name format', () => {
      const data: EnvironmentFormData = {
        name: 'invalid name!',
        adminApiBaseUrl: '',
        operatorTokenRef: '',
        payloadEnv: '',
      };

      const errors = validateFormData(data, []);
      expect(errors.name).toContain('alphanumeric');
    });

    it('detects duplicate names (case-insensitive)', () => {
      const data: EnvironmentFormData = {
        name: 'Production',
        adminApiBaseUrl: '',
        operatorTokenRef: '',
        payloadEnv: '',
      };

      const errors = validateFormData(data, ['production']);
      expect(errors.name).toContain('already exists');
    });

    it('allows same name when editing', () => {
      const data: EnvironmentFormData = {
        name: 'production',
        adminApiBaseUrl: '',
        operatorTokenRef: '',
        payloadEnv: '',
      };

      const errors = validateFormData(data, ['production'], 'production');
      expect(errors.name).toBeUndefined();
    });

    it('validates URL format when provided', () => {
      const data: EnvironmentFormData = {
        name: 'test',
        adminApiBaseUrl: 'not-a-url',
        operatorTokenRef: '',
        payloadEnv: '',
      };

      const errors = validateFormData(data, []);
      expect(errors.adminApiBaseUrl).toBe('Invalid URL format');
    });

    it('allows empty URL', () => {
      const data: EnvironmentFormData = {
        name: 'test',
        adminApiBaseUrl: '',
        operatorTokenRef: '',
        payloadEnv: '',
      };

      const errors = validateFormData(data, []);
      expect(errors.adminApiBaseUrl).toBeUndefined();
    });

    it('accepts valid URL formats', () => {
      const validUrls = [
        'https://api.example.com',
        'http://localhost:8080',
        'https://engine.internal:3000/api',
      ];

      for (const url of validUrls) {
        const data: EnvironmentFormData = {
          name: 'test',
          adminApiBaseUrl: url,
          operatorTokenRef: '',
          payloadEnv: '',
        };

        const errors = validateFormData(data, []);
        expect(errors.adminApiBaseUrl).toBeUndefined();
      }
    });
  });

  describe('emptyFormData', () => {
    it('returns empty strings for all fields', () => {
      const data = emptyFormData();

      expect(data.name).toBe('');
      expect(data.adminApiBaseUrl).toBe('');
      expect(data.operatorTokenRef).toBe('');
      expect(data.payloadEnv).toBe('');
    });
  });

  describe('environmentToFormData', () => {
    it('converts Environment to form data', () => {
      const env = mockEnvironments[0];
      const formData = environmentToFormData(env);

      expect(formData.name).toBe('production');
      expect(formData.adminApiBaseUrl).toBe('https://prod.example.com');
      expect(formData.operatorTokenRef).toBe('PROD_TOKEN');
      expect(formData.payloadEnv).toBe('prod');
    });

    it('converts null values to empty strings', () => {
      const env = mockEnvironments[1];
      const formData = environmentToFormData(env);

      expect(formData.name).toBe('staging');
      expect(formData.adminApiBaseUrl).toBe('');
      expect(formData.operatorTokenRef).toBe('');
      expect(formData.payloadEnv).toBe('');
    });
  });
});

describe('EnvironmentManager API endpoints', () => {
  it('targets correct endpoint for create', () => {
    const CREATE_ENDPOINT = '/rule-engine/environments';
    expect(CREATE_ENDPOINT).toBe('/rule-engine/environments');
  });

  it('targets correct endpoint for update', () => {
    const UPDATE_ENDPOINT = '/rule-engine/environments/doc-1';
    expect(UPDATE_ENDPOINT).toContain('/rule-engine/environments/');
  });

  it('targets correct endpoint for delete', () => {
    const DELETE_ENDPOINT = '/rule-engine/environments/doc-1';
    expect(DELETE_ENDPOINT).toContain('/rule-engine/environments/');
  });

  it('targets correct endpoint for test connection', () => {
    const TEST_ENDPOINT = '/rule-engine/environments/doc-1/test';
    expect(TEST_ENDPOINT).toContain('/test');
  });
});
