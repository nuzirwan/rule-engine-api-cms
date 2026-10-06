// Unit tests for the environment controller.
// Tests use mocked strapi.documents — no HTTP or nock required since this
// controller only interacts with Strapi's document service.

import { describe, expect, it, vi } from 'vitest';

import environmentController from '../src/controllers/environment';

/** Build a minimal mock Strapi ctx. */
function makeCtx() {
  return {
    body: undefined as unknown,
    status: undefined as number | undefined,
  };
}

/** Build a mock strapi instance with document service stubs. */
function makeStrapi(environments: Array<{
  documentId: string;
  name: string;
  adminApiBaseUrl?: string | null;
  operatorTokenRef?: string | null;
  payloadEnv?: string;
}>) {
  return {
    documents: vi.fn((uid: string) => {
      if (uid === 'api::environment.environment') {
        return {
          findMany: vi.fn().mockResolvedValue(
            environments.map((e) => ({
              documentId: e.documentId,
              name: e.name,
              adminApiBaseUrl: e.adminApiBaseUrl,
              operatorTokenRef: e.operatorTokenRef,
              payloadEnv: e.payloadEnv,
            }))
          ),
        };
      }
      return {
        findMany: vi.fn().mockResolvedValue([]),
      };
    }),
  };
}

describe('environment controller', () => {
  describe('list', () => {
    it('returns empty array when no environments exist', async () => {
      const strapi = makeStrapi([]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx();

      await ctrl.list(ctx);

      expect(ctx.body).toEqual({ environments: [] });
    });

    it('returns all environments with normalized fields', async () => {
      const strapi = makeStrapi([
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
      ]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx();

      await ctrl.list(ctx);

      expect(ctx.body).toEqual({
        environments: [
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
        ],
      });
    });

    it('normalizes undefined fields to null or default values', async () => {
      const strapi = makeStrapi([
        {
          documentId: 'doc-3',
          name: 'dev',
          // Simulating fields that may be undefined from DB
          adminApiBaseUrl: undefined as any,
          operatorTokenRef: undefined as any,
          payloadEnv: undefined as any,
        },
      ]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx();

      await ctrl.list(ctx);

      expect(ctx.body).toEqual({
        environments: [
          {
            documentId: 'doc-3',
            name: 'dev',
            adminApiBaseUrl: null,
            operatorTokenRef: null,
            payloadEnv: '',
          },
        ],
      });
    });

    it('calls strapi.documents with correct uid and fields', async () => {
      const strapi = makeStrapi([]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx();

      await ctrl.list(ctx);

      expect(strapi.documents).toHaveBeenCalledWith('api::environment.environment');
      const docService = strapi.documents.mock.results[0].value;
      expect(docService.findMany).toHaveBeenCalledWith({
        fields: ['name', 'adminApiBaseUrl', 'operatorTokenRef', 'payloadEnv'],
      });
    });
  });
});
