-- name: InsertAnswer :exec
INSERT INTO answer_record (id, project_id, batch_no, payload) VALUES (?, ?, ?, ?);

-- name: ListAnswers :many
SELECT id, project_id, batch_no, payload FROM answer_record WHERE project_id = ?;

-- name: CountAnswers :one
SELECT count(*) AS n FROM answer_record WHERE project_id = ?;

-- name: InsertTrialAnswer :exec
INSERT INTO trial_answer (id, project_id, payload, created_at) VALUES (?, ?, ?, ?);

-- name: CountTrialAnswers :one
SELECT count(*) AS n FROM trial_answer WHERE project_id = ?;

-- name: ClearTrialAnswers :exec
DELETE FROM trial_answer WHERE project_id = ?;

-- name: InsertArchiveMeta :exec
INSERT INTO archive_meta
  (project_id, project_name, expected_count, submitted_count,
   package_sha256, verify_code, archived_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListArchiveMetas :many
SELECT project_name, expected_count, submitted_count,
       package_sha256, verify_code, archived_at
FROM archive_meta ORDER BY archived_at DESC;
