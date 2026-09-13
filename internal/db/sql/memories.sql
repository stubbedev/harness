-- name: CreateMemory :one
INSERT INTO memories (
    id, category, title, content, pinned, created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now')
) RETURNING *;

-- name: GetMemory :one
SELECT * FROM memories WHERE id = ? LIMIT 1;

-- name: GetMemoryByTitle :one
SELECT * FROM memories WHERE lower(title) = lower(?) LIMIT 1;

-- name: ListMemories :many
SELECT * FROM memories
ORDER BY pinned DESC, updated_at DESC;

-- name: SearchMemories :many
SELECT * FROM memories
WHERE title LIKE '%' || ? || '%' OR content LIKE '%' || ? || '%'
ORDER BY pinned DESC, updated_at DESC;

-- name: UpdateMemory :one
UPDATE memories SET
    category = ?,
    title = ?,
    content = ?,
    pinned = ?,
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
             updated_at DESC, rowid DESC
    LIMIT ?
);
