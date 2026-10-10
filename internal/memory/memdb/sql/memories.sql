-- name: CreateMemory :one
INSERT INTO memories (
    scope, repo_key, id, category, title, content, pinned, embedding,
    created_at, updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now')
) RETURNING *;

-- name: GetMemory :one
SELECT * FROM memories
WHERE scope = ? AND repo_key = ? AND id = ?
LIMIT 1;

-- name: GetMemoryByTitle :one
SELECT * FROM memories
WHERE scope = ? AND repo_key = ? AND lower(title) = lower(sqlc.arg(title))
LIMIT 1;

-- name: ListVisibleMemories :many
-- The memories one workspace sees: every global memory and those of its
-- repository, global first. The id tie-breaks: updated_at has
-- whole-second resolution, so memories saved in the same second would
-- otherwise come back in whatever order SQLite chose, and the index the
-- model reads would reshuffle between runs.
SELECT * FROM memories
WHERE scope = 'global' OR (scope = 'repo' AND repo_key = sqlc.arg(repo_key))
ORDER BY scope = 'repo', pinned DESC, updated_at DESC, id ASC;

-- name: UpdateMemory :one
UPDATE memories SET
    category = ?,
    title = ?,
    content = ?,
    pinned = ?,
    embedding = ?,
    updated_at = strftime('%s', 'now')
WHERE scope = ? AND repo_key = ? AND id = ?
RETURNING *;

-- name: TouchMemory :exec
UPDATE memories SET use_count = use_count + 1, last_used_at = strftime('%s', 'now')
WHERE scope = ? AND repo_key = ? AND id = ?;

-- name: DeleteMemory :execrows
DELETE FROM memories WHERE scope = ? AND repo_key = ? AND id = ?;

-- name: ReapMemories :execrows
-- Keeps the max_memories most useful memories of one scope (global, or
-- one repository) and deletes the unpinned rest.
DELETE FROM memories
WHERE memories.scope = sqlc.arg(scope) AND memories.repo_key = sqlc.arg(repo_key)
  AND memories.pinned = 0
  AND memories.rowid NOT IN (
    SELECT kept.rowid FROM memories AS kept
    WHERE kept.scope = sqlc.arg(scope) AND kept.repo_key = sqlc.arg(repo_key)
    ORDER BY kept.pinned DESC, kept.use_count DESC, kept.last_used_at DESC,
             kept.updated_at DESC, kept.rowid DESC, kept.id ASC
    LIMIT sqlc.arg(keep)
);

-- name: ImportMemory :exec
-- Writes a memory carried over from a per-workspace database with the
-- usage and timestamps it had there.
INSERT INTO memories (
    scope, repo_key, id, category, title, content, pinned, use_count,
    last_used_at, embedding, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (scope, repo_key, id) DO UPDATE SET
    category = excluded.category,
    title = excluded.title,
    content = excluded.content,
    pinned = excluded.pinned,
    use_count = excluded.use_count,
    last_used_at = excluded.last_used_at,
    embedding = excluded.embedding,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at;

-- name: ListLegacyImports :many
SELECT source FROM legacy_imports ORDER BY source;

-- name: RecordLegacyImport :exec
INSERT INTO legacy_imports (source, memories, imported_at)
VALUES (?, ?, strftime('%s', 'now'))
ON CONFLICT (source) DO NOTHING;
