-- +goose Up
-- +goose StatementBegin
ALTER TABLE memories ADD COLUMN embedding BLOB;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE memories DROP COLUMN embedding;
-- +goose StatementEnd
