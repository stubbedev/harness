-- +goose Up
-- +goose StatementBegin
-- Session-history queries filter on session_id and order by created_at;
-- one composite index serves both without a temp-B-tree sort.
CREATE INDEX IF NOT EXISTS idx_messages_session_created_at
ON messages (session_id, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_session_created_at;
-- +goose StatementEnd
