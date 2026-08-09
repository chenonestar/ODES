package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
	"odes/internal/store/gen"
)

var ErrNotFound = errors.New("记录不存在")

// 本文件是 sqlc 生成代码（internal/store/gen）之上的薄封装。
//
// 分工：gen 负责"把 SQL 安全地跑起来并给出类型正确的行"，本层负责
// 字段加解密与领域类型转换。业务编排在 service 层，聚合在 stats 层——
// 这里两者都不做（ADR-003：store 不承担聚合）。

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

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// q 返回绑定在库上的查询集；qtx 返回绑定在事务上的。
func (db *DB) q() *gen.Queries { return gen.New(db.DB) }

func qtx(tx *sql.Tx) *gen.Queries { return gen.New(tx) }

// ── 项目 ────────────────────────────────────────────────────────────

func (db *DB) CreateProject(v *crypto.Vault, p *model.Project) error {
	introEnc, err := v.SealString(p.Intro, p.ID.Bytes())
	if err != nil {
		return err
	}
	scores, _ := json.Marshal(p.GradeScores)
	return db.q().InsertProject(context.Background(), gen.InsertProjectParams{
		ID: p.ID, Name: p.Name, IntroEnc: introEnc, Status: string(p.Status),
		StartAt: fmtTime(p.StartAt), EndAt: fmtTime(p.EndAt),
		ResultOpenAt:    fmtTime(p.ResultOpenAt),
		ExpectedCount:   int64(p.ExpectedCount),
		PaperEntryCount: int64(p.PaperEntryCount),
		ApCount:         int64(p.APCount),
		GradeScores:     string(scores),
		TokenDomain:     nullStr(p.TokenDomain),
		CreatedAt:       fmtTime(p.CreatedAt),
	})
}

func toProject(r gen.Project, v *crypto.Vault) *model.Project {
	p := &model.Project{
		ID: r.ID, Name: r.Name, Status: model.Status(r.Status),
		StartAt: parseTime(r.StartAt), EndAt: parseTime(r.EndAt),
		ResultOpenAt:    parseTime(r.ResultOpenAt),
		ExpectedCount:   int(r.ExpectedCount),
		PaperEntryCount: int(r.PaperEntryCount),
		APCount:         int(r.ApCount),
		PrintedAt:       parseTimePtr(r.PrintedAt),
		TokenDomain:     r.TokenDomain.String,
		CreatedAt:       parseTime(r.CreatedAt),
		PublishedAt:     parseTimePtr(r.PublishedAt),
		ClosedAt:        parseTimePtr(r.ClosedAt),
	}
	_ = json.Unmarshal([]byte(r.GradeScores), &p.GradeScores)
	if v != nil {
		if s, err := v.OpenString(r.IntroEnc, p.ID.Bytes()); err == nil {
			p.Intro = s
		}
	}
	return p
}

func (db *DB) Project(v *crypto.Vault, id anon.ID) (*model.Project, error) {
	r, err := db.q().GetProject(context.Background(), id)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return toProject(r, v), nil
}

func (db *DB) Projects(v *crypto.Vault) ([]*model.Project, error) {
	rows, err := db.q().ListProjects(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]*model.Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProject(r, v))
	}
	return out, nil
}

func (db *DB) UpdateProjectStatus(tx *sql.Tx, id anon.ID, s model.Status, stamp string) error {
	q := db.q()
	if tx != nil {
		q = qtx(tx)
	}
	ctx := context.Background()
	switch s {
	case model.StatusPublished:
		return q.SetProjectPublished(ctx, gen.SetProjectPublishedParams{
			Status: string(s), PublishedAt: nullStr(stamp), ID: id})
	case model.StatusClosed:
		return q.SetProjectClosed(ctx, gen.SetProjectClosedParams{
			Status: string(s), ClosedAt: nullStr(stamp), ID: id})
	default:
		return q.SetProjectStatus(ctx, gen.SetProjectStatusParams{Status: string(s), ID: id})
	}
}

func (db *DB) MarkPrinted(id anon.ID, domain string) error {
	return db.q().MarkProjectPrinted(context.Background(), gen.MarkProjectPrintedParams{
		PrintedAt: nullStr(fmtTime(time.Now())), TokenDomain: nullStr(domain), ID: id})
}

