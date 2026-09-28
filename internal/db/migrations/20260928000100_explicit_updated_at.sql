-- +goose Up
-- updated_at is set by the statements that write a row, not by AFTER
-- UPDATE triggers. The triggers ran a second UPDATE for every write (every
-- streamed message flush wrote each row twice), and a statement's
-- RETURNING cannot see a trigger's change, so a saved session came back
-- with its old updated_at. The message count triggers now stamp the
-- session themselves, keeping a new message the activity that orders the
-- session list.
DROP TRIGGER IF EXISTS update_sessions_updated_at;
DROP TRIGGER IF EXISTS update_messages_updated_at;
DROP TRIGGER IF EXISTS update_session_message_count_on_insert;
DROP TRIGGER IF EXISTS update_session_message_count_on_delete;
DROP TRIGGER IF EXISTS update_session_message_count_on_visible;

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_insert
AFTER INSERT ON messages
WHEN new.visible = 1
BEGIN
UPDATE sessions SET
    message_count = message_count + 1,
    updated_at = strftime('%s', 'now')
WHERE id = new.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_delete
AFTER DELETE ON messages
WHEN old.visible = 1
BEGIN
UPDATE sessions SET
    message_count = message_count - 1,
    updated_at = strftime('%s', 'now')
WHERE id = old.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_visible
AFTER UPDATE OF visible ON messages
WHEN old.visible != new.visible
BEGIN
UPDATE sessions SET
    message_count = message_count + (new.visible - old.visible),
    updated_at = strftime('%s', 'now')
WHERE id = new.session_id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS update_session_message_count_on_insert;
DROP TRIGGER IF EXISTS update_session_message_count_on_delete;
DROP TRIGGER IF EXISTS update_session_message_count_on_visible;

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_insert
AFTER INSERT ON messages
WHEN new.visible = 1
BEGIN
UPDATE sessions SET message_count = message_count + 1
WHERE id = new.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_delete
AFTER DELETE ON messages
WHEN old.visible = 1
BEGIN
UPDATE sessions SET message_count = message_count - 1
WHERE id = old.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_visible
AFTER UPDATE OF visible ON messages
WHEN old.visible != new.visible
BEGIN
UPDATE sessions SET message_count = message_count + (new.visible - old.visible)
WHERE id = new.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS update_sessions_updated_at
AFTER UPDATE ON sessions
BEGIN
UPDATE sessions SET updated_at = strftime('%s', 'now')
WHERE id = new.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS update_messages_updated_at
AFTER UPDATE ON messages
BEGIN
UPDATE messages SET updated_at = strftime('%s', 'now')
WHERE id = new.id;
END;
-- +goose StatementEnd
