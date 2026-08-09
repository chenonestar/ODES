-- name: GetMeta :one
SELECT value FROM meta WHERE key = ?;

-- name: SetMeta :exec
INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

-- name: InsertOpLog :exec
INSERT INTO op_log (id, project_id, action, result, detail, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListOpLogs :many
SELECT action, result, COALESCE(detail, '') AS detail, created_at
FROM op_log WHERE project_id = ? ORDER BY created_at DESC LIMIT ?;

-- name: DeleteOpLogsByProject :exec
DELETE FROM op_log WHERE project_id = ?;
