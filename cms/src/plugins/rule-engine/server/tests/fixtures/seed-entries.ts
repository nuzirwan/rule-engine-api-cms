// Test fixtures mirroring the engine seed at internal/config/testdata/seed.json.
// These are the CMS-entry shapes the publish-transform + publish-sequence tests
// feed in. The flow trees reproduce the seed trees VERBATIM so the §6.1/§6.2
// round-trip assertions (mixed casing: operation.{Kind,Payload,Required}
// capitalized, everything else camelCase) exercise the real engine shapes.
//
// Connections here are the CMS-authored, credential-free DISCRETE shape
// (host/port/database + secretRef), NOT the literal seed DSNs (design §6.1
// finding 3) — the seed stores a dsn, which the transform must REJECT; a
// separate test feeds the seed-style dsn to assert rejection.

import type { EngineNode } from '../../../../../../../types/engine';
import type {
  ConnectionEntry,
  FlowEntry,
  JdmEntry,
} from '../../src/services/publish-transform';

// ----------------------------------------------------------------------------
// Flow trees (verbatim from seed.json)
// ----------------------------------------------------------------------------

/** orders-expedite tree — action/condition/action/set/response with branches. */
export const ordersExpediteTree: EngineNode = {
  id: 'trigger',
  type: 'trigger',
  spec: {
    method: 'GET',
    path: '/orders/{id}',
    input: { params: ['id'] },
  },
  children: [
    {
      id: 'read-order',
      type: 'action',
      spec: {
        connection: 'orders-pg',
        operation: {
          Kind: 'query',
          Payload: {
            sql: 'SELECT amount, status FROM orders WHERE id=$1',
            params: [{ value: '{{input.id}}', as: 'int' }],
          },
          Required: true,
        },
        saveAs: 'order',
      },
      children: [
        {
          id: 'classify',
          type: 'condition',
          spec: {
            jdmId: 'order',
            input: ['order.amount', 'order.status'],
            trueKey: 'expedited',
            falseKey: 'standard',
          },
          children: [
            {
              id: 'expedited',
              type: 'action',
              spec: {
                connection: 'ship-rest',
                operation: {
                  Kind: 'http',
                  Payload: {
                    method: 'POST',
                    path: '/expedite',
                    body: { order: '{{order}}' },
                  },
                  Required: true,
                },
                saveAs: 'decision',
              },
              children: [
                {
                  id: 'set-expedited',
                  type: 'set',
                  spec: { targetPath: 'shipping', from: 'decision.body.shipping' },
                  children: [
                    {
                      id: 'respond-expedited',
                      type: 'response',
                      spec: { status: 200, bodyFrom: '' },
                    },
                  ],
                },
              ],
            },
            {
              id: 'standard',
              type: 'action',
              spec: {
                connection: 'ship-rest',
                operation: {
                  Kind: 'http',
                  Payload: {
                    method: 'POST',
                    path: '/standard',
                    body: { order: '{{order}}' },
                  },
                  Required: true,
                },
                saveAs: 'decision',
              },
              children: [
                {
                  id: 'set-standard',
                  type: 'set',
                  spec: { targetPath: 'shipping', from: 'decision.body.shipping' },
                  children: [
                    {
                      id: 'respond-standard',
                      type: 'response',
                      spec: { status: 200, bodyFrom: '' },
                    },
                  ],
                },
              ],
            },
          ],
        },
      ],
    },
  ],
};

/** fmc-order-by-id tree — action then a deep chain of set nodes + decision. */
export const fmcOrderByIdTree: EngineNode = {
  id: 'trigger',
  type: 'trigger',
  spec: {
    method: 'GET',
    path: '/order/{order_id}',
    input: { params: ['order_id'] },
  },
  children: [
    {
      id: 'read-order',
      type: 'action',
      spec: {
        connection: 'fmc-pg',
        operation: {
          Kind: 'query',
          Payload: {
            sql: 'SELECT order_id, msisdn, order_status, payment_status, payment_amount, product_name FROM fmc_order.order_status WHERE order_id = $1',
            params: ['{{input.order_id}}'],
          },
          Required: true,
        },
        saveAs: 'row',
      },
      children: [
        {
          id: 'echo-order_id',
          type: 'set',
          spec: { targetPath: 'order_id', from: 'row.order_id' },
          children: [
            {
              id: 'echo-msisdn',
              type: 'set',
              spec: { targetPath: 'msisdn', from: 'row.msisdn' },
              children: [
                {
                  id: 'echo-order_status',
                  type: 'set',
                  spec: { targetPath: 'order_status', from: 'row.order_status' },
                  children: [
                    {
                      id: 'echo-payment_status',
                      type: 'set',
                      spec: { targetPath: 'payment_status', from: 'row.payment_status' },
                      children: [
                        {
                          id: 'echo-payment_amount',
                          type: 'set',
                          spec: { targetPath: 'payment_amount', from: 'row.payment_amount' },
                          children: [
                            {
                              id: 'echo-product_name',
                              type: 'set',
                              spec: { targetPath: 'product_name', from: 'row.product_name' },
                              children: [
                                {
                                  id: 'decide',
                                  type: 'decision',
                                  spec: {
                                    jdmId: 'fmc-payment',
                                    input: ['row.payment_status'],
                                    saveAs: 'dec',
                                  },
                                },
                                {
                                  id: 'set-action',
                                  type: 'set',
                                  spec: { targetPath: 'action', from: 'dec.action' },
                                },
                                {
                                  id: 'respond',
                                  type: 'response',
                                  spec: { status: 200, bodyFrom: '' },
                                },
                              ],
                            },
                          ],
                        },
                      ],
                    },
                  ],
                },
              ],
            },
          ],
        },
      ],
    },
  ],
};

