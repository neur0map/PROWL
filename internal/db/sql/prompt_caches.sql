-- name: GetPromptCache :one
SELECT * FROM prompt_caches
WHERE cache_key = ? AND expires_at > ? AND invalidated = 0
ORDER BY expires_at DESC
LIMIT 1;

-- name: InsertPromptCache :execrows
INSERT INTO prompt_caches (
    id, cache_key, resource, session_id, created_at, expires_at,
    token_count, creation_cost, storage_cost
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING;

-- name: InvalidatePromptCache :exec
UPDATE prompt_caches SET invalidated = 1 WHERE id = ?;
