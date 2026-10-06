-- 0003_webhooks.up.sql — webhook configuration schema for external event ingestion.
--
-- Webhooks follow the same identity + immutable versions + active_pointers model
-- as flows/JDMs/connections. Each webhook binds an external provider (Stripe,
-- GitHub, etc.) to a flow via a JSONPath mapping and optional filter predicates.
-- Secrets are stored via secret_ref (never inline), resolved by the SecretProvider.
--
-- webhook_logs is a dedicated audit table for webhook invocations, capturing
-- request metadata and outcome for debugging and observability.

-- ---- webhook identity (stable; metadata only, no body) ----
CREATE TABLE webhooks (
    id          text PRIMARY KEY,              -- stable webhook id, e.g. "stripe-payment"
    name        text NOT NULL,                 -- human-readable display name
    provider    text NOT NULL,                 -- "stripe" | "github" | "generic" ...
    flow_id     text NOT NULL REFERENCES flows(id), -- flow to trigger on match
    env         text NOT NULL,                 -- environment this webhook belongs to
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL
);

-- ---- immutable webhook versions (append-only; never UPDATE/DELETE) ----
CREATE TABLE webhook_versions (
    webhook_id  text NOT NULL REFERENCES webhooks(id),
    version     int  NOT NULL,                  -- monotonic per webhook, starts at 1
    secret_ref  text,                           -- opaque pointer to secret (resolved by SecretProvider)
    mapping     jsonb NOT NULL DEFAULT '{}',    -- JSONPath mapping: {"flowField": "$.payload.field"}
    filter      jsonb NOT NULL DEFAULT '{}',    -- filter predicates: {"event_type": ["payment.success"]}
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL,
    PRIMARY KEY (webhook_id, version)
);

-- ---- webhook invocation logs (audit/debug, not immutable) ----
CREATE TABLE webhook_logs (
    id              bigserial PRIMARY KEY,
    webhook_id      text NOT NULL REFERENCES webhooks(id),
    at              timestamptz NOT NULL DEFAULT now(),
    provider        text NOT NULL,              -- provider that sent the event
    event_type      text,                       -- provider-specific event type (e.g. "payment.success")
    flow_triggered  text,                       -- flow_id if flow was triggered, null if filtered
    status          text NOT NULL,              -- "accepted" | "filtered" | "rejected" | "error"
    request_headers jsonb,                      -- relevant headers (signature, content-type, etc.)
    payload_hash    text,                       -- sha256 of raw payload for dedup/audit
    error           text                        -- error message if status=rejected/error
);

-- ---- indexes for webhook operations ----
-- Hot path: lookup active webhook by id
CREATE INDEX idx_webhook_logs_webhook_at ON webhook_logs (webhook_id, at DESC);
-- List webhooks by provider/env
CREATE INDEX idx_webhooks_provider ON webhooks (provider);
CREATE INDEX idx_webhooks_env ON webhooks (env);
-- Version lookup
CREATE INDEX idx_webhook_versions_webhook ON webhook_versions (webhook_id, version DESC);

-- active_pointers already supports arbitrary object_type; add 'webhook' values via INSERT.
-- No schema change needed — the (object_type, object_id) PK accommodates new types.

