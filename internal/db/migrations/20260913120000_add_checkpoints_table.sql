-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS checkpoints (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL CHECK (session_id != ''),
    message_id TEXT NOT NULL CHECK (message_id != ''),
    commit_sha TEXT NOT NULL CHECK (commit_sha != ''),
    created_at INTEGER NOT NULL,  -- Unix timestamp in seconds
    UNIQUE(session_id, message_id),
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_session_id ON checkpoints (session_id);
CREATE INDEX IF NOT EXISTS idx_checkpoints_message_id ON checkpoints (message_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_checkpoints_message_id;
DROP INDEX IF EXISTS idx_checkpoints_session_id;
DROP TABLE IF EXISTS checkpoints;
-- +goose StatementEnd
