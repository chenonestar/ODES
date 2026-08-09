package views

import (
	"fmt"
	"strings"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
	"odes/internal/service"
	"odes/internal/stats"
	"odes/internal/store"
)

// 视图用到的格式化与取数辅助。放在 .go 而非 .templ 里，
// 是为了让 templ 文件只剩标记结构，读起来更接近页面本身。

func itoa(n int) string         { return fmt.Sprintf("%d", n) }
func pct(f float64) string      { return fmt.Sprintf("%.1f%%", f) }
func f1(f float64) string       { return fmt.Sprintf("%.1f", f) }
func date(t time.Time) string   { return t.Format("2006-01-02 15:04") }
func hex(id anon.ID) string     { return id.Hex() }
func projURL(id anon.ID) string { return "/admin/projects/" + id.Hex() }

// HomeRow 是项目列表的一行。
type HomeRow struct {
	P                 *model.Project
	Submitted, Tokens int
}

// ProjectView 汇集项目详情页需要的一切，避免在模板里再取数。
type ProjectView struct {
	P                              *model.Project
	F                              *model.Form
	Items, Tokens, Used, Submitted int
	Trials                         []string
	Logs                           []store.OpLogEntry
	Checks                         []service.Check
}

// StatsView 同上，供统计页使用。
type StatsView struct {
	P          *model.Project
	R          *stats.Result
	Submitted  int
	GradeCells []stats.Cell
	ScoreCells []stats.Cell
	OptCells   []stats.Cell
	Matrix     Matrix
	Texts      []string
	Tags       []string
}

// Matrix 是「测评对象 × 题目」的优秀率汇总矩阵（FR-STA-016）。
//
// 主报表一行一个 (对象, 题目) 组合，4 个对象 × 8 道题就是 32 行，
// 靠上下滚动比不出"谁在哪一项上明显偏低"。矩阵把同一件事横过来摆，
// 是研判环节真正用的那张表。
type Matrix struct {
	Questions []string   // 列：题目
	Subjects  []string   // 行：测评对象
	Rates     [][]string // Rates[行][列]，已格式化；无数据为 "—"
}

// buildMatrix 只取等级题：打分题与选择题没有"优秀率"这个指标，
// 硬塞进同一张矩阵会让两种量纲混在一起，看的人无从判断列的含义。
func buildMatrix(r *stats.Result) Matrix {
	var m Matrix
	qi := map[string]int{}
	si := map[string]int{}
	type key struct{ s, q int }
	vals := map[key]float64{}

	for _, c := range r.Cells {
		if c.Kind != model.KindGrade {
			continue
		}
		if _, ok := qi[c.QuestionName]; !ok {
			qi[c.QuestionName] = len(m.Questions)
			m.Questions = append(m.Questions, c.QuestionName)
		}
		if _, ok := si[c.SubjectName]; !ok {
			si[c.SubjectName] = len(m.Subjects)
			m.Subjects = append(m.Subjects, c.SubjectName)
		}
		vals[key{si[c.SubjectName], qi[c.QuestionName]}] = c.RateExcellent
	}

	for s := range m.Subjects {
		row := make([]string, len(m.Questions))
		for q := range m.Questions {
			if v, ok := vals[key{s, q}]; ok {
				row[q] = pct(v)
			} else {
				row[q] = "—"
			}
		}
		m.Rates = append(m.Rates, row)
	}
	return m
}

// BuildMatrix 供处理器装配 StatsView。
func BuildMatrix(r *stats.Result) Matrix { return buildMatrix(r) }