func (db *DB) IncPaperEntry(tx *sql.Tx, id anon.ID) error {
	return qtx(tx).IncPaperEntryCount(context.Background(), id)
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
	return db.q().InsertSubject(context.Background(), gen.InsertSubjectParams{
		ID: s.ID, ProjectID: s.ProjectID, NameEnc: nameEnc,
		DutyEnc: dutyEnc, Tag: nullStr(s.Tag), SortNo: int64(s.SortNo)})
}

func (db *DB) Subjects(v *crypto.Vault, projectID anon.ID) ([]model.Subject, error) {
	rows, err := db.q().ListSubjects(context.Background(), projectID)
	if err != nil {
		return nil, err
	}
	out := make([]model.Subject, 0, len(rows))
	for _, r := range rows {
		s := model.Subject{ID: r.ID, ProjectID: projectID, Tag: r.Tag, SortNo: int(r.SortNo)}
		if s.Name, err = v.OpenString(r.NameEnc, projectID.Bytes()); err != nil {
			return nil, err
		}
		s.Duty, _ = v.OpenString(r.DutyEnc, projectID.Bytes())
		out = append(out, s)
	}
	return out, nil
}

// ── 题目结构 ────────────────────────────────────────────────────────

func (db *DB) CreateGroup(g *model.QuestionGroup) error {
	return db.q().InsertQuestionGroup(context.Background(), gen.InsertQuestionGroupParams{
		ID: g.ID, ProjectID: g.ProjectID, Title: g.Title,
		Intro: nullStr(g.Intro), SortNo: int64(g.SortNo)})
}

func (db *DB) CreateQuestion(projectID anon.ID, q *model.Question) error {
	ctx := context.Background()
	cfg, _ := json.Marshal(q.Config)
	if err := db.q().InsertQuestion(ctx, gen.InsertQuestionParams{
		ID: q.ID, ProjectID: projectID, GroupID: q.GroupID, Kind: string(q.Kind),
		Title: q.Title, Hint: nullStr(q.Hint), Required: boolInt(q.Required),
		SortNo: int64(q.SortNo), Config: string(cfg)}); err != nil {
		return err
	}
	for _, o := range q.Options {
		if err := db.q().InsertOption(ctx, gen.InsertOptionParams{
			ID: o.ID, QuestionID: q.ID, Label: o.Label, SortNo: int64(o.SortNo)}); err != nil {
			return err
		}
	}
	for _, sid := range q.SubjectIDs {
		if err := db.q().LinkQuestionSubject(ctx, gen.LinkQuestionSubjectParams{
			QuestionID: q.ID, SubjectID: sid}); err != nil {
			return err
		}
	}
	return nil
}

