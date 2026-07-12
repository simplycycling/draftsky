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

-- name: UpdateUserTheme :one
UPDATE users SET theme = $2 WHERE did = $1
RETURNING *;

-- name: TouchUserLastSeen :exec
-- Best-effort activity stamp. The staleness gate lives IN the SQL so concurrent
-- requests within the same hour all no-op (WHERE matches no row) instead of racing
-- to redundant writes — the caller fires this in a detached goroutine on every
-- authenticated request without a preceding SELECT to decide whether to write.
UPDATE users
SET last_seen_at = now()
WHERE did = $1
  AND (last_seen_at IS NULL OR last_seen_at < now() - interval '1 hour');

-- name: GetAdminStats :one
-- Single-pass user metrics for the admin dashboard. FILTER aggregates keep it to one
-- scan of users with no per-user loop. "today" is the current calendar day; "this
-- week" and the activity windows are trailing intervals off now().
SELECT
    count(*)                                                                  AS total_users,
    count(*) FILTER (WHERE created_at   >= date_trunc('day', now()))          AS new_today,
    count(*) FILTER (WHERE created_at   >= now() - interval '7 days')         AS new_this_week,
    count(*) FILTER (WHERE last_seen_at >= now() - interval '1 day')          AS dau,
    count(*) FILTER (WHERE last_seen_at >= now() - interval '7 days')         AS wau,
    count(*) FILTER (WHERE last_seen_at >= now() - interval '30 days')        AS mau
FROM users;

-- name: GetContentStats :one
-- Bonus one-liners for the admin dashboard: lifetime templates + recorded posts.
SELECT
    (SELECT count(*) FROM templates)     AS total_templates,
    (SELECT count(*) FROM post_history)  AS total_posts;

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
