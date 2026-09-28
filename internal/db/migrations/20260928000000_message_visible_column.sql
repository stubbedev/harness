-- +goose Up
-- Whether a row counts toward its session's message_count is decided once,
-- in Go (message.CountsInTranscript), and stored here. The triggers only
-- read it. Before this the rule lived in the triggers as a JSON scan of the
-- parts, which only ran on insert and delete: a row inserted with no parts
-- (an assistant message before it streams) counted, and when it ended as
-- bookkeeping only and was deleted the delete trigger judged it hidden and
-- never took it back, so the count only ever grew.
ALTER TABLE messages ADD COLUMN visible INTEGER NOT NULL DEFAULT 1;

UPDATE messages SET visible = CASE
  WHEN is_summary_message != 0 THEN 0
  WHEN EXISTS (SELECT 1 FROM json_each(parts))
    AND NOT EXISTS (
      SELECT 1 FROM json_each(parts)
      WHERE json_extract(value, '$.type') NOT IN ('context_note', 'subagent_note', 'finish')
    ) THEN 0
  ELSE 1
END;

DROP TRIGGER IF EXISTS update_session_message_count_on_insert;
DROP TRIGGER IF EXISTS update_session_message_count_on_delete;

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

UPDATE sessions SET message_count = (
  SELECT count(*) FROM messages m
  WHERE m.session_id = sessions.id AND m.visible = 1
);

-- +goose Down
DROP TRIGGER IF EXISTS update_session_message_count_on_visible;
DROP TRIGGER IF EXISTS update_session_message_count_on_insert;
DROP TRIGGER IF EXISTS update_session_message_count_on_delete;

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_insert
AFTER INSERT ON messages
WHEN new.is_summary_message = 0
  AND NOT (
    EXISTS (SELECT 1 FROM json_each(new.parts))
    AND NOT EXISTS (
      SELECT 1 FROM json_each(new.parts)
      WHERE json_extract(value, '$.type') NOT IN ('context_note', 'subagent_note', 'finish')
    )
  )
BEGIN
UPDATE sessions SET message_count = message_count + 1
WHERE id = new.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_delete
AFTER DELETE ON messages
WHEN old.is_summary_message = 0
  AND NOT (
    EXISTS (SELECT 1 FROM json_each(old.parts))
    AND NOT EXISTS (
      SELECT 1 FROM json_each(old.parts)
      WHERE json_extract(value, '$.type') NOT IN ('context_note', 'subagent_note', 'finish')
    )
  )
BEGIN
UPDATE sessions SET message_count = message_count - 1
WHERE id = old.session_id;
END;
-- +goose StatementEnd

ALTER TABLE messages DROP COLUMN visible;
