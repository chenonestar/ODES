-- name: InsertProject :exec
INSERT INTO project (
  id, name, intro_enc, status, start_at, end_at, result_open_at,
  expected_count, paper_entry_count, ap_count, grade_scores,
  printed_at, token_domain, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?);

-- name: GetProject :one
SELECT id, name, intro_enc, status, start_at, end_at, result_open_at,
       expected_count, paper_entry_count, ap_count, grade_scores,
       printed_at, token_domain, created_at, published_at, closed_at
FROM project WHERE id = ?;

-- name: ListProjects :many
SELECT id, name, intro_enc, status, start_at, end_at, result_open_at,
       expected_count, paper_entry_count, ap_count, grade_scores,
       printed_at, token_domain, created_at, published_at, closed_at
FROM project ORDER BY created_at DESC;

-- name: SetProjectStatus :exec
UPDATE project SET status = ? WHERE id = ?;

-- name: SetProjectPublished :exec
UPDATE project SET status = ?, published_at = ? WHERE id = ?;

-- name: SetProjectClosed :exec
UPDATE project SET status = ?, closed_at = ? WHERE id = ?;

-- name: MarkProjectPrinted :exec
UPDATE project SET printed_at = ?, token_domain = ? WHERE id = ?;

-- name: IncPaperEntryCount :exec
UPDATE project SET paper_entry_count = paper_entry_count + 1 WHERE id = ?;

-- name: DeleteProject :exec
DELETE FROM project WHERE id = ?;
