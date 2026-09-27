-- +goose Up
ALTER TABLE sessions ADD COLUMN goal TEXT;

-- +goose Down
ALTER TABLE sessions DROP COLUMN goal;
