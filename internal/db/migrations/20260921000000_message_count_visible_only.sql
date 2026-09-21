-- +goose Up
-- A session's message_count is what the reader sees in the transcript.
-- Rows the harness writes for the model alone - context notes and
-- sub-agent report-backs, whose every part is one of those or the
-- Finish bookkeeping part - and the summaries older compactions stored
-- as messages are not conversation and do not count. A row inserted
-- with no parts yet (an assistant message before it streams) counts.
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
UPDATE sessions SET
    message_count = message_count + 1
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
UPDATE sessions SET
    message_count = message_count - 1
WHERE id = old.session_id;
END;
-- +goose StatementEnd

-- Recount every session by the same rule so sessions that already hold
-- hidden rows show the right number.
UPDATE sessions SET message_count = (
  SELECT count(*) FROM messages m
  WHERE m.session_id = sessions.id
    AND m.is_summary_message = 0
    AND NOT (
      EXISTS (SELECT 1 FROM json_each(m.parts))
      AND NOT EXISTS (
        SELECT 1 FROM json_each(m.parts)
        WHERE json_extract(value, '$.type') NOT IN ('context_note', 'subagent_note', 'finish')
      )
    )
);

-- +goose Down
DROP TRIGGER IF EXISTS update_session_message_count_on_insert;
DROP TRIGGER IF EXISTS update_session_message_count_on_delete;

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_insert
AFTER INSERT ON messages
BEGIN
UPDATE sessions SET
    message_count = message_count + 1
WHERE id = new.session_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER update_session_message_count_on_delete
AFTER DELETE ON messages
BEGIN
UPDATE sessions SET
    message_count = message_count - 1
WHERE id = old.session_id;
END;
-- +goose StatementEnd

UPDATE sessions SET message_count = (
  SELECT count(*) FROM messages m WHERE m.session_id = sessions.id
);
