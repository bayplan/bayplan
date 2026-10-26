-- +goose Up
-- +goose StatementBegin

-- Internal role. An enum instead of a catalog table: cheap to extend via
-- ALTER TYPE ... ADD VALUE, and avoids an extra JOIN on every permission check.
CREATE TYPE app_role AS ENUM ('admin', 'readonly');

-- Scope of a role assignment: global (every cluster), a specific cluster, or
-- a specific namespace within a cluster. The v1 UI only uses 'global', but the
-- schema supports fine granularity from the start so as not to require a
-- painful data migration later.
CREATE TYPE scope_type AS ENUM ('global', 'cluster', 'namespace');

CREATE TABLE users (
    id                   BIGSERIAL PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE,
    password_hash        TEXT,              -- NULL for SSO-only users
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    source               TEXT NOT NULL CHECK (source IN ('local', 'entra')),
    entra_oid            TEXT UNIQUE,       -- Entra ID object id, NULL if source=local
    disabled             BOOLEAN NOT NULL DEFAULT FALSE,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_local_has_password
        CHECK (source <> 'local' OR password_hash IS NOT NULL)
);

-- Server-side sessions (instantly revocable). Only the hash of the session
-- token is stored: the plaintext value exists only in the browser's httpOnly
-- cookie, never in the database.
CREATE TABLE sessions (
    token_hash   TEXT PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(), -- for inactivity expiration (sliding)
    expires_at   TIMESTAMPTZ NOT NULL,               -- maximum absolute expiration
    ip           INET,
    user_agent   TEXT,
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at) WHERE revoked_at IS NULL;

CREATE TABLE role_assignments (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       app_role NOT NULL,
    scope_type scope_type NOT NULL DEFAULT 'global',
    -- scope_ref: NULL if scope_type='global'; cluster id or "cluster_id/namespace"
    -- if scope_type='cluster'/'namespace'. Validated at the application level.
    scope_ref  TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT role_assignments_scope_ref_required
        CHECK (scope_type = 'global' OR scope_ref IS NOT NULL),
    UNIQUE (user_id, role, scope_type, scope_ref)
);

CREATE INDEX idx_role_assignments_user_id ON role_assignments(user_id);

-- Managed K8s clusters. The technical ServiceAccount token is stored
-- encrypted (AES-GCM); key_id identifies which encryption key version protects
-- it, so the master key can be rotated without downtime.
CREATE TABLE clusters (
    id                 BIGSERIAL PRIMARY KEY,
    display_name       TEXT NOT NULL UNIQUE, -- "PRE", "PRO", ...
    api_endpoint       TEXT NOT NULL,
    ca_cert            TEXT NOT NULL,
    sa_token_encrypted BYTEA NOT NULL,
    key_id             TEXT NOT NULL,
    enabled            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Entra ID group -> internal role mapping, with the same scope model as
-- role_assignments so a group can be mapped to a role only on a specific
-- cluster.
CREATE TABLE group_role_mappings (
    id         BIGSERIAL PRIMARY KEY,
    ad_group_id TEXT NOT NULL,
    role       app_role NOT NULL,
    scope_type scope_type NOT NULL DEFAULT 'global',
    scope_ref  TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT group_role_mappings_scope_ref_required
        CHECK (scope_type = 'global' OR scope_ref IS NOT NULL),
    UNIQUE (ad_group_id, role, scope_type, scope_ref)
);

-- Append-only audit trail of every mutating action (and of logins). See the
-- audit_log_immutable trigger below: not even the table owner can modify or
-- delete rows through the normal path.
CREATE TABLE audit_log (
    id         BIGSERIAL PRIMARY KEY,
    ts         TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id    BIGINT REFERENCES users(id), -- NULL if the actor is the system
    ip         INET,
    action     TEXT NOT NULL,      -- e.g. "login.local", "deployment.restart", "deployment.scale"
    cluster    TEXT,
    namespace  TEXT,
    resource   TEXT,
    result     TEXT NOT NULL,      -- "success" | "failure" | "denied"
    detail     JSONB
);

CREATE INDEX idx_audit_log_ts ON audit_log(ts);
CREATE INDEX idx_audit_log_user_id ON audit_log(user_id);

-- Defense in depth: blocks UPDATE/DELETE on audit_log at the database level,
-- regardless of the role or GRANT permissions the application connects with.
-- Retention/archiving (outside this trigger's scope) is handled with
-- partitioning by date + export once the retention period is agreed with the
-- business.
CREATE FUNCTION audit_log_immutable() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log es append-only: % no permitido', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_log_immutable
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_audit_log_immutable ON audit_log;
DROP FUNCTION IF EXISTS audit_log_immutable();
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS group_role_mappings;
DROP TABLE IF EXISTS clusters;
DROP TABLE IF EXISTS role_assignments;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
DROP TYPE IF EXISTS scope_type;
DROP TYPE IF EXISTS app_role;
-- +goose StatementEnd
