-- 0004_groups.up.sql — groups schema for dynamic grouped workers (Phase 1).
--
-- Groups logically partition flows and connections for resource isolation. Each
-- group controls its own scaling policy, resource limits, and connection pool.
-- The default group (id='default') cannot be deleted and catches ungrouped flows.
--
-- Audit tables capture full state for reliable rollback. group_connections is a
-- junction table (not JSONB array) so the loader can join-resolve without parsing.
-- flow_versions.group_id uses ON DELETE RESTRICT: remove flows from a group
-- before deleting it.

-- ---- groups: the core group entity with scaling and resource config ----
CREATE TABLE groups (
    id                        VARCHAR(64) PRIMARY KEY,
    name                      VARCHAR(128) NOT NULL,
    description               TEXT,
    version                   INT NOT NULL DEFAULT 1,   -- bumped on every change for hot-reload detection
    scaling_mode              VARCHAR(16) NOT NULL DEFAULT 'dynamic',
    min_replicas              INT NOT NULL DEFAULT 0,
    max_replicas              INT NOT NULL DEFAULT 10,
    scale_down_delay_seconds  INT NOT NULL DEFAULT 300,
    startup_timeout_seconds   INT NOT NULL DEFAULT 30,
    cpu_request               VARCHAR(16) DEFAULT '100m',
    cpu_limit                 VARCHAR(16) DEFAULT '500m',
    memory_request            VARCHAR(16) DEFAULT '128Mi',
    memory_limit              VARCHAR(16) DEFAULT '512Mi',
    enabled                   BOOLEAN NOT NULL DEFAULT true,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_scaling_mode CHECK (scaling_mode IN ('static', 'dynamic', 'ephemeral')),
    CONSTRAINT chk_replicas_range CHECK (min_replicas >= 0 AND max_replicas >= min_replicas),
    CONSTRAINT chk_static_min CHECK (scaling_mode != 'static' OR min_replicas >= 1)
);

-- ---- group_connections: junction table for group->connection mapping ----
CREATE TABLE group_connections (
    group_id       VARCHAR(64) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    connection_key VARCHAR(64) NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, connection_key)
);

CREATE INDEX idx_group_connections_group ON group_connections (group_id);

-- ---- group_audit: full state audit for groups ----
CREATE TABLE group_audit (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id     VARCHAR(64) NOT NULL,
    operation    VARCHAR(16) NOT NULL,
    old_data     JSONB,
    new_data     JSONB,
    changed_by   VARCHAR(128) NOT NULL,
    changed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason       TEXT,
    request_id   VARCHAR(64),
    CONSTRAINT chk_group_audit_op CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE', 'ROLLBACK', 'AUTO_ROLLBACK', 'RESTORE'))
);

CREATE INDEX idx_group_audit_group    ON group_audit (group_id);
CREATE INDEX idx_group_audit_changed  ON group_audit (changed_at DESC);

-- ---- group_connections_audit: audit for connection assignments ----
CREATE TABLE group_connections_audit (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id        VARCHAR(64) NOT NULL,
    operation       VARCHAR(16) NOT NULL,
    connection_key  VARCHAR(64),          -- NULL for BULK_REPLACE
    old_connections TEXT[],               -- previous set (for BULK_REPLACE)
    new_connections TEXT[],               -- new set (for BULK_REPLACE)
    changed_by      VARCHAR(128) NOT NULL,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason          TEXT,
    request_id      VARCHAR(64),
    CONSTRAINT chk_conn_audit_op CHECK (operation IN ('INSERT', 'DELETE', 'BULK_REPLACE'))
);

CREATE INDEX idx_group_conn_audit_group   ON group_connections_audit (group_id);
CREATE INDEX idx_group_conn_audit_changed ON group_connections_audit (changed_at DESC);

-- ---- flow_group_audit: audit for flow->group reassignments ----
CREATE TABLE flow_group_audit (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    flow_id      VARCHAR(64) NOT NULL,
    old_group    VARCHAR(64),             -- NULL if previously ungrouped
    new_group    VARCHAR(64),             -- NULL if removing from group
    changed_by   VARCHAR(128) NOT NULL,
    changed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason       TEXT,
    request_id   VARCHAR(64)
);

CREATE INDEX idx_flow_group_audit_flow    ON flow_group_audit (flow_id);
CREATE INDEX idx_flow_group_audit_changed ON flow_group_audit (changed_at DESC);

-- ---- flow_versions: add group_id FK for flow->group assignment ----
ALTER TABLE flow_versions
    ADD COLUMN group_id VARCHAR(64) REFERENCES groups(id) ON DELETE RESTRICT;

CREATE INDEX idx_flow_versions_group ON flow_versions (group_id);

-- ---- seed the default group (cannot be deleted) ----
INSERT INTO groups (id, name, description, scaling_mode, enabled)
VALUES ('default', 'Default', 'Default group for ungrouped flows', 'dynamic', true);
