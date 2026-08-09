// Package stats 是内存聚合管线，也是**全部统计口径规则的唯一归属地**。
//
// 为什么是内存聚合（ADR-003）：作答内容以 AES-GCM 加密后整份存入
// answer_record.payload，SQL 无法对其 GROUP BY / COUNT / AVG。
//
// 为什么口径必须单点归属（HLD 7.3）：PDF 导出、Excel 导出、统计页三个
// 出口都调用同一份 Result，不得各自计算。口径不一致是这类系统最典型的
// 事故——报告里写 92.3%、Excel 里算出 91.8%，一旦出现就直接摧毁整套
// 数据的可信度。恒等式断言因此设为硬失败：宁可导出失败，也不能输出
// 自相矛盾的报表。
package stats

import (
	"encoding/json"
	"fmt"
	"sort"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
)

// 四档等级的固定下标。GradeAbstain(-1) 单列，不占用这四个位置。
const (
	GradeExcellent = 0 // 优秀
	GradeCompetent = 1 // 称职
	GradeBasic     = 2 // 基本称职
	GradeIncompt   = 3 // 不称职
	NumGrades      = 4
)

// Counter 是一个 (题目, 对象) 桶的计数器。
type Counter struct {
	Grades  [NumGrades]int
	Abstain int // 显式弃权 + 选填未作答
	Scores  []int
	Options map[string]int
	Texts   []string
}

// Cell 是一个 (题目, 对象) 的最终统计结果。
type Cell struct {
	QuestionID   anon.ID
	SubjectID    anon.ID
	QuestionName string
	SubjectName  string
	Kind         model.QuestionKind

	Counts   [NumGrades]int
	Abstain  int // 含未提交部分
	Rates    [NumGrades]float64
	RateAbst float64

	// 主指标（SRS 6.4）
	RateExcellent float64 // 优秀率
	RateCompAbove float64 // 称职以上率
	Weighted      float64 // 加权得分，仅辅助排序

	// 打分题
	ScoreAvg, ScoreMedian float64
	ScoreMin, ScoreMax    int

	// 选择题 / 评语
	OptionCounts map[string]int
	OptionLabels []string
	Texts        []string
}

// Result 是一次聚合的完整产物。三个出口共用。
type Result struct {
	ProjectName   string
	ExpectedCount int // 标准口径分母
	Submitted     int // 实到口径分母
	PaperEntry    int
	SubmitRate    float64
	Grades        []string
	GradeScores   []int
	Cells         []Cell
}

// Aggregate 执行完整管线：解密 → 重排 → 分桶 → 应用口径 → 恒等式断言。
//
// 解密次数等于**份数**而非作答项数（LLD 1.2 修正一）：一份作答是一个
// 整体密文，200 份即 200 次解密，而非 8 万次。
func Aggregate(p *model.Project, f *model.Form, recs []model.AnswerRecord,
	v *crypto.Vault) (*Result, error) {

	plains := make([]model.Answer, 0, len(recs))
	for _, r := range recs {
		b, err := v.Open(r.Payload, r.ProjectID.Bytes())
		if err != nil {
			return nil, fmt.Errorf("解密作答记录失败: %w", err)
		}
		var a model.Answer
		if err := json.Unmarshal(b, &a); err != nil {
			return nil, fmt.Errorf("解析作答载荷失败: %w", err)
		}
		plains = append(plains, a)
	}

	// 切断与读取顺序的关联。即便物理顺序已随机，也不把它带进后续流程。
	anon.Shuffle(plains)

	type key struct{ q, s string }
	buckets := map[key]*Counter{}
	get := func(k key) *Counter {
		c, ok := buckets[k]
		if !ok {
			c = &Counter{Options: map[string]int{}}
			buckets[k] = c
		}
		return c
	}

	for _, a := range plains {
		for _, it := range a.Items {
			c := get(key{it.Q, it.S})
			switch {
			case it.Grade != nil:
				g := *it.Grade
				if g == model.GradeAbstain || g < 0 || g >= NumGrades {
					c.Abstain++
				} else {
					c.Grades[g]++
				}
			case it.Score != nil:
				c.Scores = append(c.Scores, *it.Score)
			case it.Opt != "":
				c.Options[it.Opt]++
			case len(it.Opts) > 0:
				for _, o := range it.Opts {
					c.Options[o]++
				}
			case it.Text != "":
				c.Texts = append(c.Texts, it.Text)
			}
		}
	}

	res := &Result{
		ProjectName:   p.Name,
		ExpectedCount: p.ExpectedCount,
		Submitted:     len(recs),
		PaperEntry:    p.PaperEntryCount,
		Grades:        f.Grades,
		GradeScores:   p.GradeScores,
	}
	if p.ExpectedCount > 0 {
		res.SubmitRate = round1(float64(len(recs)) * 100 / float64(p.ExpectedCount))
	}

	for _, g := range f.Groups {
		for _, q := range g.Questions {
			for _, sid := range q.SubjectIDs {
				c := buckets[key{q.ID.Hex(), sid.Hex()}]
				if c == nil {
					c = &Counter{Options: map[string]int{}}
				}
				cell, err := finalize(p, f, &q, sid, c, len(recs))
				if err != nil {
					return nil, err
				}
				res.Cells = append(res.Cells, *cell)
			}
		}
	}
	return res, nil
}

