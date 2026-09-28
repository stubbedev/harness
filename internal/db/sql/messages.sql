-- name: GetMessage :one
SELECT *
FROM messages
WHERE id = ? LIMIT 1;

-- name: ListMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ?
-- created_at has whole-second resolution and a prompt and its reply
-- often share a second; rowid is insertion order and breaks the tie.
ORDER BY created_at ASC, rowid ASC;

-- name: CreateMessage :one
INSERT INTO messages (
    id,
    session_id,
    role,
    parts,
    model,
    provider,
    is_summary_message,
    visible,
    created_at,
    updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now')
)
RETURNING *;

-- name: UpdateMessage :exec
UPDATE messages
SET
    parts = ?,
    prism_model_id = ?,
    prism_model_name = ?,
    prism_hypercredit_savings = ?,
    prism_dollar_savings = ?,
    finished_at = ?,
    visible = ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?;


-- name: DeleteMessage :exec
DELETE FROM messages
WHERE id = ?;

-- name: ListUserMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ? AND role = 'user'
ORDER BY created_at DESC, rowid DESC;

-- name: GetLastAssistantMessageBySession :one
-- Only the provider and model of the last assistant message are read;
-- skip fetching the parts blob and the rest of the row.
SELECT provider, model
FROM messages
WHERE session_id = ? AND role = 'assistant' AND is_summary_message = 0
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: ListMessagesBySessionFrom :many
SELECT m.*
FROM messages m
WHERE m.session_id = ?
  AND m.created_at >= (SELECT b.created_at FROM messages b WHERE b.id = ?)
ORDER BY m.created_at ASC, m.rowid ASC;

-- name: DeleteMessagesFrom :many
-- Deletes the anchor message and every message after it in the session,
-- in the order ListMessagesBySession reads them, as one statement.
DELETE FROM messages
WHERE messages.session_id = sqlc.arg(session_id)
  AND (messages.created_at, messages.rowid) >= (
    SELECT a.created_at, a.rowid FROM messages a
    WHERE a.id = sqlc.arg(anchor_id) AND a.session_id = sqlc.arg(session_id)
  )
RETURNING *;

-- name: ListPromptHistory :many
-- The prompt-history entries of user messages, newest message first: each
-- text part as written and each shell command prefixed with "!", read
-- straight from the stored parts so no binary attachment or context note
-- is decoded. An empty session_id reads every session.
SELECT
    CAST(json_extract(p.value, '$.type') AS TEXT) AS part_type,
    CAST(COALESCE(json_extract(p.value, '$.data.text'), json_extract(p.value, '$.data.command'), '') AS TEXT) AS entry
FROM messages m, json_each(m.parts) p
WHERE m.role = 'user'
  AND (sqlc.arg(session_id) = '' OR m.session_id = sqlc.arg(session_id))
  AND json_extract(p.value, '$.type') IN ('text', 'shell_command')
ORDER BY m.created_at DESC, m.rowid DESC, p.key ASC;
