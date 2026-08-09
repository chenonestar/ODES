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

func itoa(n int) string        { return fmt.Sprintf("%d", n) }
func pct(f float64) string     { return fmt.Sprintf("%.1f%%", f) }
func f1(f float64) string      { return fmt.Sprintf("%.1f", f) }
func date(t time.Time) string  { return t.Format("2006-01-02 15:04") }
func hex(id anon.ID) string    { return id.Hex() }
func projURL(id anon.ID) string { return "/admin/projects/" + id.Hex() }

// HomeRow 是项目列表的一行。
type HomeRow struct {
	P                 *model.Project
	Submitted, Tokens int
}

// ProjectView 汇集项目详情页需要的一切，避免在模板里再取数。
type ProjectView struct {
	P                                *model.Project
	F                                *model.Form
	Items, Tokens, Used, Submitted   int
	Trials                           []string
	Logs                             []store.OpLogEntry
	Checks                           []service.Check
}

// StatsView 同上，供统计页使用。
type StatsView struct {
	P          *model.Project
	R          *stats.Result
	Submitted  int
	GradeCells []stats.Cell
	Texts      []string
	Tags       []string
}

// PrintSheet 是一张令牌单的内容。
type PrintSheet struct {
	SSID, Code, URL string
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
