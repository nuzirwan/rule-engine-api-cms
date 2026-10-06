// Unit tests for the environment controller.
// Tests use mocked strapi.documents — no HTTP or nock required since this
// controller only interacts with Strapi's document service.

import { describe, expect, it, vi, beforeEach } from 'vitest';

import environmentController from '../src/controllers/environment';

/** Build a minimal mock Strapi ctx. */
function makeCtx(body?: unknown, params?: Record<string, string>) {
  return {
    body: undefined as unknown,
    status: undefined as number | undefined,
    request: { body: body ?? {} },
    params: params ?? {},
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
  const mockFindMany = vi.fn().mockResolvedValue(
    environments.map((e) => ({
      documentId: e.documentId,
      name: e.name,
      adminApiBaseUrl: e.adminApiBaseUrl,
      operatorTokenRef: e.operatorTokenRef,
      payloadEnv: e.payloadEnv,
    }))
  );

  const mockFindOne = vi.fn((opts: { documentId: string }) => {
    const env = environments.find((e) => e.documentId === opts.documentId);
    return Promise.resolve(env ? {
      documentId: env.documentId,
      name: env.name,
      adminApiBaseUrl: env.adminApiBaseUrl,
      operatorTokenRef: env.operatorTokenRef,
      payloadEnv: env.payloadEnv,
    } : null);
  });

  const mockCreate = vi.fn((opts: { data: any }) => Promise.resolve({
    documentId: 'new-doc-id',
    ...opts.data,
  }));

  const mockUpdate = vi.fn((opts: { documentId: string; data: any }) => {
    const existing = environments.find((e) => e.documentId === opts.documentId);
    return Promise.resolve(existing ? {
      ...existing,
      ...opts.data,
    } : null);
  });

  const mockDelete = vi.fn().mockResolvedValue({ documentId: 'deleted' });

  return {
    documents: vi.fn((uid: string) => {
      if (uid === 'api::environment.environment') {
        return {
          findMany: mockFindMany,
          findOne: mockFindOne,
          create: mockCreate,
          update: mockUpdate,
          delete: mockDelete,
        };
      }
      return {
        findMany: vi.fn().mockResolvedValue([]),
        findOne: vi.fn().mockResolvedValue(null),
        create: vi.fn().mockRejectedValue(new Error('Unknown uid')),
        update: vi.fn().mockRejectedValue(new Error('Unknown uid')),
        delete: vi.fn().mockRejectedValue(new Error('Unknown uid')),
      };
    }),
    log: {
      error: vi.fn(),
    },
    _mocks: {
      findMany: mockFindMany,
      findOne: mockFindOne,
      create: mockCreate,
      update: mockUpdate,
      delete: mockDelete,
    },
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
      expect(strapi._mocks.findMany).toHaveBeenCalledWith({
        fields: ['name', 'adminApiBaseUrl', 'operatorTokenRef', 'payloadEnv'],
      });
    });
  });

  describe('create', () => {
    it('returns 400 when name is missing', async () => {
      const strapi = makeStrapi([]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({ adminApiBaseUrl: 'https://api.example.com' });

      await ctrl.create(ctx);

      expect(ctx.status).toBe(400);
      expect(ctx.body).toEqual({ error: 'Name is required' });
    });

    it('returns 400 when name is empty', async () => {
      const strapi = makeStrapi([]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({ name: '  ' });

      await ctrl.create(ctx);

      expect(ctx.status).toBe(400);
      expect(ctx.body).toEqual({ error: 'Name is required' });
    });

    it('returns 409 when name already exists', async () => {
      const strapi = makeStrapi([{ documentId: 'doc-1', name: 'production' }]);
      // Override findMany to return match for duplicate check
      strapi._mocks.findMany.mockResolvedValueOnce([{ documentId: 'doc-1', name: 'production' }]);

      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({ name: 'production' });

      await ctrl.create(ctx);

      expect(ctx.status).toBe(409);
      expect(ctx.body).toEqual({ error: 'An environment with this name already exists' });
    });

    it('creates environment with all fields', async () => {
      const strapi = makeStrapi([]);
      strapi._mocks.findMany.mockResolvedValueOnce([]); // No duplicates

      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({
        name: 'new-env',
        adminApiBaseUrl: 'https://api.example.com',
        operatorTokenRef: 'TOKEN_REF',
        payloadEnv: 'test',
      });

      await ctrl.create(ctx);

      expect(ctx.status).toBe(201);
      expect(strapi._mocks.create).toHaveBeenCalledWith({
        data: {
          name: 'new-env',
          adminApiBaseUrl: 'https://api.example.com',
          operatorTokenRef: 'TOKEN_REF',
          payloadEnv: 'test',
        },
      });
    });

    it('trims name and normalizes empty strings to null', async () => {
      const strapi = makeStrapi([]);
      strapi._mocks.findMany.mockResolvedValueOnce([]);

      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({
        name: '  test-env  ',
        adminApiBaseUrl: '',
        operatorTokenRef: '',
        payloadEnv: '',
      });

      await ctrl.create(ctx);

      expect(strapi._mocks.create).toHaveBeenCalledWith({
        data: {
          name: 'test-env',
          adminApiBaseUrl: null,
          operatorTokenRef: null,
          payloadEnv: '',
        },
      });
    });
  });

  describe('update', () => {
    it('returns 404 when environment does not exist', async () => {
      const strapi = makeStrapi([]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({ adminApiBaseUrl: 'https://new.example.com' }, { id: 'non-existent' });

      await ctrl.update(ctx);

      expect(ctx.status).toBe(404);
      expect(ctx.body).toEqual({ error: 'Environment not found' });
    });

    it('updates environment fields', async () => {
      const strapi = makeStrapi([
        {
          documentId: 'doc-1',
          name: 'production',
          adminApiBaseUrl: 'https://old.example.com',
          operatorTokenRef: 'OLD_TOKEN',
          payloadEnv: 'old',
        },
      ]);

      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx(
        {
          adminApiBaseUrl: 'https://new.example.com',
          operatorTokenRef: 'NEW_TOKEN',
          payloadEnv: 'new',
        },
        { id: 'doc-1' }
      );

      await ctrl.update(ctx);

      expect(strapi._mocks.update).toHaveBeenCalledWith({
        documentId: 'doc-1',
        data: {
          adminApiBaseUrl: 'https://new.example.com',
          operatorTokenRef: 'NEW_TOKEN',
          payloadEnv: 'new',
        },
      });
    });

    it('preserves existing values when not provided in update', async () => {
      const strapi = makeStrapi([
        {
          documentId: 'doc-1',
          name: 'production',
          adminApiBaseUrl: 'https://existing.example.com',
          operatorTokenRef: 'EXISTING_TOKEN',
          payloadEnv: 'existing',
        },
      ]);

      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({ payloadEnv: 'updated' }, { id: 'doc-1' });

      await ctrl.update(ctx);

      expect(strapi._mocks.update).toHaveBeenCalledWith({
        documentId: 'doc-1',
        data: {
          adminApiBaseUrl: 'https://existing.example.com',
          operatorTokenRef: 'EXISTING_TOKEN',
          payloadEnv: 'updated',
        },
      });
    });
  });

  describe('remove', () => {
    it('returns 404 when environment does not exist', async () => {
      const strapi = makeStrapi([]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({}, { id: 'non-existent' });

      await ctrl.remove(ctx);

      expect(ctx.status).toBe(404);
      expect(ctx.body).toEqual({ error: 'Environment not found' });
    });

    it('deletes environment and returns success', async () => {
      const strapi = makeStrapi([{ documentId: 'doc-1', name: 'production' }]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({}, { id: 'doc-1' });

      await ctrl.remove(ctx);

      expect(strapi._mocks.delete).toHaveBeenCalledWith({ documentId: 'doc-1' });
      expect(ctx.body).toEqual({ success: true });
    });
  });

  describe('testConnection', () => {
    it('returns 404 when environment does not exist', async () => {
      const strapi = makeStrapi([]);
      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({}, { id: 'non-existent' });

      await ctrl.testConnection(ctx);

      expect(ctx.status).toBe(404);
      expect(ctx.body).toEqual({ error: 'Environment not found' });
    });

    it('returns config error when ADMIN_API_BASE_URL is not configured', async () => {
      // This test verifies the error handling when resolveAdminConfig throws
      const strapi = makeStrapi([
        {
          documentId: 'doc-1',
          name: 'test',
          adminApiBaseUrl: null,
          operatorTokenRef: null,
          payloadEnv: '',
        },
      ]);

      // Clear env vars to trigger config error
      const originalEnv = process.env;
      process.env = { ...originalEnv };
      delete process.env.ADMIN_API_BASE_URL;
      delete process.env.ADMIN_API_OPERATOR_TOKEN;

      const ctrl = environmentController({ strapi } as any);
      const ctx = makeCtx({}, { id: 'doc-1' });

      await ctrl.testConnection(ctx);

      // Should return error about missing config
      expect(ctx.body).toHaveProperty('success', false);
      expect(ctx.body).toHaveProperty('message');

      // Restore env
      process.env = originalEnv;
    });
  });
});

