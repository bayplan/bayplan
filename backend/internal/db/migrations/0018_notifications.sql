-- +goose Up
-- +goose StatementBegin

-- Notifications: rules over the problems the panel already detects
-- (k8s/attention.go: CrashLoopBackOff, stuck rollouts, NotReady nodes…) and
-- over the loss of connection to a cluster, sent to channels (generic webhook,
-- Slack, Teams, email). It is not an alerting system over metrics (that is
-- Prometheus/Alertmanager): it warns about the same things as the "Needs
-- attention" screen, so one does not have to look at it.
--
-- The webhook URL is encrypted: in Slack and Teams the URL IS the secret
-- (whoever has it can post to the channel). Only the non-sensitive part goes
-- in config.
CREATE TABLE notification_channels (
    id               BIGSERIAL PRIMARY KEY,
    name             TEXT NOT NULL UNIQUE,
    type             TEXT NOT NULL CHECK (type IN ('webhook', 'slack', 'teams', 'email')),
    config           JSONB NOT NULL DEFAULT '{}'::jsonb,
    secret_encrypted BYTEA,
    key_id           TEXT,
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT notification_channels_key_id_present CHECK (secret_encrypted IS NULL OR key_id IS NOT NULL)
);

-- A rule filters problems (cluster, severity, kind, reason, namespace),
-- requires them to have been active for a minimum (min_duration_seconds: a
-- pod that restarts once is not an alert) and repeats at most every cooldown.
-- channel_ids is JSONB and not a join table: when a channel is deleted the
-- handler removes it from the rules (validated in the application, like
-- scope_ref).
-- cluster_id NULL = every cluster.
CREATE TABLE notification_rules (
    id                   BIGSERIAL PRIMARY KEY,
    name                 TEXT NOT NULL UNIQUE,
    enabled              BOOLEAN NOT NULL DEFAULT TRUE,
    cluster_id           BIGINT REFERENCES clusters(id) ON DELETE CASCADE,
    severities           JSONB NOT NULL DEFAULT '["error"]'::jsonb,
    kinds                JSONB NOT NULL DEFAULT '[]'::jsonb,
    reasons              JSONB NOT NULL DEFAULT '[]'::jsonb,
    namespaces           JSONB NOT NULL DEFAULT '[]'::jsonb,
    min_duration_seconds INT NOT NULL DEFAULT 300 CHECK (min_duration_seconds >= 0),
    cooldown_seconds     INT NOT NULL DEFAULT 3600 CHECK (cooldown_seconds >= 60),
    notify_resolved      BOOLEAN NOT NULL DEFAULT TRUE,
    channel_ids          JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Delivery history: what the UI shows as "latest deliveries" and the only
-- thing that explains why a notice did not arrive (HTTP 4xx from the webhook,
-- SMTP down). Names are denormalized so they survive the deletion of the
-- channel/rule. It is pruned at startup (see notify.Store.Prune); it is not an
-- audit trail.
CREATE TABLE notification_deliveries (
    id           BIGSERIAL PRIMARY KEY,
    ts           TIMESTAMPTZ NOT NULL DEFAULT now(),
    rule_id      BIGINT,
    rule_name    TEXT NOT NULL,
    channel_id   BIGINT,
    channel_name TEXT NOT NULL,
    cluster      TEXT NOT NULL DEFAULT '',
    issue_key    TEXT NOT NULL DEFAULT '',
    event        TEXT NOT NULL CHECK (event IN ('firing', 'resolved', 'test')),
    title        TEXT NOT NULL DEFAULT '',
    ok           BOOLEAN NOT NULL,
    error        TEXT
);
CREATE INDEX notification_deliveries_ts_idx ON notification_deliveries (ts DESC);

CREATE TRIGGER trg_audit_db_notification_channels
    AFTER INSERT OR UPDATE OR DELETE ON notification_channels
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();
CREATE TRIGGER trg_audit_db_notification_rules
    AFTER INSERT OR UPDATE OR DELETE ON notification_rules
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_rules;
DROP TABLE IF EXISTS notification_channels;
-- +goose StatementEnd
