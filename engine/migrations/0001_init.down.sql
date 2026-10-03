-- 0001_init.down.sql — tear down the config store schema (reverse order of up).
-- Drops in dependency order so FOREIGN KEY references resolve cleanly.

DROP INDEX IF EXISTS idx_connver_key_ver;
DROP INDEX IF EXISTS idx_audit_object;
DROP INDEX IF EXISTS idx_flows_route;

DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS active_pointers;
DROP TABLE IF EXISTS flow_fixtures;
DROP TABLE IF EXISTS connection_versions;
DROP TABLE IF EXISTS jdm_versions;
DROP TABLE IF EXISTS flow_versions;
DROP TABLE IF EXISTS connections;
DROP TABLE IF EXISTS jdms;
DROP TABLE IF EXISTS flows;
