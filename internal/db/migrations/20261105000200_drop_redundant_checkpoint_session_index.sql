-- +goose Up
-- +goose StatementBegin
-- UNIQUE(session_id, message_id) has session_id as its leftmost column,
-- so its index serves every lookup by session this one did.
DROP INDEX IF EXISTS idx_checkpoints_session_id;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_checkpoints_session_id ON checkpoints (session_id);
-- +goose StatementEnd
