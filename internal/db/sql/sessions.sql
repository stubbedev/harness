-- name: CreateSession :one
INSERT INTO sessions (
    id,
    parent_session_id,
    title,
    message_count,
    prompt_tokens,
    completion_tokens,
    cost,
    summary_message_id,
    updated_at,
    created_at
) VALUES (
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    ?,
    null,
    strftime('%s', 'now'),
    strftime('%s', 'now')
) RETURNING *;

-- name: GetSessionByID :one
SELECT *
FROM sessions
WHERE id = ? LIMIT 1;

-- name: GetLastSession :one
SELECT *
FROM sessions
WHERE parent_session_id IS NULL
ORDER BY updated_at DESC
LIMIT 1;

-- name: ListSessions :many
SELECT *
FROM sessions
WHERE parent_session_id is NULL
ORDER BY updated_at DESC;

-- name: ListChildSessions :many
SELECT *
FROM sessions
WHERE parent_session_id = ?
ORDER BY updated_at DESC;

-- name: UpdateSession :one
UPDATE sessions
SET
    title = ?,
    prompt_tokens = ?,
    completion_tokens = ?,
    summary_message_id = ?,
    cost = ?,
    todos = ?,
    compaction_summary = ?,
    compaction_boundary_id = ?,
    compaction_aged_id = ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?
RETURNING *;

-- name: RecordSessionUsage :one
-- Adds a step's cost and replaces whichever token counter the step
-- measured (NULL leaves a counter alone), in one statement so a concurrent
-- rename or sub-agent cost roll-up is never overwritten.
UPDATE sessions
SET
    cost = cost + sqlc.arg(cost_delta),
    prompt_tokens = COALESCE(sqlc.narg(prompt_tokens), prompt_tokens),
    completion_tokens = COALESCE(sqlc.narg(completion_tokens), completion_tokens),
    updated_at = strftime('%s', 'now')
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateSessionCompaction :one
-- Writes the compaction pointers, and the token counters when given,
-- leaving title, cost and todos to their own writers.
UPDATE sessions
SET
    compaction_summary = sqlc.narg(compaction_summary),
    compaction_boundary_id = sqlc.narg(compaction_boundary_id),
    compaction_aged_id = sqlc.narg(compaction_aged_id),
    summary_message_id = sqlc.narg(summary_message_id),
    prompt_tokens = COALESCE(sqlc.narg(prompt_tokens), prompt_tokens),
    completion_tokens = COALESCE(sqlc.narg(completion_tokens), completion_tokens),
    updated_at = strftime('%s', 'now')
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateSessionTitleAndUsage :exec
UPDATE sessions
SET
    title = ?,
    prompt_tokens = prompt_tokens + ?,
    completion_tokens = completion_tokens + ?,
    cost = cost + ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?;

-- name: AddSessionCost :execrows
UPDATE sessions
SET
    cost = cost + ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?;


-- name: RenameSession :exec
UPDATE sessions
SET
    title = ?
WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions
WHERE id = ?;
-- name: UpdateSessionGoal :exec
UPDATE sessions
SET goal = ?
WHERE id = ?;
