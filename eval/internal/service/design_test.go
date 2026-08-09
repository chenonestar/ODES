package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
)

func okForm(name string) ProjectForm {
	now := time.Now()
	return ProjectForm{
		Name: name, StartAt: now.Add(time.Hour), EndAt: now.Add(2 * time.Hour),
		ResultOpenAt: now.Add(3 * time.Hour), ExpectedCount: 30, APCount: 1,
	}
}

func newDraft(t *testing.T, s *Service) anon.ID {
	t.Helper()
	id, err := s.CreateProject(context.Background(), okForm("测试项目"))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func formOf(t *testing.T, s *Service, id anon.ID) *model.Form {
	t.Helper()
	p, err := s.DB.Project(s.Vault, id)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// 新建项目必须自带一个题组：没有题组时设计器上连"添加题目"的落点
// 都没有，第一步就卡住。
func TestCreateProjectHasDefaultGroup(t *testing.T) {
	s := newSvc(t)
	id := newDraft(t, s)
	f := formOf(t, s, id)
	if len(f.Groups) != 1 {
		t.Fatalf("新建项目应自带 1 个题组，得到 %d 个", len(f.Groups))
	}
}

func TestProjectFormValidation(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	now := time.Now()

	cases := []struct {
		name string
		mut  func(*ProjectForm)
		want error
	}{
		{"截止早于开始", func(f *ProjectForm) { f.EndAt = now.Add(-time.Hour) }, ErrBadTimes},
		{"结果开放早于截止", func(f *ProjectForm) { f.ResultOpenAt = now }, ErrBadTimes},
		{"应参加人数为 0", func(f *ProjectForm) { f.ExpectedCount = 0 }, ErrBadCount},
		{"AP 数量越界", func(f *ProjectForm) { f.APCount = 7 }, ErrBadAPCount},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := okForm("x")
			c.mut(&f)
			if _, err := s.CreateProject(ctx, f); !errors.Is(err, c.want) {
				t.Fatalf("应返回 %v，得到 %v", c.want, err)
			}
		})
	}
}

// 这是本文件里最要紧的一条：已发布之后不得再改题。
//
// 发布后改题意味着不同的人答的不是同一张表——先答的人看到 8 道题、
// 后答的人看到 9 道，统计分母对不上，而这件事从数据上事后完全看不出来。
func TestEditingBlockedAfterPublish(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	id := newDraft(t, s)
	if err := s.AddSubject(ctx, id, "张三", "局长", ""); err != nil {
		t.Fatal(err)
	}
	f := formOf(t, s, id)
	gid := f.Groups[0].ID
	if err := s.AddQuestion(ctx, id, QuestionForm{
		GroupID: gid, Kind: model.KindGrade, Title: "德", Required: true,
		AllSubject: true,
	}); err != nil {
		t.Fatal(err)
	}
	f = formOf(t, s, id)
	qid := f.Groups[0].Questions[0].ID
	sid := f.Subjects[0].ID

	// 直接改状态，绕开前置校验——这里测的是编辑闸门，不是发布流程
	if err := s.DB.UpdateProjectStatus(nil, id, model.StatusPublished,
		time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	ops := map[string]func() error{
		"改项目": func() error { return s.UpdateProject(ctx, id, okForm("改名")) },
		"加对象": func() error { return s.AddSubject(ctx, id, "李四", "", "") },
		"改对象": func() error { return s.UpdateSubject(ctx, sid, "李四", "", "") },
		"删对象": func() error { return s.DeleteSubject(ctx, sid) },
		"加题组": func() error { return s.AddGroup(ctx, id, "新组", "") },
		"改题组": func() error { return s.UpdateGroup(ctx, gid, "改名", "") },
		"删题组": func() error { return s.DeleteGroup(ctx, gid) },
		"加题": func() error {
			return s.AddQuestion(ctx, id, QuestionForm{GroupID: gid, Kind: model.KindGrade, Title: "能"})
		},
		"改题":   func() error { return s.UpdateQuestion(ctx, qid, QuestionForm{Kind: model.KindGrade, Title: "改"}) },
		"删题":   func() error { return s.DeleteQuestion(ctx, qid) },
		"题目排序": func() error { return s.ReorderQuestions(ctx, id, gid, []anon.ID{qid}) },
		"题组排序": func() error { return s.ReorderGroups(ctx, id, []anon.ID{gid}) },
		"对象排序": func() error { return s.ReorderSubjects(ctx, id, []anon.ID{sid}) },
		"导入名单": func() error { return s.ImportRoster(ctx, id, []string{"张三"}) },
		"删除项目": func() error { return s.DeleteProject(ctx, id) },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, ErrNotDraft) {
			t.Errorf("已发布状态下「%s」应被拒绝（ErrNotDraft），得到：%v", name, err)
		}
	}
}

