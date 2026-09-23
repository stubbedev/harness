-- name: CreateMemory :one
INSERT INTO memories (
    id, category, title, content, pinned, embedding, created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now')
) RETURNING *;

-- name: GetMemory :one
SELECT * FROM memories WHERE id = ? LIMIT 1;

-- name: GetMemoryByTitle :one
SELECT * FROM memories WHERE lower(title) = lower(?) LIMIT 1;

-- name: ListMemories :many
-- The id tie-breaks: updated_at has whole-second resolution, so
-- memories saved in the same second would otherwise come back in
-- whatever order SQLite chose, and the index the model reads would
-- reshuffle between runs.
SELECT * FROM memories
ORDER BY pinned DESC, updated_at DESC, id ASC;

-- name: UpdateMemory :one
UPDATE memories SET
    category = ?,
    title = ?,
    content = ?,
    pinned = ?,
    embedding = ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?
RETURNING *;

-- name: TouchMemory :exec
UPDATE memories SET use_count = use_count + 1, last_used_at = strftime('%s', 'now')
WHERE id = ?;

-- name: DeleteMemory :execrows
DELETE FROM memories WHERE id = ?;

-- name: CountMemories :one
SELECT COUNT(*) FROM memories;

-- name: ReapMemories :execrows
DELETE FROM memories
WHERE pinned = 0 AND id NOT IN (
    SELECT id FROM memories
    ORDER BY pinned DESC, use_count DESC, last_used_at DESC,
             updated_at DESC, rowid DESC, id ASC
    LIMIT ?
);
