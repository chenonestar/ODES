package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
)

var ErrNotFound = errors.New("记录不存在")

const rfc = time.RFC3339

func fmtTime(t time.Time) string { return t.Format(rfc) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(rfc, s)
	return t
}

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

// ── 项目 ────────────────────────────────────────────────────────────

func (db *DB) CreateProject(v *crypto.Vault, p *model.Project) error {
	introEnc, err := v.SealString(p.Intro, p.ID.Bytes())
	if err != nil {
		return err
	}
	scores, _ := json.Marshal(p.GradeScores)
	_, err = db.Exec(`INSERT INTO project
		(id,name,intro_enc,status,start_at,end_at,result_open_at,expected_count,
		 paper_entry_count,ap_count,grade_scores,printed_at,token_domain,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
		p.ID.Bytes(), p.Name, introEnc, string(p.Status),
		fmtTime(p.StartAt), fmtTime(p.EndAt), fmtTime(p.ResultOpenAt),
		p.ExpectedCount, p.PaperEntryCount, p.APCount, string(scores),
		p.TokenDomain, fmtTime(p.CreatedAt))
	return err
}

const projectCols = `id,name,intro_enc,status,start_at,end_at,result_open_at,expected_count,
	paper_entry_count,ap_count,grade_scores,printed_at,token_domain,created_at,published_at,closed_at`

func scanProject(sc interface{ Scan(...any) error }, v *crypto.Vault) (*model.Project, error) {
	var (
		p           model.Project
		idB         []byte
		introEnc    []byte
		status      string
		start       string
		end         string
		resultOpen  string
		scores      string
		printedAt   sql.NullString
		tokenDomain sql.NullString
		created     string
		published   sql.NullString
		closed      sql.NullString
	)
	if err := sc.Scan(&idB, &p.Name, &introEnc, &status, &start, &end, &resultOpen,
		&p.ExpectedCount, &p.PaperEntryCount, &p.APCount, &scores,
		&printedAt, &tokenDomain, &created, &published, &closed); err != nil {
		return nil, err
	}
	id, err := anon.IDFromBytes(idB)
	if err != nil {
		return nil, err
	}
	p.ID = id
	p.Status = model.Status(status)
	p.StartAt, p.EndAt, p.ResultOpenAt = parseTime(start), parseTime(end), parseTime(resultOpen)
	p.CreatedAt = parseTime(created)
	p.PublishedAt, p.ClosedAt, p.PrintedAt = parseTimePtr(published), parseTimePtr(closed), parseTimePtr(printedAt)
	p.TokenDomain = tokenDomain.String
	_ = json.Unmarshal([]byte(scores), &p.GradeScores)
	if v != nil {
		if s, err := v.OpenString(introEnc, p.ID.Bytes()); err == nil {
			p.Intro = s
		}
	}
	return &p, nil
}

func (db *DB) Project(v *crypto.Vault, id anon.ID) (*model.Project, error) {
	row := db.QueryRow(`SELECT `+projectCols+` FROM project WHERE id=?`, id.Bytes())
	p, err := scanProject(row, v)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return p, err
}

func (db *DB) Projects(v *crypto.Vault) ([]*model.Project, error) {
	rows, err := db.Query(`SELECT ` + projectCols + ` FROM project ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Project
	for rows.Next() {
		p, err := scanProject(rows, v)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) UpdateProjectStatus(tx *sql.Tx, id anon.ID, s model.Status, stamp string) error {
	ex := db.Exec
	if tx != nil {
		ex = tx.Exec
	}
	var err error
	switch s {
	case model.StatusPublished:
		_, err = ex(`UPDATE project SET status=?,published_at=? WHERE id=?`, string(s), stamp, id.Bytes())
	case model.StatusClosed:
		_, err = ex(`UPDATE project SET status=?,closed_at=? WHERE id=?`, string(s), stamp, id.Bytes())
	default:
		_, err = ex(`UPDATE project SET status=? WHERE id=?`, string(s), id.Bytes())
	}
	return err
}

func (db *DB) MarkPrinted(id anon.ID, domain string) error {
	_, err := db.Exec(`UPDATE project SET printed_at=?,token_domain=? WHERE id=?`,
		fmtTime(time.Now()), domain, id.Bytes())
	return err
}

func (db *DB) IncPaperEntry(tx *sql.Tx, id anon.ID) error {
	_, err := tx.Exec(`UPDATE project SET paper_entry_count=paper_entry_count+1 WHERE id=?`, id.Bytes())
	return err
}

// ── 测评对象 ────────────────────────────────────────────────────────

func (db *DB) CreateSubject(v *crypto.Vault, s *model.Subject) error {
	nameEnc, err := v.SealString(s.Name, s.ProjectID.Bytes())
	if err != nil {
		return err
	}
	dutyEnc, err := v.SealString(s.Duty, s.ProjectID.Bytes())
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO subject(id,project_id,name_enc,duty_enc,tag,sort_no)
		VALUES(?,?,?,?,?,?)`, s.ID.Bytes(), s.ProjectID.Bytes(), nameEnc, dutyEnc, s.Tag, s.SortNo)
	return err
}

func (db *DB) Subjects(v *crypto.Vault, projectID anon.ID) ([]model.Subject, error) {
	rows, err := db.Query(`SELECT id,name_enc,duty_enc,COALESCE(tag,''),sort_no
		FROM subject WHERE project_id=? ORDER BY sort_no`, projectID.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Subject
	for rows.Next() {
		var (
			s                model.Subject
			idB              []byte
			nameEnc, dutyEnc []byte
		)
		if err := rows.Scan(&idB, &nameEnc, &dutyEnc, &s.Tag, &s.SortNo); err != nil {
			return nil, err
		}
		id, err := anon.IDFromBytes(idB)
		if err != nil {
			return nil, err
		}
		s.ID, s.ProjectID = id, projectID
		if s.Name, err = v.OpenString(nameEnc, projectID.Bytes()); err != nil {
			return nil, fmt.Errorf("解密测评对象姓名失败: %w", err)
		}
		s.Duty, _ = v.OpenString(dutyEnc, projectID.Bytes())
		out = append(out, s)
	}
	return out, rows.Err()
}

// ── 题目结构 ────────────────────────────────────────────────────────

func (db *DB) CreateGroup(g *model.QuestionGroup) error {
	_, err := db.Exec(`INSERT INTO question_group(id,project_id,title,intro,sort_no)
		VALUES(?,?,?,?,?)`, g.ID.Bytes(), g.ProjectID.Bytes(), g.Title, g.Intro, g.SortNo)
	return err
}

func (db *DB) CreateQuestion(projectID anon.ID, q *model.Question) error {
	cfg, _ := json.Marshal(q.Config)
	req := 0
	if q.Required {
		req = 1
	}
	_, err := db.Exec(`INSERT INTO question(id,project_id,group_id,kind,title,hint,required,sort_no,config)
		VALUES(?,?,?,?,?,?,?,?,?)`, q.ID.Bytes(), projectID.Bytes(), q.GroupID.Bytes(),
		string(q.Kind), q.Title, q.Hint, req, q.SortNo, string(cfg))
	if err != nil {
		return err
	}
	for _, o := range q.Options {
		if _, err := db.Exec(`INSERT INTO question_option(id,question_id,label,sort_no)
			VALUES(?,?,?,?)`, o.ID.Bytes(), q.ID.Bytes(), o.Label, o.SortNo); err != nil {
			return err
		}
	}
	for _, sid := range q.SubjectIDs {
		if _, err := db.Exec(`INSERT INTO question_subject(question_id,subject_id) VALUES(?,?)`,
			q.ID.Bytes(), sid.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// Form 装配一个项目的完整题目结构。作答端与统计共用同一份。
func (db *DB) Form(v *crypto.Vault, p *model.Project) (*model.Form, error) {
	subjects, err := db.Subjects(v, p.ID)
	if err != nil {
		return nil, err
	}
	grades := []string{"优秀", "称职", "基本称职", "不称职"}
	f := &model.Form{Grades: grades, Subjects: subjects}

	grows, err := db.Query(`SELECT id,title,COALESCE(intro,''),sort_no
		FROM question_group WHERE project_id=? ORDER BY sort_no`, p.ID.Bytes())
	if err != nil {
		return nil, err
	}
	defer grows.Close()
	for grows.Next() {
		var g model.QuestionGroup
		var idB []byte
		if err := grows.Scan(&idB, &g.Title, &g.Intro, &g.SortNo); err != nil {
			return nil, err
		}
		if g.ID, err = anon.IDFromBytes(idB); err != nil {
			return nil, err
		}
		g.ProjectID = p.ID
		f.Groups = append(f.Groups, g)
	}
	if err := grows.Err(); err != nil {
		return nil, err
	}

	for i := range f.Groups {
		qs, err := db.questions(f.Groups[i].ID)
		if err != nil {
			return nil, err
		}
		f.Groups[i].Questions = qs
	}
	return f, nil
}

func (db *DB) questions(groupID anon.ID) ([]model.Question, error) {
	rows, err := db.Query(`SELECT id,kind,title,COALESCE(hint,''),required,sort_no,config
		FROM question WHERE group_id=? ORDER BY sort_no`, groupID.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Question
	for rows.Next() {
		var (
			q    model.Question
			idB  []byte
			kind string
			req  int
			cfg  string
		)
		if err := rows.Scan(&idB, &kind, &q.Title, &q.Hint, &req, &q.SortNo, &cfg); err != nil {
			return nil, err
		}
		if q.ID, err = anon.IDFromBytes(idB); err != nil {
			return nil, err
		}
		q.GroupID, q.Kind, q.Required = groupID, model.QuestionKind(kind), req == 1
		_ = json.Unmarshal([]byte(cfg), &q.Config)
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Options, err = db.options(out[i].ID); err != nil {
			return nil, err
		}
		if out[i].SubjectIDs, err = db.questionSubjects(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (db *DB) options(qid anon.ID) ([]model.Option, error) {
	rows, err := db.Query(`SELECT id,label,sort_no FROM question_option
		WHERE question_id=? ORDER BY sort_no`, qid.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Option
	for rows.Next() {
		var o model.Option
		var idB []byte
		if err := rows.Scan(&idB, &o.Label, &o.SortNo); err != nil {
			return nil, err
		}
		if o.ID, err = anon.IDFromBytes(idB); err != nil {
			return nil, err
		}
		o.QuestionID = qid
		out = append(out, o)
	}
	return out, rows.Err()
}

func (db *DB) questionSubjects(qid anon.ID) ([]anon.ID, error) {
	rows, err := db.Query(`SELECT qs.subject_id FROM question_subject qs
		JOIN subject s ON s.id=qs.subject_id
		WHERE qs.question_id=? ORDER BY s.sort_no`, qid.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []anon.ID
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		id, err := anon.IDFromBytes(b)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ── 令牌 ────────────────────────────────────────────────────────────

func (db *DB) InsertTokens(ctx context.Context, toks []model.Token) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		st, err := tx.Prepare(`INSERT INTO token
			(id,project_id,value,short_code,batch_no,ap_index,is_spare,used)
			VALUES(?,?,?,?,?,?,?,0)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, t := range toks {
			spare := 0
			if t.IsSpare {
				spare = 1
			}
			if _, err := st.Exec(t.ID.Bytes(), t.ProjectID.Bytes(), t.Value,
				t.ShortCode, t.BatchNo, t.APIndex, spare); err != nil {
				return err
			}
		}
		return nil
	})
}

