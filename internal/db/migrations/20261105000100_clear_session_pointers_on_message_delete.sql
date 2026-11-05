-- +goose Up
-- +goose StatementBegin
-- A session's summary, compaction boundary and aged mark point at
-- messages. Deleting a message clears every pointer to it in the same
-- statement, so no delete path can leave one dangling. A compaction
-- whose boundary goes stands for conversation that is gone, so its
-- summary and aged mark go with it.
CREATE TRIGGER IF NOT EXISTS clear_session_pointers_on_message_delete
AFTER DELETE ON messages
BEGIN
UPDATE sessions SET
    summary_message_id = CASE WHEN summary_message_id = old.id THEN NULL ELSE summary_message_id END,
    compaction_summary = CASE WHEN compaction_boundary_id = old.id THEN NULL ELSE compaction_summary END,
    compaction_aged_id = CASE
        WHEN compaction_boundary_id = old.id OR compaction_aged_id = old.id THEN NULL
        ELSE compaction_aged_id
    END,
    compaction_boundary_id = CASE WHEN compaction_boundary_id = old.id THEN NULL ELSE compaction_boundary_id END
WHERE id = old.session_id
  AND old.id IN (summary_message_id, compaction_boundary_id, compaction_aged_id);
END;

-- Clear the pointers earlier deletes left dangling.
UPDATE sessions SET
    compaction_summary = NULL,
    compaction_boundary_id = NULL,
    compaction_aged_id = NULL
WHERE compaction_boundary_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM messages WHERE messages.id = sessions.compaction_boundary_id);
UPDATE sessions SET compaction_aged_id = NULL
WHERE compaction_aged_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM messages WHERE messages.id = sessions.compaction_aged_id);
UPDATE sessions SET summary_message_id = NULL
WHERE summary_message_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM messages WHERE messages.id = sessions.summary_message_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS clear_session_pointers_on_message_delete;
-- +goose StatementEnd
