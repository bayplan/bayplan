-- +goose Up
-- +goose StatementBegin

-- Per-person daily use of the AI provider behind "Explain" (internal/ai). The
-- limit is checked and incremented in one statement before calling the
-- provider, so two simultaneous clicks cannot both slip under it, and if this
-- table cannot be read the call is refused: an unreadable counter must not
-- turn into unlimited spending. It is not audit (ai.explain is audited in
-- audit_log); old days are pruned at startup.
CREATE TABLE ai_usage (
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day           DATE   NOT NULL,
    calls         INT    NOT NULL DEFAULT 0,
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE ai_usage;
-- +goose StatementEnd
