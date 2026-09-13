-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS model_catalog (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    data TEXT NOT NULL,
    fetched_at INTEGER NOT NULL  -- Unix secs of last successful fetch
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS model_catalog;
-- +goose StatementEnd
