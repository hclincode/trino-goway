-- +goose Up
ALTER TABLE gateway_backend ADD COLUMN external_url VARCHAR(2048) NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE gateway_backend DROP COLUMN external_url;