// Form 装配一个项目的完整题目结构。作答端与统计共用同一份。
func (db *DB) Form(v *crypto.Vault, p *model.Project) (*model.Form, error) {
	ctx := context.Background()
	subjects, err := db.Subjects(v, p.ID)
	if err != nil {
		return nil, err
	}
	f := &model.Form{Grades: model.DefaultGrades, Subjects: subjects}

	groups, err := db.q().ListQuestionGroups(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		grp := model.QuestionGroup{
			ID: g.ID, ProjectID: p.ID, Title: g.Title,
			Intro: g.Intro, SortNo: int(g.SortNo),
		}
		qs, err := db.q().ListQuestionsByGroup(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range qs {
			q := model.Question{
				ID: r.ID, GroupID: g.ID, Kind: model.QuestionKind(r.Kind),
				Title: r.Title, Hint: r.Hint, Required: r.Required == 1,
				SortNo: int(r.SortNo),
			}
			_ = json.Unmarshal([]byte(r.Config), &q.Config)

			opts, err := db.q().ListOptions(ctx, r.ID)
			if err != nil {
				return nil, err
			}
			for _, o := range opts {
				q.Options = append(q.Options, model.Option{
					ID: o.ID, QuestionID: r.ID, Label: o.Label, SortNo: int(o.SortNo)})
			}
			if q.SubjectIDs, err = db.q().ListQuestionSubjects(ctx, r.ID); err != nil {
				return nil, err
			}
			grp.Questions = append(grp.Questions, q)
		}
		f.Groups = append(f.Groups, grp)
	}

	// 档位名称与档数可配置（FR-FRM-010）：取第一道显式配置了档位的等级题。
	// 同一份测评表内档位必须一致——不同题目用不同档数，统计口径就没法
	// 统一，第 6 章的恒等式也无从谈起，因此这里只认一份配置。
	for _, g := range f.Groups {
		for _, q := range g.Questions {
			if q.Kind == model.KindGrade && model.ValidGrades(q.Config.Grades) {
				f.Grades = q.Config.Grades
				return f, nil
			}
		}
	}
	return f, nil
}



// ── 令牌 ────────────────────────────────────────────────────────────

func (db *DB) InsertTokens(ctx context.Context, toks []model.Token) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		q := qtx(tx)
		for _, t := range toks {
			if err := q.InsertToken(ctx, gen.InsertTokenParams{
				ID: t.ID, ProjectID: t.ProjectID, Value: t.Value,
				ShortCode: t.ShortCode, BatchNo: int64(t.BatchNo),
				ApIndex: int64(t.APIndex), IsSpare: boolInt(t.IsSpare),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func toToken(r gen.Token) *model.Token {
	return &model.Token{
		ID: r.ID, ProjectID: r.ProjectID, Value: r.Value, ShortCode: r.ShortCode,
		BatchNo: int(r.BatchNo), APIndex: int(r.ApIndex),
		IsSpare: r.IsSpare == 1, Used: r.Used == 1,
	}
}

func (db *DB) AnyTokenByValue(value string) (*model.Token, error) {
	r, err := db.q().GetTokenByValue(context.Background(), value)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return toToken(r), nil
}

func (db *DB) AnyTokenByShortCode(code string) (*model.Token, error) {
	r, err := db.q().GetTokenByShortCode(context.Background(), code)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return toToken(r), nil
}

// Tokens 供令牌单打印。返回顺序**不可**依赖：调用方须自行 Shuffle。
func (db *DB) Tokens(projectID anon.ID, spare bool) ([]model.Token, error) {
	rows, err := db.q().ListTokens(context.Background(), gen.ListTokensParams{
		ProjectID: projectID, IsSpare: boolInt(spare)})
	if err != nil {
		return nil, err
	}
	out := make([]model.Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, *toToken(r))
	}
	return out, nil
}

func (db *DB) CountTokens(projectID anon.ID) (total, used int, err error) {
	r, err := db.q().CountTokens(context.Background(), projectID)
	if err != nil {
		return 0, 0, err
	}
	usedN, _ := r.Used.(int64)
	return int(r.Total), int(usedN), nil
}

// ── 作答记录 ────────────────────────────────────────────────────────
//
// InsertAnswerTx 与 MarkTokenUsedTx 必须在同一事务内调用，且顺序为
// 先写作答、后置位令牌（见 service.Submit 与 ADR-004）。

func InsertAnswerTx(tx *sql.Tx, r *model.AnswerRecord) error {
	return qtx(tx).InsertAnswer(context.Background(), gen.InsertAnswerParams{
		ID: r.ID, ProjectID: r.ProjectID, BatchNo: int64(r.BatchNo), Payload: r.Payload})
}

// MarkTokenUsedTx 条件更新。影响行数为 0 意味着并发提交已被抢先，
// 调用方须据此回滚——这是幂等保证的关键（NFR-REL-011）。
func MarkTokenUsedTx(tx *sql.Tx, id anon.ID) (int64, error) {
	return qtx(tx).MarkTokenUsed(context.Background(), id)
}

// LoadAnswers 是统计管线唯一的一次全表读取（HLD 7.2）。
// 查询里刻意没有 ORDER BY，调用方仍须再 Shuffle 一次。
func (db *DB) LoadAnswers(projectID anon.ID) ([]model.AnswerRecord, error) {
	rows, err := db.q().ListAnswers(context.Background(), projectID)
	if err != nil {
		return nil, err
	}
	out := make([]model.AnswerRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.AnswerRecord{
			ID: r.ID, ProjectID: r.ProjectID, BatchNo: int(r.BatchNo), Payload: r.Payload})
	}
	return out, nil
}

func (db *DB) CountAnswers(projectID anon.ID) (int, error) {
	n, err := db.q().CountAnswers(context.Background(), projectID)
	return int(n), err
}

// ── 试填数据（FR-PRJ-030~033）───────────────────────────────────────

func (db *DB) InsertTrialToken(projectID anon.ID, value string) error {
	return db.q().InsertTrialToken(context.Background(), gen.InsertTrialTokenParams{
		ID: anon.NewID(), ProjectID: projectID, Value: value})
}

func (db *DB) TrialTokens(projectID anon.ID) ([]string, error) {
	return db.q().ListTrialTokens(context.Background(), projectID)
}

func (db *DB) IsTrialToken(projectID anon.ID, value string) (bool, error) {
	n, err := db.q().CountTrialTokenByValue(context.Background(),
		gen.CountTrialTokenByValueParams{ProjectID: projectID, Value: value})
	return n > 0, err
}

func (db *DB) InsertTrialAnswer(projectID anon.ID, payload []byte) error {
	return db.q().InsertTrialAnswer(context.Background(), gen.InsertTrialAnswerParams{
		ID: anon.NewID(), ProjectID: projectID, Payload: payload,
		CreatedAt: fmtTime(time.Now())})
}

func (db *DB) CountTrialAnswers(projectID anon.ID) (int, error) {
	n, err := db.q().CountTrialAnswers(context.Background(), projectID)
	return int(n), err
}

func (db *DB) ClearTrialData(tx *sql.Tx, projectID anon.ID) error {
	return qtx(tx).ClearTrialAnswers(context.Background(), projectID)
}

// ── 名单（分发后可整表删除，FR-TKN-012）─────────────────────────────

func (db *DB) InsertRoster(v *crypto.Vault, projectID anon.ID, label string) error {
	enc, err := v.SealString(label, projectID.Bytes())
	if err != nil {
		return err
	}
	return db.q().InsertRoster(context.Background(), gen.InsertRosterParams{
		ID: anon.NewID(), ProjectID: projectID, LabelEnc: enc})
}

func (db *DB) CountRoster(projectID anon.ID) (int, error) {
	n, err := db.q().CountRoster(context.Background(), projectID)
	return int(n), err
}

func (db *DB) DeleteRoster(projectID anon.ID) error {
	return db.q().DeleteRoster(context.Background(), projectID)
}

// ── 归档与擦除 ──────────────────────────────────────────────────────

func (db *DB) PurgeProject(tx *sql.Tx, projectID anon.ID) error {
	ctx := context.Background()
	q := qtx(tx)
	// op_log.project_id **没有**外键（archive_meta 同理），因为这两张表要在
	// 项目行消失后仍可写入。级联带不走它们，必须显式删除——SRS 5.3 规定
	// 操作日志随项目擦除。
	if err := q.DeleteOpLogsByProject(ctx,
		anon.NullID{ID: projectID, Valid: true}); err != nil {
		return err
	}
	// 其余关联数据由外键的 ON DELETE CASCADE 带走：question / question_group /
	// question_option / subject / question_subject / roster / token /
	// answer_record / trial_token / trial_answer。
	// 这依赖连接开启了 foreign_keys —— 见 Open() 中 DSN 的说明。
	return q.DeleteProject(ctx, projectID)
}

func (db *DB) WriteArchiveMeta(projectID anon.ID, name string, expected, submitted int,
	sha, verifyCode string) error {
	return db.q().InsertArchiveMeta(context.Background(), gen.InsertArchiveMetaParams{
		ProjectID: projectID, ProjectName: name,
		ExpectedCount: int64(expected), SubmittedCount: int64(submitted),
		PackageSha256: sha, VerifyCode: verifyCode, ArchivedAt: fmtTime(time.Now())})
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
	rows, err := db.q().ListArchiveMetas(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]ArchiveMeta, 0, len(rows))
	for _, r := range rows {
		out = append(out, ArchiveMeta{
			Name: r.ProjectName, Expected: int(r.ExpectedCount),
			Submitted: int(r.SubmittedCount), SHA256: r.PackageSha256,
			VerifyCode: r.VerifyCode, ArchivedAt: r.ArchivedAt})
	}
	return out, nil
}
