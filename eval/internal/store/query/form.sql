-- name: InsertSubject :exec
INSERT INTO subject (id, project_id, name_enc, duty_enc, tag, sort_no)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListSubjects :many
SELECT id, name_enc, duty_enc, COALESCE(tag, '') AS tag, sort_no
FROM subject WHERE project_id = ? ORDER BY sort_no;

-- name: InsertQuestionGroup :exec
INSERT INTO question_group (id, project_id, title, intro, sort_no)
VALUES (?, ?, ?, ?, ?);

-- name: ListQuestionGroups :many
SELECT id, title, COALESCE(intro, '') AS intro, sort_no
FROM question_group WHERE project_id = ? ORDER BY sort_no;

-- name: InsertQuestion :exec
INSERT INTO question (id, project_id, group_id, kind, title, hint, required, sort_no, config)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListQuestionsByGroup :many
SELECT id, kind, title, COALESCE(hint, '') AS hint, required, sort_no, config
FROM question WHERE group_id = ? ORDER BY sort_no;

-- name: InsertOption :exec
INSERT INTO question_option (id, question_id, label, sort_no) VALUES (?, ?, ?, ?);

-- name: ListOptions :many
SELECT id, label, sort_no FROM question_option
WHERE question_id = ? ORDER BY sort_no;

-- name: LinkQuestionSubject :exec
INSERT INTO question_subject (question_id, subject_id) VALUES (?, ?);

-- name: ListQuestionSubjects :many
SELECT qs.subject_id FROM question_subject qs
JOIN subject s ON s.id = qs.subject_id
WHERE qs.question_id = ? ORDER BY s.sort_no;
