package views

import (
	"fmt"
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
