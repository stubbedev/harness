-- +goose Up
-- +goose StatementBegin
-- The session list, the last-session lookup, the child listing, the
-- session-tree walk and the files-with-children query all filter on
-- parent_session_id and order by updated_at; id breaks updated_at ties.
CREATE INDEX IF NOT EXISTS idx_sessions_parent_updated_at
ON sessions (parent_session_id, updated_at, id);

-- Prompt history and the user-message listing read only user messages,
-- newest first, per session or across every session. Partial indexes keep
-- the scan to user rows instead of walking every assistant and tool row.
CREATE INDEX IF NOT EXISTS idx_messages_user_session_created_at
ON messages (session_id, created_at) WHERE role = 'user';
CREATE INDEX IF NOT EXISTS idx_messages_user_created_at
ON messages (created_at) WHERE role = 'user';

-- idx_messages_session_created_at has session_id as its leftmost column,
-- so it serves every lookup this index did.
DROP INDEX IF EXISTS idx_messages_session_id;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_messages_session_id ON messages (session_id);
DROP INDEX IF EXISTS idx_messages_user_created_at;
DROP INDEX IF EXISTS idx_messages_user_session_created_at;
DROP INDEX IF EXISTS idx_sessions_parent_updated_at;
-- +goose StatementEnd
