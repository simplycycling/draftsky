-- name: ListTemplatesByUser :many
SELECT * FROM templates
WHERE user_id = $1
ORDER BY position ASC, created_at ASC;

-- name: GetTemplate :one
SELECT * FROM templates WHERE id = $1 AND user_id = $2;

-- name: CreateTemplate :one
INSERT INTO templates (user_id, name, suffix, position)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateTemplate :one
UPDATE templates
SET name   = $3,
    suffix = $4
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: DeleteTemplate :exec
DELETE FROM templates WHERE id = $1 AND user_id = $2;

-- name: UpdateTemplatePosition :one
UPDATE templates
SET position = $3
WHERE id = $1 AND user_id = $2
RETURNING *;
