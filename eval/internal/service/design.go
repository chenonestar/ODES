package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
)

// ── 项目与测评表编辑（FR-PRJ-020~022 / FR-FRM-020~034）──────────────
//
// 两条纪律贯穿本文件，处理器一律不得自己实现：
//
//  1. **只在草稿态可编辑**（FR-PRJ-022）。已发布之后改题，等于让不同的人
//     答的不是同一张表——先答的人看到 8 道题、后答的人看到 9 道，统计出来
//     的分母根本对不上，而这件事从数据上事后完全看不出来。
//  2. **归属校验**。处理器拿到的是 URL 里的 ID，不校验就等于让任何人凭
//     一个 ID 去改别的项目的题目。每个改子对象的入口都先回查 project_id。

var (
	ErrNotDraft   = errors.New("只有草稿状态的项目可以编辑；已发布后改题会让不同的人答到不同的表")
	ErrNotInProj  = errors.New("该对象不属于此项目")
	ErrLastGroup  = errors.New("至少要保留一个题组")
	ErrBadTimes   = errors.New("时间设置不合法：须满足 开始 < 截止 ≤ 结果开放")
	ErrBadCount   = errors.New("应参加人数须大于 0：它是全部比率的分母")
	ErrBadAPCount = errors.New("AP 数量须在 1~6 之间")
)

// requireDraft 取项目并确认处于草稿态。
func (s *Service) requireDraft(id anon.ID) (*model.Project, error) {
	p, err := s.DB.Project(s.Vault, id)
	if err != nil {
		return nil, err
	}
	if p.Status != model.StatusDraft {
		return nil, fmt.Errorf("%w（当前：%s）", ErrNotDraft, p.Status.Label())
	}
	return p, nil
}

// ── 项目 ────────────────────────────────────────────────────────────

// ProjectForm 是新建/编辑项目的输入。
type ProjectForm struct {
	Name          string
	Intro         string
	StartAt       time.Time
	EndAt         time.Time
	ResultOpenAt  time.Time
	ExpectedCount int
	APCount       int
	GradeScores   []int
}

func (f *ProjectForm) validate() error {
	if strings.TrimSpace(f.Name) == "" {
		return errors.New("项目名称不能为空")
	}
	if !f.StartAt.Before(f.EndAt) || f.ResultOpenAt.Before(f.EndAt) {
		return ErrBadTimes
	}
	if f.ExpectedCount <= 0 {
		return ErrBadCount
	}
	if f.APCount < 1 || f.APCount > 6 {
		return ErrBadAPCount
	}
	if len(f.GradeScores) == 0 {
		f.GradeScores = []int{100, 80, 60, 0}
	}
	return nil
}

// CreateProject 新建项目，并附带一个默认题组——空项目没有题组时，
// 设计器上连"添加题目"的落点都没有，第一步就卡住。
func (s *Service) CreateProject(ctx context.Context, f ProjectForm) (anon.ID, error) {
	if err := f.validate(); err != nil {
		return anon.ID{}, err
	}
	p := &model.Project{
		ID: anon.NewID(), Name: f.Name, Intro: f.Intro,
		Status:  model.StatusDraft,
		StartAt: f.StartAt, EndAt: f.EndAt, ResultOpenAt: f.ResultOpenAt,
		ExpectedCount: f.ExpectedCount, APCount: f.APCount,
		GradeScores: f.GradeScores, CreatedAt: time.Now(),
	}
	if err := s.DB.CreateProject(s.Vault, p); err != nil {
		return anon.ID{}, err
	}
	g := &model.QuestionGroup{
		ID: anon.NewID(), ProjectID: p.ID, Title: "测评内容", SortNo: 0,
	}
	if err := s.DB.CreateGroup(g); err != nil {
		return anon.ID{}, err
	}
	_ = s.DB.Log(&p.ID, "create_project", "ok", "新建项目「"+p.Name+"」")
	return p.ID, nil
}

// UpdateProject 编辑项目基本信息（FR-PRJ-022，仅草稿态）。
func (s *Service) UpdateProject(ctx context.Context, id anon.ID, f ProjectForm) error {
	p, err := s.requireDraft(id)
	if err != nil {
		return err
	}
	if err := f.validate(); err != nil {
		return err
	}
	p.Name, p.Intro = f.Name, f.Intro
	p.StartAt, p.EndAt, p.ResultOpenAt = f.StartAt, f.EndAt, f.ResultOpenAt
	p.ExpectedCount, p.APCount, p.GradeScores = f.ExpectedCount, f.APCount, f.GradeScores
	if err := s.DB.UpdateProject(s.Vault, p); err != nil {
		return err
	}
	s.invalidateStats(id)
	_ = s.DB.Log(&id, "update_project", "ok", "修改项目基本信息")
	return nil
}

