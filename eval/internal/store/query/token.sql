-- name: InsertToken :exec
INSERT INTO token (id, project_id, value, short_code, batch_no, ap_index, is_spare, used)
VALUES (?, ?, ?, ?, ?, ?, ?, 0);

-- name: GetTokenByValue :one
SELECT id, project_id, value, short_code, batch_no, ap_index, is_spare, used
FROM token WHERE value = ?;

-- name: GetTokenByShortCode :one
SELECT id, project_id, value, short_code, batch_no, ap_index, is_spare, used
FROM token WHERE short_code = ?;

-- name: ListTokens :many
SELECT id, project_id, value, short_code, batch_no, ap_index, is_spare, used
FROM token WHERE project_id = ? AND is_spare = ?;

-- name: CountTokens :one
SELECT count(*) AS total, COALESCE(sum(used), 0) AS used
FROM token WHERE project_id = ? AND is_spare = 0;

-- name: MarkTokenUsed :execrows
UPDATE token SET used = 1 WHERE id = ? AND used = 0;

-- name: InsertTrialToken :exec
INSERT INTO trial_token (id, project_id, value) VALUES (?, ?, ?);

-- name: ListTrialTokens :many
SELECT value FROM trial_token WHERE project_id = ?;

-- name: CountTrialTokenByValue :one
SELECT count(*) AS n FROM trial_token WHERE project_id = ? AND value = ?;

-- name: InsertRoster :exec
INSERT INTO roster (id, project_id, label_enc) VALUES (?, ?, ?);

-- name: CountRoster :one
SELECT count(*) AS n FROM roster WHERE project_id = ?;

-- name: DeleteRoster :exec
DELETE FROM roster WHERE project_id = ?;