// 归属校验：不得凭一个 ID 去改别的项目的题。
func TestCannotEditAcrossProjects(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	a := newDraft(t, s)
	b, err := s.CreateProject(ctx, okForm("另一个项目"))
	if err != nil {
		t.Fatal(err)
	}
	fb := formOf(t, s, b)
	otherGroup := fb.Groups[0].ID

	// 用项目 A 的 ID + 项目 B 的题组
	err = s.AddQuestion(ctx, a, QuestionForm{
		GroupID: otherGroup, Kind: model.KindGrade, Title: "越权",
	})
	if !errors.Is(err, ErrNotInProj) {
		t.Fatalf("跨项目加题应被拒绝，得到 %v", err)
	}
	if err := s.ReorderGroups(ctx, a, []anon.ID{otherGroup}); !errors.Is(err, ErrNotInProj) {
		t.Fatalf("跨项目排序应被拒绝，得到 %v", err)
	}
}

// FR-PRJ-022：复制只复制结构，不复制作答数据、令牌与名单。
func TestCopyProjectStructureOnly(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	src, toks := fixture(t, s, 3)

	// 造一份提交与一份名单，确认都不会被复制过去
	if err := s.Submit(ctx, toks[0], gradeAnswer(t, s, src, 0)); err != nil {
		t.Fatal(err)
	}
	p, _ := s.DB.Project(s.Vault, src)
	_ = s.DB.InsertRoster(s.Vault, src, "张三")

	dstID, err := s.CopyProject(ctx, src, "复制出来的项目")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := s.DB.Project(s.Vault, dstID)
	if err != nil {
		t.Fatal(err)
	}

	if dst.Status != model.StatusDraft {
		t.Errorf("复制出的项目应为草稿态，得到 %s", dst.Status)
	}
	if n, _ := s.DB.CountAnswers(dstID); n != 0 {
		t.Errorf("不应复制作答数据，得到 %d 份", n)
	}
	if total, _, _ := s.DB.CountTokens(dstID); total != 0 {
		t.Errorf("不应复制令牌，得到 %d 个", total)
	}
	if n, _ := s.DB.CountRoster(dstID); n != 0 {
		t.Errorf("不应复制名单，得到 %d 条", n)
	}

	// 结构要复制过去
	sf, df := formOf(t, s, src), formOf(t, s, dstID)
	if len(df.Subjects) != len(sf.Subjects) || len(df.Groups) != len(sf.Groups) {
		t.Fatalf("结构未完整复制：对象 %d/%d，题组 %d/%d",
			len(df.Subjects), len(sf.Subjects), len(df.Groups), len(sf.Groups))
	}
	// 对象 ID 必须是新的：共用 ID 会让删除任一方时级联到对方
	for i := range df.Subjects {
		if df.Subjects[i].ID == sf.Subjects[i].ID {
			t.Fatal("复制出的测评对象沿用了原 ID，删除任一项目会级联影响另一个")
		}
	}
	// 题目关联要指向**新的**对象 ID
	for _, g := range df.Groups {
		for _, q := range g.Questions {
			for _, sid := range q.SubjectIDs {
				if df.Subject(sid) == nil {
					t.Fatalf("题目「%s」关联到了不属于新项目的对象", q.Title)
				}
			}
		}
	}
	_ = p
}

// 拖拽产生的是一个新顺序，因此接口接受完整顺序而不是"上移一位"。
// 整体覆盖是幂等的，重发一次结果相同。
func TestReorderIsIdempotentAndComplete(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	id := newDraft(t, s)
	gid := formOf(t, s, id).Groups[0].ID
	for _, title := range []string{"甲", "乙", "丙"} {
		if err := s.AddQuestion(ctx, id, QuestionForm{
			GroupID: gid, Kind: model.KindGrade, Title: title,
		}); err != nil {
			t.Fatal(err)
		}
	}
	qs := formOf(t, s, id).Groups[0].Questions
	want := []anon.ID{qs[2].ID, qs[0].ID, qs[1].ID}

	for i := 0; i < 2; i++ { // 跑两遍验证幂等
		if err := s.ReorderQuestions(ctx, id, gid, want); err != nil {
			t.Fatal(err)
		}
		got := formOf(t, s, id).Groups[0].Questions
		for j := range want {
			if got[j].ID != want[j] {
				t.Fatalf("第 %d 遍排序后第 %d 题为 %s，应为 %s",
					i+1, j, got[j].Title, qs[j].Title)
			}
		}
	}
	if got := formOf(t, s, id).Groups[0].Questions[0].Title; got != "丙" {
		t.Fatalf("首题应为丙，得到 %s", got)
	}
}