// TokenByValue 是作答入口的等值查找。
//
// 令牌值不加密正是为了这次查找——加密会破坏索引；且令牌本身不含身份
// 信息，泄露它只意味着可冒用一次作答机会，而非泄露身份（LLD 2.4）。
func (db *DB) TokenByValue(projectID anon.ID, value string) (*model.Token, error) {
	return db.tokenBy(`value`, projectID, value)
}

func (db *DB) TokenByShortCode(projectID anon.ID, code string) (*model.Token, error) {
	return db.tokenBy(`short_code`, projectID, code)
}

func (db *DB) tokenBy(col string, projectID anon.ID, val string) (*model.Token, error) {
	row := db.QueryRow(fmt.Sprintf(
		`SELECT id,project_id,value,short_code,batch_no,ap_index,is_spare,used
		 FROM token WHERE project_id=? AND %s=?`, col), projectID.Bytes(), val)
	return scanToken(row)
}

// AnyTokenByValue 在不知道项目的情况下按令牌值定位（扫码入口 /e/{token}）。
func (db *DB) AnyTokenByValue(value string) (*model.Token, error) {
	row := db.QueryRow(`SELECT id,project_id,value,short_code,batch_no,ap_index,is_spare,used
		FROM token WHERE value=?`, value)
	return scanToken(row)
}

