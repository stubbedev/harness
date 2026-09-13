-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS memories (
    id TEXT NOT NULL CHECK (id != ''),
    category TEXT NOT NULL CHECK (category != ''),
    title TEXT NOT NULL CHECK (title != ''),
    content TEXT NOT NULL CHECK (content != ''),
    pinned INTEGER NOT NULL DEFAULT 0,
    use_count INTEGER NOT NULL DEFAULT 0,
    last_used_at INTEGER NOT NULL DEFAULT 0,  -- Last read, unix secs
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (id)
);

CREATE INDEX IF NOT EXISTS idx_memories_category ON memories (category);
CREATE INDEX IF NOT EXISTS idx_memories_last_used_at ON memories (last_used_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_memories_last_used_at;
DROP INDEX IF EXISTS idx_memories_category;
DROP TABLE IF EXISTS memories;
-- +goose StatementEnd
