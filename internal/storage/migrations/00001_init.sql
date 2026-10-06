-- +goose Up
CREATE TABLE IF NOT EXISTS app_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO app_meta (key, value) VALUES ('schema', '1');

-- +goose Down
DROP TABLE IF EXISTS app_meta;
