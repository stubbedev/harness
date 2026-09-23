-- +goose Up
-- +goose StatementBegin
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
AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, title, content)
    VALUES ('delete', old.rowid, old.title, old.content);
    INSERT INTO memories_fts (rowid, title, content)
    VALUES (new.rowid, new.title, new.content);
END;

-- Index memories saved before this migration existed.
INSERT INTO memories_fts (memories_fts) VALUES ('rebuild');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS memories_fts_update;
DROP TRIGGER IF EXISTS memories_fts_delete;
DROP TRIGGER IF EXISTS memories_fts_insert;
DROP TABLE IF EXISTS memories_fts;
-- +goose StatementEnd
