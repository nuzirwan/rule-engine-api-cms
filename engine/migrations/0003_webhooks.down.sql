-- 0003_webhooks.down.sql — tear down webhook schema (reverse order of up).
-- Drops in dependency order so FOREIGN KEY references resolve cleanly.
-- Note: active_pointers rows with object_type='webhook' are NOT deleted here
-- because they will fail FK validation when webhooks table is dropped anyway.

DROP INDEX IF EXISTS idx_webhook_versions_webhook;
DROP INDEX IF EXISTS idx_webhooks_env;
DROP INDEX IF EXISTS idx_webhooks_provider;
DROP INDEX IF EXISTS idx_webhook_logs_webhook_at;

DROP TABLE IF EXISTS webhook_logs CASCADE;
DROP TABLE IF EXISTS webhook_versions CASCADE;
DROP TABLE IF EXISTS webhooks CASCADE;

