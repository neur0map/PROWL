-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS prompt_caches (
    id TEXT PRIMARY KEY NOT NULL,
    cache_key TEXT NOT NULL,
    resource TEXT NOT NULL,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    token_count INTEGER NOT NULL,
    creation_cost REAL NOT NULL,
    invalidated INTEGER NOT NULL DEFAULT 0,
    storage_cost REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_prompt_caches_key_expiry ON prompt_caches(cache_key, expires_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE prompt_caches;
-- +goose StatementEnd
