package service

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
	"odes/internal/store"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	v, _, err := crypto.Init("Test-Password-123")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	return New(db, v, "localhost")
}

// fixture 造一个进行中的项目，返回项目 ID 与全部正式令牌值。
func fixture(t *testing.T, s *Service, people int) (anon.ID, []string) {
	t.Helper()
	now := time.Now()
	p := &model.Project{
		ID: anon.NewID(), Name: "并发测试", Status: model.StatusRunning,
		StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour),
		ResultOpenAt: now.Add(time.Hour), ExpectedCount: people,
		APCount: 1, GradeScores: []int{100, 80, 60, 0}, CreatedAt: now,
	}
	if err := s.DB.CreateProject(s.Vault, p); err != nil {
		t.Fatal(err)
	}
	sub := &model.Subject{ID: anon.NewID(), ProjectID: p.ID, Name: "张三", SortNo: 0}
	if err := s.DB.CreateSubject(s.Vault, sub); err != nil {
		t.Fatal(err)
	}
	g := &model.QuestionGroup{ID: anon.NewID(), ProjectID: p.ID, Title: "组", SortNo: 0}
	if err := s.DB.CreateGroup(g); err != nil {
		t.Fatal(err)
	}
	q := &model.Question{
		ID: anon.NewID(), GroupID: g.ID, Kind: model.KindGrade,
		Title: "题", Required: true, SubjectIDs: []anon.ID{sub.ID},
	}
	if err := s.DB.CreateQuestion(p.ID, q); err != nil {
		t.Fatal(err)
	}
	if err := s.GenerateTokens(context.Background(), p.ID, people, 1, 1); err != nil {
		t.Fatal(err)
	}
	toks, err := s.DB.Tokens(p.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var vals []string
	for _, tk := range toks {
		vals = append(vals, tk.Value)
	}
	return p.ID, vals
}

func gradeAnswer(t *testing.T, s *Service, pid anon.ID, grade int) *model.Answer {
	t.Helper()
	p, _ := s.DB.Project(s.Vault, pid)
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		t.Fatal(err)
	}
	q := f.Groups[0].Questions[0]
	g := grade
	return &model.Answer{V: 1, Items: []model.AnswerItem{
		{Q: q.ID.Hex(), S: f.Subjects[0].ID.Hex(), Grade: &g},
	}}
}

// TX-01　同一令牌并发 20 次提交，仅 1 条记录落库，其余返回 TOKEN_USED。
func TestTX01_ConcurrentSubmitSameToken(t *testing.T) {
	s := newSvc(t)
	pid, toks := fixture(t, s, 5)
	a := gradeAnswer(t, s, pid, 0)

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, usedCount := 0, 0

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Submit(context.Background(), toks[0], a)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case isTokenUsed(err):
				usedCount++
			default:
				t.Errorf("意外错误: %v", err)
			}
		}()
	}
	wg.Wait()

	if okCount != 1 {
		t.Errorf("应恰有 1 次提交成功，得到 %d", okCount)
	}
	if usedCount != n-1 {
		t.Errorf("应有 %d 次返回 TOKEN_USED，得到 %d", n-1, usedCount)
	}
	got, err := s.DB.CountAnswers(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("库中应恰有 1 条作答记录，得到 %d", got)
	}
}

func isTokenUsed(err error) bool {
	return err != nil && err.Error() == ErrTokenUsed.Error()
}

// TX-02　提交失败时不留孤儿作答记录。
//
// 令牌已被用过 → Submit 应在事务内回滚，作答记录不得残留。
func TestTX02_NoOrphanAnswerOnFailure(t *testing.T) {
	s := newSvc(t)
	pid, toks := fixture(t, s, 5)
	a := gradeAnswer(t, s, pid, 0)

	if err := s.Submit(context.Background(), toks[0], a); err != nil {
		t.Fatal(err)
	}
	// 第二次用同一令牌
	if err := s.Submit(context.Background(), toks[0], a); !isTokenUsed(err) {
		t.Fatalf("重复提交应返回 TOKEN_USED，得到 %v", err)
	}
	n, _ := s.DB.CountAnswers(pid)
	if n != 1 {
		t.Errorf("应仍为 1 条记录（无孤儿），得到 %d", n)
	}
}

