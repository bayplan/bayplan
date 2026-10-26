-- +goose Up
-- +goose StatementBegin

-- Notification engine state across restarts: per problem and rule, when it
-- was first seen, when it was last notified and whether it was actually
-- notified. Before, it lived only in memory and a restart lost two things: the
-- "resolved" of what got fixed while the server was down (it was never sent)
-- and the cooldown (what had already been notified was notified again as soon
-- as the first tick passed). It is not history (that is
-- notification_deliveries): one row per active problem, deleted on resolution.
CREATE TABLE notification_state (
    issue_key  TEXT NOT NULL,
    rule_id    BIGINT NOT NULL REFERENCES notification_rules(id) ON DELETE CASCADE,
    cluster_id BIGINT NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL,
    last_sent  TIMESTAMPTZ NOT NULL,
    sent       BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (issue_key, rule_id)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS notification_state;
-- +goose StatementEnd
