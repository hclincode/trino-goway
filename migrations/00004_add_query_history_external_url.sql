-- +goose Up
ALTER TABLE query_history ADD COLUMN external_url VARCHAR(2048) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE query_history DROP COLUMN external_url;
