-- name: UpsertUser :one
INSERT INTO users (did, handle, access_token, refresh_token, token_expiry)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (did) DO UPDATE SET
    handle        = EXCLUDED.handle,
    access_token  = EXCLUDED.access_token,
    refresh_token = EXCLUDED.refresh_token,
    token_expiry  = EXCLUDED.token_expiry
RETURNING *;

-- name: GetUserByDID :one
SELECT * FROM users WHERE did = $1;

-- name: UpdateUserTokens :one
UPDATE users
SET access_token  = $2,
    refresh_token = $3,
    token_expiry  = $4
WHERE did = $1
RETURNING *;

-- name: UpdateUserAvatar :exec
UPDATE users SET avatar = $2 WHERE did = $1;

-- name: CreatePostHistory :one
INSERT INTO post_history (user_id, uri, hashtags)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetRecentTagsByUser :many
SELECT tag::text AS tag, MAX(ph.created_at) AS last_used
FROM post_history ph, unnest(ph.hashtags) AS tag
WHERE ph.user_id = $1
GROUP BY tag
ORDER BY last_used DESC
LIMIT 10;
