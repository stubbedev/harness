-- +goose Up
-- +goose StatementBegin
-- Startup recovery looks for the assistant messages that never finished.
-- Only a turn in flight or one a dead process left behind is unfinished,
-- so the partial index stays a handful of rows however long the history
-- grows.
CREATE INDEX IF NOT EXISTS idx_messages_unfinished_assistant
ON messages (session_id, created_at) WHERE role = 'assistant' AND finished_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_messages_unfinished_assistant;
-- +goose StatementEnd