func cellsOfKind(r *stats.Result, kinds ...model.QuestionKind) []stats.Cell {
	want := map[model.QuestionKind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var out []stats.Cell
	for _, c := range r.Cells {
		if want[c.Kind] {
			out = append(out, c)
		}
	}
	return out
}

// ScoreCells / OptionCells 供处理器装配 StatsView。
func ScoreCells(r *stats.Result) []stats.Cell {
	return cellsOfKind(r, model.KindScore)
}
func OptionCells(r *stats.Result) []stats.Cell {
	return cellsOfKind(r, model.KindSingle, model.KindMulti)
}

// OptionRow 是选择题展开后的一行。
type OptionRow struct {
	Label string
	Count int
	Rate  string
}

// optionRows 按题目定义的选项顺序展开，并给出占实提交份数的比例。
// 不用应参加人数作分母：选择题多为选填，未作答不等同于"选了某项"，
// 用应参加人数会让所有比例被系统性压低，与等级题的口径混淆。
func optionRows(c stats.Cell, submitted int) []OptionRow {
	var out []OptionRow
	for _, l := range c.OptionLabels {
		n := c.OptionCounts[l]
		rate := "—"
		if submitted > 0 {
			rate = fmt.Sprintf("%.1f%%", float64(n)*100/float64(submitted))
		}
		out = append(out, OptionRow{Label: l, Count: n, Rate: rate})
	}
	return out
}

// PrintSheet 是一张令牌单的内容。QR 为内联 data URI。
type PrintSheet struct {
	SSID, Code, URL, QR string
}

func gradeCells(r *stats.Result) []stats.Cell {
	var out []stats.Cell
	for _, c := range r.Cells {
		if c.Kind == model.KindGrade {
			out = append(out, c)
		}
	}
	return out
}

// GradeCells 供处理器装配 StatsView。
func GradeCells(r *stats.Result) []stats.Cell { return gradeCells(r) }

// ── 设计器视图 ──────────────────────────────────────────────────────

// DesignView 汇集设计器、项目表单、题目表单、名单页共用的数据。
//
// 合成一个结构体而不是四个：这几个页面之间来回跳转，共用的字段
// （项目、测评表、可编辑性、错误信息）远多于各自独有的部分。
type DesignView struct {
	P        *model.Project
	F        *model.Form
	Editable bool
	Err      string

	// 计数（FR-FRM-034）
	Items, QuestionCount, RosterCount   int
	MaxItems, MaxQuestions, MaxSubjects int

	// 表单页
	FormTitle, Action, Back string

	// 题目表单
	Q           *model.Question
	GroupID     string
	OptionCSV   string
	AllSubjects bool
	Linked      map[string]bool

	// 名单页
	Preview    []string
	RosterList []string
}

// dragAttr 返回 draggable 属性的值。
//
// 必须显式给出 "true"/"false"，不能用布尔属性写法：draggable 在 HTML 里是
// **枚举属性**而不是布尔属性，templ 的 `draggable?={...}` 渲染出的
// `draggable=""` 是非法值，浏览器按 auto 处理——表格行于是根本拖不动，
// 而页面上那个拖拽把手看起来一切正常。
func dragAttr(on bool) string {
	if on {
		return "true"
	}
	return "false"
}

func subjURL(id anon.ID) string     { return "/admin/subjects/" + id.Hex() }
func groupURL(id anon.ID) string    { return "/admin/groups/" + id.Hex() }
func questionURL(id anon.ID) string { return "/admin/questions/" + id.Hex() }

// dtLocal 输出 <input type="datetime-local"> 需要的格式。
// 该控件只认 2006-01-02T15:04，带时区或带秒都会被静默清空——
// 表单一打开时间就是空的，而管理员多半会以为是自己没填。
func dtLocal(t time.Time) string { return t.Format("2006-01-02T15:04") }

func joinInts(v []int) string {
	parts := make([]string, 0, len(v))
	for _, n := range v {
		parts = append(parts, fmt.Sprintf("%d", n))
	}
	return strings.Join(parts, ",")
}

func joinStrs(v []string) string { return strings.Join(v, ",") }

func kindLabel(k model.QuestionKind) string {
	switch k {
	case model.KindGrade:
		return "等级题"
	case model.KindScore:
		return "打分题"
	case model.KindSingle:
		return "单选题"
	case model.KindMulti:
		return "多选题"
	case model.KindText:
		return "开放评语"
	}
	return string(k)
}

// KindOption 是题型下拉的一项。
type KindOption struct{ Value, Label string }

func kindOptions() []KindOption {
	return []KindOption{
		{string(model.KindGrade), "等级题（干部考察标准题型）"},
		{string(model.KindScore), "打分题（满意度/专项评议）"},
		{string(model.KindSingle), "单选题"},
		{string(model.KindMulti), "多选题"},
		{string(model.KindText), "开放评语"},
	}
}
