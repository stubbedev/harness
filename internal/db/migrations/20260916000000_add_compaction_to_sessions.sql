-- +goose Up
ALTER TABLE sessions ADD COLUMN compaction_summary TEXT;
ALTER TABLE sessions ADD COLUMN compaction_boundary_id TEXT;
ALTER TABLE sessions ADD COLUMN compaction_aged_id TEXT;

-- +goose Down
ALTER TABLE sessions DROP COLUMN compaction_aged_id;
ALTER TABLE sessions DROP COLUMN compaction_boundary_id;
ALTER TABLE sessions DROP COLUMN compaction_summary;
