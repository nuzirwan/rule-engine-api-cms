-- 0004_groups.down.sql — tear down groups schema (reverse order of up).
-- Drops in dependency order so FOREIGN KEY references resolve cleanly.

-- Remove flow_versions.group_id column (drops index implicitly)
DROP INDEX IF EXISTS idx_flow_versions_group;
ALTER TABLE flow_versions DROP COLUMN IF EXISTS group_id;

-- Drop audit tables (no FK dependencies)
DROP TABLE IF EXISTS flow_group_audit;
DROP TABLE IF EXISTS group_connections_audit;
DROP TABLE IF EXISTS group_audit;

-- Drop junction table (depends on groups)
DROP TABLE IF EXISTS group_connections;

-- Drop groups (including the seeded default row)
DROP TABLE IF EXISTS groups;