func (db *DB) AnyTokenByShortCode(code string) (*model.Token, error) {
	row := db.QueryRow(`SELECT id,project_id,value,short_code,batch_no,ap_index,is_spare,used
		FROM token WHERE short_code=?`, code)
	return scanToken(row)
}

func scanToken(row *sql.Row) (*model.Token, error) {
	var (
		t          model.Token
		idB, pidB  []byte
		spare, use int
	)
	err := row.Scan(&idB, &pidB, &t.Value, &t.ShortCode, &t.BatchNo, &t.APIndex, &spare, &use)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if t.ID, err = anon.IDFromBytes(idB); err != nil {
		return nil, err
	}
	if t.ProjectID, err = anon.IDFromBytes(pidB); err != nil {
		return nil, err
	}
	t.IsSpare, t.Used = spare == 1, use == 1
	return &t, nil
}

// Tokens 供令牌单打印。返回顺序**不可**依赖：调用方须自行 Shuffle。
func (db *DB) Tokens(projectID anon.ID, spare bool) ([]model.Token, error) {
	s := 0
	if spare {
		s = 1
	}
	rows, err := db.Query(`SELECT id,project_id,value,short_code,batch_no,ap_index,is_spare,used
		FROM token WHERE project_id=? AND is_spare=?`, projectID.Bytes(), s)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Token
	for rows.Next() {
		var (
			t           model.Token
			idB, pidB   []byte
			isSp, isUse int
		)
		if err := rows.Scan(&idB, &pidB, &t.Value, &t.ShortCode, &t.BatchNo,
			&t.APIndex, &isSp, &isUse); err != nil {
			return nil, err
		}
		if t.ID, err = anon.IDFromBytes(idB); err != nil {
			return nil, err
		}
		if t.ProjectID, err = anon.IDFromBytes(pidB); err != nil {
			return nil, err
		}
		t.IsSpare, t.Used = isSp == 1, isUse == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

func (db *DB) CountTokens(projectID anon.ID) (total, used int, err error) {
	err = db.QueryRow(`SELECT count(*),COALESCE(sum(used),0) FROM token
		WHERE project_id=? AND is_spare=0`, projectID.Bytes()).Scan(&total, &used)
	return
}

// ── 作答记录 ────────────────────────────────────────────────────────

// InsertAnswerTx 与 MarkTokenUsedTx 必须在同一事务内调用，顺序为
// 先写作答、后置位令牌（见 service.Submit 与 ADR-004）。

func InsertAnswerTx(tx *sql.Tx, r *model.AnswerRecord) error {
	_, err := tx.Exec(`INSERT INTO answer_record(id,project_id,batch_no,payload)
		VALUES(?,?,?,?)`, r.ID.Bytes(), r.ProjectID.Bytes(), r.BatchNo, r.Payload)
	return err
}

// MarkTokenUsedTx 条件更新。影响行数为 0 意味着并发提交已被抢先，
// 调用方须据此回滚——这是幂等保证的关键（NFR-REL-011）。
func MarkTokenUsedTx(tx *sql.Tx, id anon.ID) (int64, error) {
	res, err := tx.Exec(`UPDATE token SET used=1 WHERE id=? AND used=0`, id.Bytes())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LoadAnswers 是统计管线唯一的一次全表读取（HLD 7.2）。
//
// 刻意不写 ORDER BY：让读出顺序就是物理顺序，而物理顺序因随机主键
// + WITHOUT ROWID 已与提交顺序无关。调用方仍须再 Shuffle 一次。
func (db *DB) LoadAnswers(projectID anon.ID) ([]model.AnswerRecord, error) {
	rows, err := db.Query(`SELECT id,project_id,batch_no,payload FROM answer_record
		WHERE project_id=?`, projectID.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AnswerRecord
	for rows.Next() {
		var r model.AnswerRecord
		var idB, pidB []byte
		if err := rows.Scan(&idB, &pidB, &r.BatchNo, &r.Payload); err != nil {
			return nil, err
		}
		if r.ID, err = anon.IDFromBytes(idB); err != nil {
			return nil, err
		}
		if r.ProjectID, err = anon.IDFromBytes(pidB); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) CountAnswers(projectID anon.ID) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM answer_record WHERE project_id=?`,
		projectID.Bytes()).Scan(&n)
	return n, err
}

// ── 试填数据（FR-PRJ-030~033）───────────────────────────────────────

func (db *DB) InsertTrialToken(projectID anon.ID, value string) error {
	id := anon.NewID()
	_, err := db.Exec(`INSERT INTO trial_token(id,project_id,value) VALUES(?,?,?)`,
		id.Bytes(), projectID.Bytes(), value)
	return err
}

func (db *DB) TrialTokens(projectID anon.ID) ([]string, error) {
	rows, err := db.Query(`SELECT value FROM trial_token WHERE project_id=?`, projectID.Bytes())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (db *DB) IsTrialToken(projectID anon.ID, value string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM trial_token WHERE project_id=? AND value=?`,
		projectID.Bytes(), value).Scan(&n)
	return n > 0, err
}

func (db *DB) InsertTrialAnswer(projectID anon.ID, payload []byte) error {
	id := anon.NewID()
	_, err := db.Exec(`INSERT INTO trial_answer(id,project_id,payload,created_at)
		VALUES(?,?,?,?)`, id.Bytes(), projectID.Bytes(), payload, fmtTime(time.Now()))
	return err
}

func (db *DB) CountTrialAnswers(projectID anon.ID) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM trial_answer WHERE project_id=?`,
		projectID.Bytes()).Scan(&n)
	return n, err
}

// ClearTrialData 在发布时调用（FR-PRJ-033）。
func (db *DB) ClearTrialData(tx *sql.Tx, projectID anon.ID) error {
	_, err := tx.Exec(`DELETE FROM trial_answer WHERE project_id=?`, projectID.Bytes())
	return err
}

// ── 名单（分发后可整表删除，FR-TKN-012）─────────────────────────────

func (db *DB) InsertRoster(v *crypto.Vault, projectID anon.ID, label string) error {
	enc, err := v.SealString(label, projectID.Bytes())
	if err != nil {
		return err
	}
	id := anon.NewID()
	_, err = db.Exec(`INSERT INTO roster(id,project_id,label_enc) VALUES(?,?,?)`,
		id.Bytes(), projectID.Bytes(), enc)
	return err
}

func (db *DB) CountRoster(projectID anon.ID) (int, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM roster WHERE project_id=?`,
		projectID.Bytes()).Scan(&n)
	return n, err
}

func (db *DB) DeleteRoster(projectID anon.ID) error {
	_, err := db.Exec(`DELETE FROM roster WHERE project_id=?`, projectID.Bytes())
	return err
}

// ── 归档与擦除 ──────────────────────────────────────────────────────

func (db *DB) PurgeProject(tx *sql.Tx, projectID anon.ID) error {
	// 外键的 ON DELETE CASCADE 会带走 question/subject/token/answer_record 等。
	// 这依赖连接开启了 foreign_keys —— 见 Open() 中 DSN 的说明。
	_, err := tx.Exec(`DELETE FROM project WHERE id=?`, projectID.Bytes())
	return err
}

func (db *DB) WriteArchiveMeta(projectID anon.ID, name string, expected, submitted int,
	sha, verifyCode string) error {
	_, err := db.Exec(`INSERT INTO archive_meta
		(project_id,project_name,expected_count,submitted_count,package_sha256,verify_code,archived_at)
		VALUES(?,?,?,?,?,?,?)`, projectID.Bytes(), name, expected, submitted, sha, verifyCode,
		fmtTime(time.Now()))
	return err
}

type ArchiveMeta struct {
	Name       string
	Expected   int
	Submitted  int
	SHA256     string
	VerifyCode string
	ArchivedAt string
}

func (db *DB) ArchiveMetas() ([]ArchiveMeta, error) {
	rows, err := db.Query(`SELECT project_name,expected_count,submitted_count,
		package_sha256,verify_code,archived_at FROM archive_meta ORDER BY archived_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArchiveMeta
	for rows.Next() {
		var a ArchiveMeta
		if err := rows.Scan(&a.Name, &a.Expected, &a.Submitted, &a.SHA256,
			&a.VerifyCode, &a.ArchivedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