// CopyProject 复制为新项目（FR-PRJ-022）：**只复制结构**，
// 不复制作答数据、令牌与名单。
//
// 这是"周期性测评"被删掉之后留下的复用路径（SRS 3.1.2）：模板可以长期
// 保留，但每次测评都是全新的一份数据，与 CON-06「测评结束后本机擦除」
// 不冲突。复制出来的项目一律回到草稿态、时间窗顺延，避免直接沿用上一次
// 的时间而在现场发现已经过期。
func (s *Service) CopyProject(ctx context.Context, srcID anon.ID, newName string) (anon.ID, error) {
	src, err := s.DB.Project(s.Vault, srcID)
	if err != nil {
		return anon.ID{}, err
	}
	form, err := s.DB.Form(s.Vault, src)
	if err != nil {
		return anon.ID{}, err
	}
	if strings.TrimSpace(newName) == "" {
		newName = src.Name + "（副本）"
	}

	now := time.Now()
	dst := &model.Project{
		ID: anon.NewID(), Name: newName, Intro: src.Intro,
		Status:  model.StatusDraft,
		StartAt: now.Add(time.Hour), EndAt: now.Add(25 * time.Hour),
		ResultOpenAt:  now.Add(25 * time.Hour),
		ExpectedCount: src.ExpectedCount, APCount: src.APCount,
		GradeScores: src.GradeScores, CreatedAt: now,
	}
	if err := s.DB.CreateProject(s.Vault, dst); err != nil {
		return anon.ID{}, err
	}

	// 对象要重新分配 ID：题目关联的是对象 ID，两个项目共用一个 ID
	// 会让删除任一方时级联到对方。
	idMap := map[anon.ID]anon.ID{}
	for _, sub := range form.Subjects {
		ns := &model.Subject{
			ID: anon.NewID(), ProjectID: dst.ID,
			Name: sub.Name, Duty: sub.Duty, Tag: sub.Tag, SortNo: sub.SortNo,
		}
		if err := s.DB.CreateSubject(s.Vault, ns); err != nil {
			return anon.ID{}, err
		}
		idMap[sub.ID] = ns.ID
	}
	for _, g := range form.Groups {
		ng := &model.QuestionGroup{
			ID: anon.NewID(), ProjectID: dst.ID,
			Title: g.Title, Intro: g.Intro, SortNo: g.SortNo,
		}
		if err := s.DB.CreateGroup(ng); err != nil {
			return anon.ID{}, err
		}
		for _, q := range g.Questions {
			nq := &model.Question{
				ID: anon.NewID(), GroupID: ng.ID, Kind: q.Kind,
				Title: q.Title, Hint: q.Hint, Required: q.Required,
				SortNo: q.SortNo, Config: q.Config,
			}
			// 选项必须重新分配 ID。沿用原 ID 会撞 question_option 的主键，
			// 使得"复制一个含选择题的项目"直接失败——而演示项目里恰好
			// 没有选择题，这个坑很容易一路留到现场才被踩到。
			for _, o := range q.Options {
				nq.Options = append(nq.Options,
					model.Option{ID: anon.NewID(), Label: o.Label, SortNo: o.SortNo})
			}
			for _, sid := range q.SubjectIDs {
				if mapped, ok := idMap[sid]; ok {
					nq.SubjectIDs = append(nq.SubjectIDs, mapped)
				}
			}
			if err := s.DB.CreateQuestion(dst.ID, nq); err != nil {
				return anon.ID{}, err
			}
		}
	}
	_ = s.DB.Log(&dst.ID, "copy_project", "ok",
		"复制自「"+src.Name+"」，仅复制结构，未复制作答数据、令牌与名单")
	return dst.ID, nil
}

// DeleteProject 删除项目。只允许草稿态：一旦有过提交，删除就是销毁证据。
func (s *Service) DeleteProject(ctx context.Context, id anon.ID) error {
	if _, err := s.requireDraft(id); err != nil {
		return err
	}
	n, err := s.DB.CountAnswers(id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("项目已有 %d 份提交，不可删除", n)
	}
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		return s.DB.PurgeProject(tx, id)
	})
}

// ── 测评对象（FR-FRM-030~032）──────────────────────────────────────