// 至少保留一个题组：删光之后设计器上没有任何落点可以再加题。
func TestCannotDeleteLastGroup(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	id := newDraft(t, s)
	gid := formOf(t, s, id).Groups[0].ID
	if err := s.DeleteGroup(ctx, gid); !errors.Is(err, ErrLastGroup) {
		t.Fatalf("删除最后一个题组应被拒绝，得到 %v", err)
	}
	if err := s.AddGroup(ctx, id, "第二组", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroup(ctx, gid); err != nil {
		t.Fatalf("还有两个题组时应可删除：%v", err)
	}
}

func TestQuestionValidation(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	id := newDraft(t, s)
	gid := formOf(t, s, id).Groups[0].ID

	cases := []struct {
		name string
		qf   QuestionForm
		want string
	}{
		{"题干为空", QuestionForm{GroupID: gid, Kind: model.KindGrade}, "题干"},
		{"选项过少", QuestionForm{GroupID: gid, Kind: model.KindSingle, Title: "选一个",
			Options: []string{"甲"}}, "2~10"},
		{"选项过多", QuestionForm{GroupID: gid, Kind: model.KindMulti, Title: "多选",
			Options: make([]string, 11)}, "2~10"},
		{"档数越界", QuestionForm{GroupID: gid, Kind: model.KindGrade, Title: "档",
			Config: model.QuestionConfig{Grades: []string{"优", "良"}}}, "档"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.AddQuestion(ctx, id, c.qf)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("应因 %q 被拒，得到 %v", c.want, err)
			}
		})
	}
}

// FR-FRM-031：关联"全部对象"在草稿态就展开成具体列表。
//
// 存成"全部"标记的话，发布后新增一个对象会悄悄改变已生成令牌对应的
// 作答项总数；展开后冻结，作答项数才是所见即所得的。
func TestAllSubjectsExpandsAtDesignTime(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	id := newDraft(t, s)
	for _, n := range []string{"张三", "李四"} {
		if err := s.AddSubject(ctx, id, n, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	gid := formOf(t, s, id).Groups[0].ID
	if err := s.AddQuestion(ctx, id, QuestionForm{
		GroupID: gid, Kind: model.KindGrade, Title: "德", AllSubject: true,
	}); err != nil {
		t.Fatal(err)
	}
	q := formOf(t, s, id).Groups[0].Questions[0]
	if len(q.SubjectIDs) != 2 {
		t.Fatalf("应展开为 2 个对象，得到 %d 个", len(q.SubjectIDs))
	}

	// 再加一个对象，已存在的题目不应自动扩张
	if err := s.AddSubject(ctx, id, "王五", "", ""); err != nil {
		t.Fatal(err)
	}
	q = formOf(t, s, id).Groups[0].Questions[0]
	if len(q.SubjectIDs) != 2 {
		t.Fatalf("新增对象后旧题目关联数应保持 2，得到 %d——"+
			"自动扩张会让展开后作答项数与设计器上看到的不一致", len(q.SubjectIDs))
	}
}

// 复制含选择题的项目。
//
// 这条是真机测试逼出来的：CopyProject 原先直接沿用了原题的选项对象，
// 连同它们的主键一起复制，于是第二次 INSERT 撞上 question_option 的
// 主键约束，整个复制失败。演示项目里恰好没有选择题，这个坑很容易
// 一路留到现场——那时管理员想做的正是"照着上次那份复制一份"。
func TestCopyProjectWithChoiceQuestions(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	id := newDraft(t, s)
	if err := s.AddSubject(ctx, id, "张三", "", ""); err != nil {
		t.Fatal(err)
	}
	gid := formOf(t, s, id).Groups[0].ID
	if err := s.AddQuestion(ctx, id, QuestionForm{
		GroupID: gid, Kind: model.KindSingle, Title: "最突出方面",
		Options: []string{"担当作为", "廉洁自律", "群众口碑"}, AllSubject: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddQuestion(ctx, id, QuestionForm{
		GroupID: gid, Kind: model.KindMulti, Title: "需改进方面",
		Options: []string{"统筹协调", "创新意识"}, AllSubject: true,
	}); err != nil {
		t.Fatal(err)
	}

	dst, err := s.CopyProject(ctx, id, "副本")
	if err != nil {
		t.Fatalf("复制含选择题的项目失败：%v", err)
	}

	// 选项要完整带过去，且主键必须是新的
	srcOpts := map[anon.ID]bool{}
	for _, g := range formOf(t, s, id).Groups {
		for _, q := range g.Questions {
			for _, o := range q.Options {
				srcOpts[o.ID] = true
			}
		}
	}
	n := 0
	for _, g := range formOf(t, s, dst).Groups {
		for _, q := range g.Questions {
			if len(q.Options) == 0 && (q.Kind == model.KindSingle || q.Kind == model.KindMulti) {
				t.Errorf("题目「%s」的选项没有被复制", q.Title)
			}
			for _, o := range q.Options {
				n++
				if srcOpts[o.ID] {
					t.Errorf("选项「%s」沿用了原项目的主键，会撞 question_option 的唯一约束",
						o.Label)
				}
			}
		}
	}
	if n != 5 {
		t.Errorf("应复制 5 个选项，实际 %d 个", n)
	}

	// 复制两次也要能成：管理员照着同一份模板连开两场测评是常态
	if _, err := s.CopyProject(ctx, id, "副本二"); err != nil {
		t.Fatalf("第二次复制失败：%v", err)
	}
}
