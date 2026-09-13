-- name: GetModelCatalog :one
SELECT * FROM model_catalog WHERE id = 1;

-- name: SaveModelCatalog :one
INSERT INTO model_catalog (id, data, fetched_at)
VALUES (1, ?, strftime('%s', 'now'))
ON CONFLICT (id) DO UPDATE SET
data = excluded.data,
fetched_at = excluded.fetched_at
RETURNING *;