func (s *Service) AddSubject(ctx context.Context, projectID anon.ID,
	name, duty, tag string) error {

	if _, err := s.requireDraft(projectID); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("测评对象姓名不能为空")
	}
	p, err := s.DB.Project(s.Vault, projectID)
	if err != nil {
		return err
	}
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return err
	}
	if len(f.Subjects) >= MaxSubjects {
		return fmt.Errorf("测评对象最多 %d 位（PC-08a）", MaxSubjects)
	}
	return s.DB.CreateSubject(s.Vault, &model.Subject{
		ID: anon.NewID(), ProjectID: projectID,
		Name: name, Duty: duty, Tag: tag, SortNo: len(f.Subjects),
	})
}

func (s *Service) UpdateSubject(ctx context.Context, subjectID anon.ID,
	name, duty, tag string) error {

	pid, err := s.DB.SubjectProject(subjectID)
	if err != nil {
		return err
	}
	if _, err := s.requireDraft(pid); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("测评对象姓名不能为空")
	}
	return s.DB.UpdateSubject(s.Vault, &model.Subject{
		ID: subjectID, ProjectID: pid, Name: name, Duty: duty, Tag: tag,
	})
}

func (s *Service) DeleteSubject(ctx context.Context, subjectID anon.ID) error {
	pid, err := s.DB.SubjectProject(subjectID)
	if err != nil {
		return err
	}
	if _, err := s.requireDraft(pid); err != nil {
		return err
	}
	return s.DB.DeleteSubject(subjectID)
}

// ── 题组与题目（FR-FRM-022 / FR-FRM-023）───────────────────────────

func (s *Service) AddGroup(ctx context.Context, projectID anon.ID, title, intro string) error {
	p, err := s.requireDraft(projectID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" {
		return errors.New("题组名称不能为空")
	}
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return err
	}
	return s.DB.CreateGroup(&model.QuestionGroup{
		ID: anon.NewID(), ProjectID: projectID,
		Title: title, Intro: intro, SortNo: len(f.Groups),
	})
}

func (s *Service) UpdateGroup(ctx context.Context, groupID anon.ID, title, intro string) error {
	pid, err := s.DB.GroupProject(groupID)
	if err != nil {
		return err
	}
	if _, err := s.requireDraft(pid); err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" {
		return errors.New("题组名称不能为空")
	}
	return s.DB.UpdateGroup(&model.QuestionGroup{ID: groupID, Title: title, Intro: intro})
}

func (s *Service) DeleteGroup(ctx context.Context, groupID anon.ID) error {
	pid, err := s.DB.GroupProject(groupID)
	if err != nil {
		return err
	}
	p, err := s.requireDraft(pid)
	if err != nil {
		return err
	}
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return err
	}
	if len(f.Groups) <= 1 {
		return ErrLastGroup
	}
	return s.DB.DeleteGroup(groupID)
}

// QuestionForm 是新增/编辑题目的输入。
type QuestionForm struct {
	GroupID    anon.ID
	Kind       model.QuestionKind
	Title      string
	Hint       string
	Required   bool
	Config     model.QuestionConfig
	Options    []string
	SubjectIDs []anon.ID
	AllSubject bool // 关联全部对象（FR-FRM-031）
}

func (s *Service) validateQuestion(p *model.Project, f *QuestionForm) error {
	if strings.TrimSpace(f.Title) == "" {
		return errors.New("题干不能为空")
	}
	switch f.Kind {
	case model.KindGrade:
		if len(f.Config.Grades) > 0 && !model.ValidGrades(f.Config.Grades) {
			return fmt.Errorf("等级题档数须为 %d~%d 档（FR-FRM-010）",
				model.MinGrades, model.MaxGrades)
		}
	case model.KindSingle, model.KindMulti:
		if len(f.Options) < 2 || len(f.Options) > 10 {
			return errors.New("选择题的选项须为 2~10 个（FR-FRM-012）")
		}
	case model.KindScore, model.KindText:
	default:
		return fmt.Errorf("未知题型 %q", f.Kind)
	}
	return nil
}

// resolveSubjects 把"全部对象"展开成具体 ID 列表。
//
// 存成具体列表而不是一个"全部"标记：标记语义下，发布后新增一个对象会
// 悄悄改变已生成令牌所对应的作答项总数。草稿态展开、发布后冻结，
// 展开后作答项数就是所见即所得的。
func (s *Service) resolveSubjects(p *model.Project, f *QuestionForm) ([]anon.ID, error) {
	if !f.AllSubject {
		return f.SubjectIDs, nil
	}
	form, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return nil, err
	}
	var ids []anon.ID
	for _, sub := range form.Subjects {
		ids = append(ids, sub.ID)
	}
	return ids, nil
}

