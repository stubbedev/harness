-- name: CreateCheckpoint :one
INSERT INTO checkpoints (
    id,
    session_id,
    message_id,
    commit_sha,
    created_at
) VALUES (
    ?, ?, ?, ?, strftime('%s', 'now')
)
ON CONFLICT(session_id, message_id) DO UPDATE SET
    commit_sha = excluded.commit_sha
RETURNING *;

-- name: GetCheckpointByMessage :one
SELECT *
FROM checkpoints
WHERE message_id = ? LIMIT 1;

-- name: ListCheckpointsBySession :many
SELECT *
FROM checkpoints
WHERE session_id = ?
ORDER BY created_at ASC;

-- name: DeleteSessionCheckpoints :exec
DELETE FROM checkpoints
WHERE session_id = ?;
