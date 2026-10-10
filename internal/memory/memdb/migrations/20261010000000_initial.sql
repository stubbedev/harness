-- +goose Up
-- +goose StatementBegin
-- The machine-wide memory store, shared by every workspace and every
-- harness process on the machine. A memory is global (repo_key empty)
-- or belongs to one repository, identified by the key derived from its
-- upstream, so clones and worktrees of one repository share their
-- memories. The same id in two scopes is two memories.
CREATE TABLE IF NOT EXISTS memories (
    scope TEXT NOT NULL CHECK (scope IN ('global', 'repo')),
    repo_key TEXT NOT NULL,
    id TEXT NOT NULL CHECK (id != ''),
    category TEXT NOT NULL CHECK (category != ''),
    title TEXT NOT NULL CHECK (title != ''),
    content TEXT NOT NULL CHECK (content != ''),
    pinned INTEGER NOT NULL DEFAULT 0,
    use_count INTEGER NOT NULL DEFAULT 0,
    last_used_at INTEGER NOT NULL DEFAULT 0,  -- Last read, unix secs
    embedding BLOB,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (scope, repo_key, id),
    CHECK ((scope = 'global') = (repo_key = ''))
);

CREATE INDEX IF NOT EXISTS idx_memories_last_used_at ON memories (last_used_at);

-- External-content FTS5 index over memories: the index stores no text,
-- it maps rowids to the searchable token stream, and the triggers keep
-- it in sync with the base table. porter stems so "running" matches
-- "runs"; unicode61 splits on non-alphanumerics.
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    title,
    content,
    tokenize = 'porter unicode61',
    content = 'memories',
    content_rowid = 'rowid'
);

CREATE TRIGGER IF NOT EXISTS memories_fts_insert
AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts (rowid, title, content)
    VALUES (new.rowid, new.title, new.content);
END;

CREATE TRIGGER IF NOT EXISTS memories_fts_delete
AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, title, content)
    VALUES ('delete', old.rowid, old.title, old.content);
END;

CREATE TRIGGER IF NOT EXISTS memories_fts_update
AFTER UPDATE OF title, content ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, title, content)
    VALUES ('delete', old.rowid, old.title, old.content);
    INSERT INTO memories_fts (rowid, title, content)
    VALUES (new.rowid, new.title, new.content);
END;

-- The per-workspace databases whose memories were imported into this
-- store, so each is imported once. source is the workspace data
-- directory's name under the global data root, or the absolute path of
-- a data directory that lives elsewhere.
CREATE TABLE IF NOT EXISTS legacy_imports (
    source TEXT NOT NULL PRIMARY KEY,
    memories INTEGER NOT NULL,
    imported_at INTEGER NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS legacy_imports;
DROP TRIGGER IF EXISTS memories_fts_update;
DROP TRIGGER IF EXISTS memories_fts_delete;
DROP TRIGGER IF EXISTS memories_fts_insert;
DROP TABLE IF EXISTS memories_fts;
DROP INDEX IF EXISTS idx_memories_last_used_at;
DROP TABLE IF EXISTS memories;
-- +goose StatementEnd
