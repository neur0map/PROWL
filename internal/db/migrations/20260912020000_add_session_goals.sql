-- +goose Up
CREATE TABLE IF NOT EXISTS session_goals (
    session_id TEXT PRIMARY KEY NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    objective TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'paused', 'budget-limited', 'complete')),
    token_budget INTEGER CHECK (token_budget > 0),
    tokens_used INTEGER NOT NULL DEFAULT 0,
    time_used_seconds REAL NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE session_goals;