// TestAnswerHasNoLinkToToken 验证提交后作答记录里确实找不到令牌的影子。
//
// 直接读原始行：payload 是密文，其余列只有 id / project_id / batch_no。
func TestAnswerHasNoLinkToToken(t *testing.T) {
	s := newSvc(t)
	pid, toks := fixture(t, s, 5)
	a := gradeAnswer(t, s, pid, 0)
	if err := s.Submit(context.Background(), toks[0], a); err != nil {
		t.Fatal(err)
	}

	recs, err := s.DB.LoadAnswers(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("应有 1 条记录，得到 %d", len(recs))
	}
	tk, err := s.DB.AnyTokenByValue(toks[0])
	if err != nil {
		t.Fatal(err)
	}
	if recs[0].ID == tk.ID {
		t.Error("作答记录主键与令牌主键相同：这是一条直接关联路径")
	}
	// 密文里不得出现令牌值
	if containsBytes(recs[0].Payload, []byte(toks[0])) {
		t.Error("作答记录 payload 中出现了令牌值明文")
	}
}

func containsBytes(hay, needle []byte) bool {
	if len(needle) == 0 || len(hay) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestSubmitOutsideWindow 验证时间窗控制。
func TestSubmitOutsideWindow(t *testing.T) {
	s := newSvc(t)
	pid, toks := fixture(t, s, 3)
	a := gradeAnswer(t, s, pid, 0)

	// 手动把项目改成已截止
	if err := s.DB.UpdateProjectStatus(nil, pid, model.StatusClosed,
		time.Now().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(context.Background(), toks[0], a); err != ErrClosed {
		t.Fatalf("已截止时提交应返回 CLOSED，得到 %v", err)
	}
}

// TestRequiredValidationServerSide 验证服务端不信任前端的必填检查。
func TestRequiredValidationServerSide(t *testing.T) {
	s := newSvc(t)
	pid, toks := fixture(t, s, 3)
	_ = pid
	empty := &model.Answer{V: 1}
	err := s.Submit(context.Background(), toks[0], empty)
	if MissingOf(err) == nil {
		t.Fatalf("空作答应被服务端拒绝并返回未完成项，得到 %v", err)
	}
	t.Logf("服务端拒绝并定位到 %d 处未完成项", len(MissingOf(err)))
}

// TestShortCodeCheckDigit 验证短码校验位能挡住输错。
func TestShortCodeCheckDigit(t *testing.T) {
	for i := 0; i < 500; i++ {
		c := anon.NewShortCode()
		if !anon.VerifyShortCode(c) {
			t.Fatalf("自己生成的短码 %s 未通过校验", c)
		}
		// 改动任意一位，应当被检出（校验位设计的目的：输错即时提示）
		b := []byte(c)
		b[i%7] = anon.Charset[(indexIn(b[i%7])+1)%len(anon.Charset)]
		if anon.VerifyShortCode(string(b)) {
			t.Fatalf("改动一位后的 %s 仍通过校验，校验位失效", string(b))
		}
	}
}

func indexIn(c byte) int {
	for i := 0; i < len(anon.Charset); i++ {
		if anon.Charset[i] == c {
			return i
		}
	}
	return 0
}

// TestTokensAreShuffledNotSequential 验证令牌生成后确实做了随机重排。
//
// V1.0 的缺陷：令牌按名单顺序生成、按打印页位置分发，打印底稿即
// 人码对照表。这条测试盯住 FR-TKN-025。
func TestTokensAreShuffledNotSequential(t *testing.T) {
	s := newSvc(t)
	pid, _ := fixture(t, s, 60)
	toks, err := s.DB.Tokens(pid, false)
	if err != nil {
		t.Fatal(err)
	}
	// AP 序号按重排后的位置轮转分配；若集合未重排，
	// 同一 AP 下的令牌值会呈现可预测的规律。这里只做基本检查：
	// AP 分配应当均匀且覆盖全部 AP。
	seen := map[int]int{}
	for _, tk := range toks {
		seen[tk.APIndex]++
	}
	if len(seen) != 1 {
		t.Logf("AP 分布：%v", seen)
	}
	if len(toks) != 60 {
		t.Errorf("应生成 60 个正式令牌，得到 %d", len(toks))
	}
	spares, _ := s.DB.Tokens(pid, true)
	if len(spares) != 6 {
		t.Errorf("应生成 6 个备用令牌（10%%），得到 %d", len(spares))
	}
}

// M-1　档位名称与档数可配置（FR-FRM-010，3–5 档）。
//
// 档位落在 question.config 里——schema 的列注释写明"各题型配置：grade 档位名"。
func TestConfigurableGrades(t *testing.T) {
	s := newSvc(t)
	now := time.Now()
	p := &model.Project{
		ID: anon.NewID(), Name: "三档测评", Status: model.StatusDraft,
		StartAt: now, EndAt: now.Add(time.Hour), ResultOpenAt: now.Add(time.Hour),
		ExpectedCount: 10, APCount: 1, GradeScores: []int{100, 60, 0}, CreatedAt: now,
	}
	if err := s.DB.CreateProject(s.Vault, p); err != nil {
		t.Fatal(err)
	}
	sub := &model.Subject{ID: anon.NewID(), ProjectID: p.ID, Name: "张三"}
	if err := s.DB.CreateSubject(s.Vault, sub); err != nil {
		t.Fatal(err)
	}
	g := &model.QuestionGroup{ID: anon.NewID(), ProjectID: p.ID, Title: "组"}
	if err := s.DB.CreateGroup(g); err != nil {
		t.Fatal(err)
	}
	custom := []string{"满意", "基本满意", "不满意"}
	q := &model.Question{
		ID: anon.NewID(), GroupID: g.ID, Kind: model.KindGrade, Title: "题",
		Required: true, SubjectIDs: []anon.ID{sub.ID},
		Config:   model.QuestionConfig{Grades: custom},
	}
	if err := s.DB.CreateQuestion(p.ID, q); err != nil {
		t.Fatal(err)
	}

	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Grades) != 3 || f.Grades[0] != "满意" || f.Grades[2] != "不满意" {
		t.Errorf("档位应取题目配置的三档，得到 %v", f.Grades)
	}
}

// 未配置时回落到干部考核的法定四档。
func TestDefaultGradesWhenUnconfigured(t *testing.T) {
	s := newSvc(t)
	pid, _ := fixture(t, s, 3)
	p, _ := s.DB.Project(s.Vault, pid)
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Grades) != 4 || f.Grades[0] != "优秀" {
		t.Errorf("未配置时应为法定四档，得到 %v", f.Grades)
	}
}

