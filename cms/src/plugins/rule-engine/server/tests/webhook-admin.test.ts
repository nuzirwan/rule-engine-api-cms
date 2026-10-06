// Unit tests for AdminClient webhook methods (FEAT-004).
//
// Tests use nock to intercept HTTP calls (same pattern as sync.test.ts).

import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import nock from 'nock';

import { AdminClient, resolveAdminConfig } from '../src/services/admin-client';
import { httpFetch } from './fixtures/http-fetch';

const BASE = 'http://engine.webhook-test';
const TOKEN = 'webhook-token-xyz';

/** Build a nock scope that requires the Bearer token on every call. */
function engine() {
  return nock(BASE, { reqheaders: { authorization: `Bearer ${TOKEN}` } });
}

/** Create an AdminClient instance for testing. */
function makeClient() {
  const config = resolveAdminConfig(
    { adminApiBaseUrl: BASE, operatorTokenRef: null, payloadEnv: 'test' },
    { ADMIN_API_OPERATOR_TOKEN: TOKEN }
  );
  return new AdminClient(config, httpFetch);
}

describe('AdminClient webhook methods', () => {
  beforeEach(() => {
    nock.disableNetConnect();
  });

  afterEach(() => {
    nock.cleanAll();
    nock.enableNetConnect();
  });

  describe('listWebhooks', () => {
    it('returns webhooks from GET /admin/webhooks', async () => {
      engine().get('/admin/webhooks').reply(200, {
        webhooks: [
          { id: 'wh-1', name: 'Stripe Payment', provider: 'stripe', flowId: 'payment-flow', updatedAt: '2024-01-01' },
          { id: 'wh-2', name: 'GitHub Push', provider: 'github', flowId: 'ci-flow', updatedAt: '2024-01-02' },
        ],
      });

      const client = makeClient();
      const result = await client.listWebhooks();

      expect(result.webhooks).toHaveLength(2);
      expect(result.webhooks[0]).toMatchObject({ id: 'wh-1', name: 'Stripe Payment', provider: 'stripe' });
      expect(result.webhooks[1]).toMatchObject({ id: 'wh-2', name: 'GitHub Push', provider: 'github' });
    });

    it('throws AdminApiError for 500', async () => {
      engine().get('/admin/webhooks').reply(500, { error: 'internal error' });

      const client = makeClient();
      await expect(client.listWebhooks()).rejects.toMatchObject({
        status: 500,
        recoverable: true,
      });
    });
  });

  describe('getWebhook', () => {
    it('returns webhook from GET /admin/webhooks/{id}', async () => {
      engine().get('/admin/webhooks/wh-1').reply(200, {
        webhookId: 'wh-1',
        name: 'Stripe Payment',
        secretRef: 'env:STRIPE_SECRET',
        provider: 'stripe',
        flowId: 'payment-flow',
        mapping: [{ sourceJsonPath: '$.data.object.id', targetContextPath: 'payment_id' }],
        filter: [{ jsonPath: '$.type', allowedValues: ['payment_intent.succeeded'] }],
        version: 1,
      });

      const client = makeClient();
      const result = await client.getWebhook('wh-1');

      expect(result).toMatchObject({
        webhookId: 'wh-1',
        name: 'Stripe Payment',
        provider: 'stripe',
        mapping: [{ sourceJsonPath: '$.data.object.id', targetContextPath: 'payment_id' }],
      });
    });

    it('throws AdminApiError for 404', async () => {
      engine().get('/admin/webhooks/missing').reply(404, { error: 'not found' });

      const client = makeClient();
      await expect(client.getWebhook('missing')).rejects.toMatchObject({
        status: 404,
        recoverable: false,
      });
    });
  });

  describe('createWebhook', () => {
    it('posts to /admin/webhooks with env', async () => {
      engine()
        .post('/admin/webhooks', (body) => body.env === 'test' && body.webhookId === 'wh-new')
        .reply(201, { webhookId: 'wh-new', version: 1 });

      const client = makeClient();
      const result = await client.createWebhook({
        webhookId: 'wh-new',
        name: 'New Webhook',
        provider: 'generic',
        flowId: 'handler-flow',
      });

      expect(result).toMatchObject({ webhookId: 'wh-new', version: 1 });
    });

    it('throws AdminApiError for 400', async () => {
      engine().post('/admin/webhooks').reply(400, { error: 'validation failed' });

      const client = makeClient();
      await expect(
        client.createWebhook({ webhookId: '', name: '', provider: 'generic', flowId: '' })
      ).rejects.toMatchObject({
        status: 400,
        recoverable: false,
      });
    });
  });

  describe('updateWebhook', () => {
    it('puts to /admin/webhooks/{id} with env', async () => {
      engine()
        .put('/admin/webhooks/wh-1', (body) => body.env === 'test' && body.name === 'Updated Name')
        .reply(200, {
          webhookId: 'wh-1',
          name: 'Updated Name',
          provider: 'stripe',
          flowId: 'payment-flow',
          version: 2,
        });

      const client = makeClient();
      const result = await client.updateWebhook('wh-1', { name: 'Updated Name' });

      expect(result).toMatchObject({ webhookId: 'wh-1', name: 'Updated Name', version: 2 });
    });

    it('throws AdminApiError for 404', async () => {
      engine().put('/admin/webhooks/missing').reply(404, { error: 'not found' });

      const client = makeClient();
      await expect(client.updateWebhook('missing', { name: 'x' })).rejects.toMatchObject({
        status: 404,
        recoverable: false,
      });
    });
  });

  describe('deleteWebhook', () => {
    it('deletes /admin/webhooks/{id}', async () => {
      engine().delete('/admin/webhooks/wh-1').reply(204);

      const client = makeClient();
      // 204 No Content returns null (empty response body parsed as null)
      await expect(client.deleteWebhook('wh-1')).resolves.toBeNull();
    });

    it('throws AdminApiError for 404', async () => {
      engine().delete('/admin/webhooks/missing').reply(404, { error: 'not found' });

      const client = makeClient();
      await expect(client.deleteWebhook('missing')).rejects.toMatchObject({
        status: 404,
        recoverable: false,
      });
    });
  });

  describe('getWebhookLogs', () => {
    it('returns logs from GET /admin/webhooks/{id}/logs', async () => {
      engine().get('/admin/webhooks/wh-1/logs').reply(200, {
        webhookId: 'wh-1',
        logs: [
          {
            timestamp: '2024-01-01T12:00:00Z',
            provider: 'stripe',
            eventType: 'payment_intent.succeeded',
            status: 'processed',
            flowId: 'payment-flow',
            durationMs: 123,
          },
        ],
        total: 1,
      });

      const client = makeClient();
      const result = await client.getWebhookLogs('wh-1');

      expect(result.webhookId).toBe('wh-1');
      expect(result.logs).toHaveLength(1);
      expect(result.logs[0]).toMatchObject({
        eventType: 'payment_intent.succeeded',
        status: 'processed',
      });
    });

    it('passes limit and offset as query params', async () => {
      engine()
        .get('/admin/webhooks/wh-1/logs')
        .query({ limit: '10', offset: '20' })
        .reply(200, { webhookId: 'wh-1', logs: [], total: 0 });

      const client = makeClient();
      const result = await client.getWebhookLogs('wh-1', { limit: 10, offset: 20 });

      expect(result.logs).toHaveLength(0);
    });

    it('throws AdminApiError for 500', async () => {
      engine().get('/admin/webhooks/wh-1/logs').reply(500, { error: 'internal error' });

      const client = makeClient();
      await expect(client.getWebhookLogs('wh-1')).rejects.toMatchObject({
        status: 500,
        recoverable: true,
      });
    });
  });
});
