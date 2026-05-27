-- +goose Up
CREATE TABLE IF NOT EXISTS gateway_backend (
    name         VARCHAR(255) PRIMARY KEY,
    url          VARCHAR(2048) NOT NULL,
    routing_group VARCHAR(255) NOT NULL DEFAULT '',
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS gateway_backend;