// finalize 把一个桶的原始计数转成最终指标，并施加全部口径规则。
func finalize(p *model.Project, f *model.Form, q *model.Question, sid anon.ID,
	c *Counter, submitted int) (*Cell, error) {

	cell := Cell{
		QuestionID: q.ID, SubjectID: sid, QuestionName: q.Title, Kind: q.Kind,
		Counts: c.Grades, OptionCounts: c.Options, Texts: c.Texts,
	}
	if s := f.Subject(sid); s != nil {
		cell.SubjectName = s.Name
	}
	for _, o := range q.Options {
		cell.OptionLabels = append(cell.OptionLabels, o.Label)
	}

	switch q.Kind {
	case model.KindScore:
		if n := len(c.Scores); n > 0 {
			sorted := append([]int(nil), c.Scores...)
			sort.Ints(sorted)
			sum := 0
			for _, s := range sorted {
				sum += s
			}
			cell.ScoreAvg = round1(float64(sum) / float64(n))
			cell.ScoreMin, cell.ScoreMax = sorted[0], sorted[n-1]
			if n%2 == 1 {
				cell.ScoreMedian = float64(sorted[n/2])
			} else {
				cell.ScoreMedian = float64(sorted[n/2-1]+sorted[n/2]) / 2
			}
		}
		return &cell, nil

	case model.KindGrade:
		// —— 口径规则集中在这里（SRS 6.2 / 6.3 / 6.4）——

		// 分母：标准口径取**应参加人数**，与组织工作通行做法一致。
		// 未提交者相当于未投票，不减小分母。
		denom := p.ExpectedCount
		if denom <= 0 {
			denom = submitted
		}
		if denom <= 0 {
			return &cell, nil
		}

		// 未提交部分一律并入弃权：四档计数 + 弃权 ≡ 应参加人数。
		answered := c.Abstain
		for _, n := range c.Grades {
			answered += n
		}
		cell.Abstain = c.Abstain + (denom - answered)
		if cell.Abstain < 0 {
			// 提交数超过应参加人数：管理员把 expected_count 填小了。
			// 不静默截断——这会让分母口径失去意义。
			return nil, fmt.Errorf(
				"题目「%s」的作答数超过应参加人数（%d > %d），请核对应参加人数设置",
				q.Title, answered, denom)
		}

		// 比率一律保留一位小数，四舍五入；差额调平到当前最大项。
		raw := make([]float64, NumGrades+1)
		for i, n := range c.Grades {
			raw[i] = float64(n) * 100 / float64(denom)
		}
		raw[NumGrades] = float64(cell.Abstain) * 100 / float64(denom)

		r := make([]float64, len(raw))
		for i := range raw {
			r[i] = round1(raw[i])
		}
		// 调平到最大项而非首项：最大项的相对误差最小，视觉上最不易察觉。
		diff := round1(100.0 - sum(r))
		if diff != 0 {
			r[argmax(r)] = round1(r[argmax(r)] + diff)
		}

		copy(cell.Rates[:], r[:NumGrades])
		cell.RateAbst = r[NumGrades]
		cell.RateExcellent = cell.Rates[GradeExcellent]
		cell.RateCompAbove = round1(
			float64(c.Grades[GradeExcellent]+c.Grades[GradeCompetent]) * 100 / float64(denom))

		// 加权得分：弃权不计入分子分母，仅用于同优秀率时的辅助排序。
		valid := 0
		wsum := 0
		for i, n := range c.Grades {
			valid += n
			if i < len(p.GradeScores) {
				wsum += n * p.GradeScores[i]
			}
		}
		if valid > 0 {
			cell.Weighted = round1(float64(wsum) / float64(valid))
		}

		// 恒等式硬断言（HLD 7.2）：四档 + 弃权 = 100.0%。
		// 不成立即返回错误、拒绝出报表，而不是打个日志继续。
		if total := round1(sum(r)); total != 100.0 {
			return nil, fmt.Errorf(
				"口径恒等式不成立：题目「%s」四档与弃权比率合计 %.1f%%，应为 100.0%%",
				q.Title, total)
		}
	}
	return &cell, nil
}

// SmallSampleLimit 是小样本保护阈值（NFR-ANO-050）。
// 分组内对象数少于此值时不展示该分组独立统计。
const SmallSampleLimit = 3

var ErrSampleTooSmall = fmt.Errorf("该分组人数过少，不单独统计")

// FilterByTag 实现分组筛选与小样本保护。
// 在服务层拦截而非前端隐藏——前端隐藏可被绕过（LLD 6.6）。
func (r *Result) FilterByTag(f *model.Form, tag string) (*Result, error) {
	var ids []anon.ID
	for _, s := range f.Subjects {
		if s.Tag == tag {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) < SmallSampleLimit {
		return nil, ErrSampleTooSmall
	}
	in := map[anon.ID]bool{}
	for _, id := range ids {
		in[id] = true
	}
	out := *r
	out.Cells = nil
	for _, c := range r.Cells {
		if in[c.SubjectID] {
			out.Cells = append(out.Cells, c)
		}
	}
	return &out, nil
}

// AllTexts 汇总评语，顺序随机（FR-STA-017 / NFR-ANO-030）。
func (r *Result) AllTexts() []string {
	var out []string
	for _, c := range r.Cells {
		for _, t := range c.Texts {
			if c.SubjectName != "" {
				out = append(out, c.SubjectName+"：" + t)
			} else {
				out = append(out, t)
			}
		}
	}
	anon.Shuffle(out)
	return out
}

func round1(f float64) float64 {
	// 用整数运算做四舍五入，避免二进制浮点在 .05 边界上的意外行为。
	if f >= 0 {
		return float64(int64(f*10+0.5)) / 10
	}
	return float64(int64(f*10-0.5)) / 10
}

func sum(fs []float64) float64 {
	t := 0.0
	for _, f := range fs {
		t += f
	}
	return round1(t)
}

func argmax(fs []float64) int {
	m := 0
	for i := range fs {
		if fs[i] > fs[m] {
			m = i
		}
	}
	return m
}
