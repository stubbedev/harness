-- name: GetFile :one
SELECT *
FROM files
WHERE id = ? LIMIT 1;

-- name: GetFileByPathAndSession :one
SELECT *
FROM files
WHERE path = ? AND session_id = ?
ORDER BY version DESC, created_at DESC
LIMIT 1;

-- name: ListFilesBySessionWithChildren :many
SELECT *
FROM files
WHERE session_id = ?
   OR session_id IN (SELECT id FROM sessions WHERE parent_session_id = ?)
ORDER BY version ASC, created_at ASC;

-- name: CreateFile :one
-- A version the session already holds for the path inserts nothing and
-- returns no row, which callers read as a conflict.
INSERT INTO files (
    id,
    session_id,
    path,
    content,
    version,
    created_at,
    updated_at
) VALUES (
    ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now')
)
ON CONFLICT (path, session_id, version) DO NOTHING
RETURNING *;

-- name: CreateFileNextVersion :one
-- Inserts the path's next version, computed in the same statement so a
-- concurrent writer cannot take it in between.
INSERT INTO files (
    id,
    session_id,
    path,
    content,
    version,
    created_at,
    updated_at
)
SELECT
    sqlc.arg(id),
    sqlc.arg(session_id),
    sqlc.arg(path),
    sqlc.arg(content),
    COALESCE(MAX(f.version) + 1, 0),
    strftime('%s', 'now'),
    strftime('%s', 'now')
FROM files f
WHERE f.path = sqlc.arg(path)
RETURNING *;
