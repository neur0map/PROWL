-- name: GetSessionGoal :one
SELECT * FROM session_goals WHERE session_id = ?;

-- name: SaveSessionGoal :one
INSERT INTO session_goals (
    session_id, id, objective, status, token_budget, tokens_used,
    time_used_seconds, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    id = excluded.id,
    objective = excluded.objective,
    status = excluded.status,
    token_budget = excluded.token_budget,
    tokens_used = excluded.tokens_used,
    time_used_seconds = excluded.time_used_seconds,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at
RETURNING *;

-- name: DeleteSessionGoal :exec
DELETE FROM session_goals WHERE session_id = ?;
