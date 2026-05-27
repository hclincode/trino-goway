-- +goose Up
CREATE TABLE IF NOT EXISTS query_history (
    query_id     VARCHAR(255) PRIMARY KEY,
    backend_url  VARCHAR(2048) NOT NULL,
    user_name    VARCHAR(255) NOT NULL DEFAULT '',
    source       VARCHAR(255) NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS query_history;
