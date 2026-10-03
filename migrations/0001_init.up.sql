-- 0001_init.up.sql — engine-owned config store schema (Slice D §1).
--
-- One physical database per environment (ADR-006): rows carry no `env` column,
-- the database IS the environment. Immutable *_versions rows are append-only
-- (never UPDATE/DELETE after insert — [[versioning-and-compatibility]]). The
-- only mutable "what runs now" state is active_pointers. Every publish/rollback/
-- promote appends an audit_log row in the same transaction as the state change.
-- Secrets are never stored: a connection version holds secret_ref only
-- ([[config-and-secrets]]). Parameterized queries only in the store code.

-- ---- object identities (stable; metadata only, no body) ----
CREATE TABLE flows (
    id          text PRIMARY KEY,              -- stable flow id, e.g. "orders"
    method      text NOT NULL,                 -- "GET"
    path        text NOT NULL,                 -- "/orders/{id}" (Go 1.22 ServeMux pattern)
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL,
    UNIQUE (method, path)                       -- one flow owns a route
);

CREATE TABLE jdms (
    id          text PRIMARY KEY,              -- stable JDM id referenced by decision nodes
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL
);

CREATE TABLE connections (
    key         text PRIMARY KEY,              -- stable connection key referenced by action nodes
    type        text NOT NULL,                 -- "postgres" | "valkey" | "rest"
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL
);

-- ---- immutable versions (append-only; never UPDATE/DELETE) ----
CREATE TABLE flow_versions (
    flow_id     text NOT NULL REFERENCES flows(id),
    version     int  NOT NULL,                  -- monotonic per flow, starts at 1
    tree        jsonb NOT NULL,                 -- the Node tree (Slice A schema)
    checksum    text NOT NULL,                  -- sha256 of canonical(tree) — contract test anchor
    validated   boolean NOT NULL DEFAULT false, -- true only after validate+fixtures passed (AC-13)
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL,
    PRIMARY KEY (flow_id, version)
);

CREATE TABLE jdm_versions (
    jdm_id      text NOT NULL REFERENCES jdms(id),
    version     int  NOT NULL,
    jdm         jsonb NOT NULL,                 -- GoRules JDM graph
    checksum    text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL,
    PRIMARY KEY (jdm_id, version)
);

CREATE TABLE connection_versions (
    conn_key    text NOT NULL REFERENCES connections(key),
    version     int  NOT NULL,
    settings    jsonb NOT NULL,                 -- host/port/baseURL/pool...  (NO secret value)
    secret_ref  text,                           -- opaque pointer resolved by Slice B registry
    resilience  jsonb NOT NULL,                 -- timeout/retry/breaker policy
    checksum    text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    created_by  text NOT NULL,
    PRIMARY KEY (conn_key, version)
);

-- ---- fixtures travel WITH a specific flow version (§6c, AC-12) ----
CREATE TABLE flow_fixtures (
    flow_id     text NOT NULL,
    version     int  NOT NULL,
    name        text NOT NULL,
    body        jsonb NOT NULL,                 -- {input, want/expect ...}
    PRIMARY KEY (flow_id, version, name),
    FOREIGN KEY (flow_id, version) REFERENCES flow_versions(flow_id, version)
);

-- ---- the only mutable "what runs now" state: pointer per object ----
CREATE TABLE active_pointers (
    object_type text NOT NULL,                  -- "flow" | "jdm" | "connection"
    object_id   text NOT NULL,                  -- flow_id | jdm_id | conn_key
    version     int  NOT NULL,                  -- currently active version
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  text NOT NULL,
    PRIMARY KEY (object_type, object_id)
);

-- ---- append-only audit of every publish/rollback/promote (§6c, AC-9..AC-12) ----
CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    actor       text NOT NULL,
    action      text NOT NULL,                  -- "create_version"|"publish"|"rollback"|"promote"
    object_type text NOT NULL,
    object_id   text NOT NULL,
    from_version int,                           -- pointer before (null for create)
    to_version   int,                           -- pointer after / version created
    reason      text
);

-- ---- indexes for the hot resolve path and the admin reads ----
-- Hot path: (method, path) -> flow identity, then pointer lookup, then version body.
CREATE INDEX idx_flows_route     ON flows (method, path);                     -- route match
CREATE INDEX idx_audit_object    ON audit_log (object_type, object_id, at DESC);
CREATE INDEX idx_connver_key_ver ON connection_versions (conn_key, version DESC);
-- active_pointers PK (object_type, object_id) already serves the pointer lookup.
-- version body PKs already serve point lookups (flow_id,version)/(jdm_id,version).