func (s *Service) AddQuestion(ctx context.Context, projectID anon.ID, qf QuestionForm) error {
	p, err := s.requireDraft(projectID)
	if err != nil {
		return err
	}
	if err := s.validateQuestion(p, &qf); err != nil {
		return err
	}
	// 归属校验：题组必须属于本项目
	if pid, err := s.DB.GroupProject(qf.GroupID); err != nil {
		return err
	} else if pid != projectID {
		return ErrNotInProj
	}
	form, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return err
	}
	n := 0
	for _, g := range form.Groups {
		n += len(g.Questions)
	}
	if n >= MaxQuestions {
		return fmt.Errorf("题目最多 %d 道（PC-08b）", MaxQuestions)
	}
	sids, err := s.resolveSubjects(p, &qf)
	if err != nil {
		return err
	}
	q := &model.Question{
		ID: anon.NewID(), GroupID: qf.GroupID, Kind: qf.Kind,
		Title: qf.Title, Hint: qf.Hint, Required: qf.Required,
		SortNo: n, Config: qf.Config, SubjectIDs: sids,
	}
	for _, o := range qf.Options {
		q.Options = append(q.Options, model.Option{ID: anon.NewID(), Label: o})
	}
	if err := s.DB.CreateQuestion(projectID, q); err != nil {
		return err
	}
	s.invalidateStats(projectID)
	return nil
}

func (s *Service) UpdateQuestion(ctx context.Context, questionID anon.ID, qf QuestionForm) error {
	pid, err := s.DB.QuestionProject(questionID)
	if err != nil {
		return err
	}
	p, err := s.requireDraft(pid)
	if err != nil {
		return err
	}
	if err := s.validateQuestion(p, &qf); err != nil {
		return err
	}
	sids, err := s.resolveSubjects(p, &qf)
	if err != nil {
		return err
	}
	q := &model.Question{
		ID: questionID, Kind: qf.Kind, Title: qf.Title, Hint: qf.Hint,
		Required: qf.Required, Config: qf.Config, SubjectIDs: sids,
	}
	for _, o := range qf.Options {
		q.Options = append(q.Options, model.Option{ID: anon.NewID(), Label: o})
	}
	// 选项与关联是整体重写，必须在一个事务里：中途失败会留下一道
	// 既没有选项也没有关联对象的题，而它在作答页上是一片空白。
	if err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
		return s.DB.UpdateQuestion(tx, q)
	}); err != nil {
		return err
	}
	s.invalidateStats(pid)
	return nil
}

func (s *Service) DeleteQuestion(ctx context.Context, questionID anon.ID) error {
	pid, err := s.DB.QuestionProject(questionID)
	if err != nil {
		return err
	}
	if _, err := s.requireDraft(pid); err != nil {
		return err
	}
	if err := s.DB.DeleteQuestion(questionID); err != nil {
		return err
	}
	s.invalidateStats(pid)
	return nil
}

// ── 排序（FR-FRM-022 / FR-FRM-023）─────────────────────────────────

// ReorderQuestions 按给定顺序重排某题组内的题目，并可跨组移动。
//
// 接受**完整的目标顺序**而不是"上移一位"：拖拽产生的是一个新顺序，
// 逐次交换需要前端自己算出交换序列，而任何一次请求丢失都会让顺序错乱
// 且无从发现。整体覆盖是幂等的，重发一次结果相同。
func (s *Service) ReorderQuestions(ctx context.Context, projectID, groupID anon.ID,
	ordered []anon.ID) error {

	if _, err := s.requireDraft(projectID); err != nil {
		return err
	}
	if pid, err := s.DB.GroupProject(groupID); err != nil {
		return err
	} else if pid != projectID {
		return ErrNotInProj
	}
	for i, qid := range ordered {
		if pid, err := s.DB.QuestionProject(qid); err != nil {
			return err
		} else if pid != projectID {
			return ErrNotInProj
		}
		if err := s.DB.SetQuestionSort(qid, i, groupID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ReorderGroups(ctx context.Context, projectID anon.ID, ordered []anon.ID) error {
	if _, err := s.requireDraft(projectID); err != nil {
		return err
	}
	for i, gid := range ordered {
		if pid, err := s.DB.GroupProject(gid); err != nil {
			return err
		} else if pid != projectID {
			return ErrNotInProj
		}
		if err := s.DB.SetGroupSort(gid, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ReorderSubjects(ctx context.Context, projectID anon.ID, ordered []anon.ID) error {
	if _, err := s.requireDraft(projectID); err != nil {
		return err
	}
	for i, sid := range ordered {
		if pid, err := s.DB.SubjectProject(sid); err != nil {
			return err
		} else if pid != projectID {
			return ErrNotInProj
		}
		if err := s.DB.SetSubjectSort(sid, i); err != nil {
			return err
		}
	}
	return nil
}