// ----------------------------------------------------------------------------
// Flow entries (CMS content shape the transform/sequence consume)
// ----------------------------------------------------------------------------

export const ordersExpediteFlow: FlowEntry = {
  flowId: 'orders-expedite',
  method: 'GET',
  path: '/orders/{id}',
  tree: ordersExpediteTree,
  fixtures: [
    { name: 'high-value paid order expedites', input: { id: 1 } },
    { name: 'low-value paid order ships standard', input: { id: 2 } },
  ],
};

export const fmcOrderByIdFlow: FlowEntry = {
  flowId: 'fmc-order-by-id',
  method: 'GET',
  path: '/order/{order_id}',
  tree: fmcOrderByIdTree,
};

// ----------------------------------------------------------------------------
// JDM graphs (verbatim from seed.json)
// ----------------------------------------------------------------------------

export const orderJdmDoc = {
  nodes: [
    { id: 'in', type: 'inputNode', name: 'Request', position: { x: 100, y: 100 } },
    {
      id: 'dt',
      type: 'decisionTableNode',
      name: 'Shipping',
      position: { x: 350, y: 100 },
      content: {
        hitPolicy: 'first',
        inputs: [
          { id: 'i1', name: 'Amount', field: 'amount', type: 'expression' },
          { id: 'i2', name: 'Status', field: 'status', type: 'expression' },
        ],
        outputs: [{ id: 'o1', name: 'Shipping', field: 'shipping', type: 'expression' }],
        rules: [
          { _id: 'r1', i1: '> 1000', i2: "== 'paid'", o1: "'expedited'" },
          { _id: 'r2', i1: '', i2: '', o1: "'standard'" },
        ],
      },
    },
    { id: 'out', type: 'outputNode', name: 'Response', position: { x: 600, y: 100 } },
  ],
  edges: [
    { id: 'e1', type: 'edge', sourceId: 'in', targetId: 'dt' },
    { id: 'e2', type: 'edge', sourceId: 'dt', targetId: 'out' },
  ],
};

export const fmcPaymentJdmDoc = {
  nodes: [
    { id: 'in', type: 'inputNode', name: 'Request', position: { x: 100, y: 100 } },
    {
      id: 'dt',
      type: 'decisionTableNode',
      name: 'Payment',
      position: { x: 350, y: 100 },
      content: {
        hitPolicy: 'first',
        inputs: [
          { id: 'i1', name: 'PaymentStatus', field: 'payment_status', type: 'expression' },
        ],
        outputs: [{ id: 'o1', name: 'Action', field: 'action', type: 'expression' }],
        rules: [
          { _id: 'r1', i1: "== 'PAID'", o1: "'proceed_fulfillment'" },
          { _id: 'r2', i1: '', o1: "'await_payment'" },
        ],
      },
    },
    { id: 'out', type: 'outputNode', name: 'Response', position: { x: 600, y: 100 } },
  ],
  edges: [
    { id: 'e1', type: 'edge', sourceId: 'in', targetId: 'dt' },
    { id: 'e2', type: 'edge', sourceId: 'dt', targetId: 'out' },
  ],
};

export const orderJdm: JdmEntry = { jdmId: 'order', doc: orderJdmDoc };
export const fmcPaymentJdm: JdmEntry = { jdmId: 'fmc-payment', doc: fmcPaymentJdmDoc };

// ----------------------------------------------------------------------------
// Connection entries — CMS-authored credential-free DISCRETE shape (NOT the
// seed DSNs). secretRef only; resilience authored in ms + retry.maxAttempts.
// ----------------------------------------------------------------------------

export const fmcPgConnection: ConnectionEntry = {
  key: 'fmc-pg',
  type: 'postgres',
  settings: {
    host: '127.0.0.1',
    port: 5432,
    database: 'fmc_utility',
    user: 'app',
    sslmode: 'disable',
    pool: { maxConns: 4, minConns: 1 },
  },
  secretRef: 'env:FMC_PG_PASSWORD',
  resilience: { timeoutMs: 2000, retry: { maxAttempts: 1 } },
};

export const shipRestConnection: ConnectionEntry = {
  key: 'ship-rest',
  type: 'rest',
  settings: {
    baseURL: 'http://127.0.0.1:0',
    headers: { Accept: 'application/json' },
  },
  secretRef: '',
  resilience: { timeoutMs: 2000, retry: { maxAttempts: 1 } },
};

export const ordersPgConnection: ConnectionEntry = {
  key: 'orders-pg',
  type: 'postgres',
  settings: {
    host: '127.0.0.1',
    port: 5432,
    database: 'orders',
    user: 'postgres',
    sslmode: 'disable',
    pool: { maxConns: 4, minConns: 1 },
  },
  secretRef: '',
  resilience: { timeoutMs: 2000, retry: { maxAttempts: 1 } },
};