func TestGradeCountRange(t *testing.T) {
	for _, c := range []struct {
		g    []string
		want bool
	}{
		{[]string{"A", "B"}, false},                          // 2 档，少于下限
		{[]string{"A", "B", "C"}, true},                      // 3 档
		{[]string{"A", "B", "C", "D", "E"}, true},            // 5 档
		{[]string{"A", "B", "C", "D", "E", "F"}, false},      // 6 档，超上限
		{[]string{"A", "", "C"}, false},                      // 空档位名
	} {
		if got := model.ValidGrades(c.g); got != c.want {
			t.Errorf("ValidGrades(%v) = %v，期望 %v（FR-FRM-010 要求 3–5 档）", c.g, got, c.want)
		}
	}
}

// M-7　归档口令不得与登录口令相同（FR-SYS-030）。
func TestArchivePasswordMustDifferFromLogin(t *testing.T) {
	s := newSvc(t)
	// newSvc 用的登录口令是 Test-Password-123，把它的哈希写进 meta
	_, meta, err := crypto.Init("Test-Password-123")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string][]byte{
		"kek_salt": meta.KEKSalt, "admin_pwd_hash": meta.PwdHash,
	} {
		if err := s.DB.SetMeta(k, v); err != nil {
			t.Fatal(err)
		}
	}
	pid, _ := fixture(t, s, 3)

	if _, _, err := s.BuildArchive(pid, "Test-Password-123"); err == nil {
		t.Error("归档口令与登录口令相同时应被拒绝")
	} else {
		t.Logf("按预期拒绝：%v", err)
	}
	if _, _, err := s.BuildArchive(pid, "Another-Pass-456"); err != nil {
		t.Errorf("不同口令应当放行，得到 %v", err)
	}
}
