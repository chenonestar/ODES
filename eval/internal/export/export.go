// Package export 产出报告与数据文件。
//
// 三个出口（统计页、HTML/PDF 报告、CSV/Excel）**必须共用同一份
// stats.Result**，不得各自计算。口径不一致是这类系统最典型的事故：
// 报告里写 92.3%、Excel 里算出 91.8%，一旦出现就直接摧毁整套数据的
// 可信度（HLD 7.3）。
package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"strings"
	"time"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
	"odes/internal/stats"
)

// StatsCSV 输出逐项统计表。
func StatsCSV(w io.Writer, r *stats.Result) error {
	// UTF-8 BOM：Excel 打开中文 CSV 不加 BOM 会乱码。
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	defer cw.Flush()

	head := []string{"测评对象", "题目", "优秀", "称职", "基本称职", "不称职", "弃权",
		"优秀率", "称职以上率", "基本称职率", "不称职率", "弃权率", "加权得分"}
	if err := cw.Write(head); err != nil {
		return err
	}
	for _, c := range r.Cells {
		if c.Kind != model.KindGrade {
			continue
		}
		row := []string{
			safe(c.SubjectName), safe(c.QuestionName),
			itoa(c.Counts[0]), itoa(c.Counts[1]), itoa(c.Counts[2]), itoa(c.Counts[3]),
			itoa(c.Abstain),
			pct(c.RateExcellent), pct(c.RateCompAbove),
			pct(c.Rates[2]), pct(c.Rates[3]), pct(c.RateAbst),
			fmt.Sprintf("%.1f", c.Weighted),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	// 口径说明随数据一同输出，避免脱离上下文被误读（FR-EXP-020）
	_ = cw.Write(nil)
	_ = cw.Write([]string{"口径说明"})
	_ = cw.Write([]string{"分母", fmt.Sprintf("应参加人数 %d（标准口径）", r.ExpectedCount)})
	_ = cw.Write([]string{"实提交", fmt.Sprintf("%d 份，其中纸质补录 %d 份", r.Submitted, r.PaperEntry)})
	_ = cw.Write([]string{"弃权", "显式弃权 + 选填未作答 + 整份未提交，均计入分母"})
	_ = cw.Write([]string{"加权得分", "仅用于同优秀率时的辅助排序，不作为结论表述"})
	return cw.Error()
}

// RawCSV 输出原始匿名数据（FR-EXP-012）。
//
// 两条纪律：
//  1. 导出前统一 Shuffle——记录顺序不得反映任何东西；
//  2. 编号现场生成、不入库，两次导出的编号互不一致（NFR-ANO-030）。
//
// 输出中不含时间、IP、设备等任何可追溯信息。
func RawCSV(w io.Writer, p *model.Project, f *model.Form,
	recs []model.AnswerRecord, v *crypto.Vault) error {

	type row struct {
		label string
		items map[string]string
	}

	// 先解密
	decoded := make([]map[string]string, 0, len(recs))
	for _, rec := range recs {
		b, err := v.Open(rec.Payload, rec.ProjectID.Bytes())
		if err != nil {
			return fmt.Errorf("解密作答记录失败: %w", err)
		}
		var a model.Answer
		if err := json.Unmarshal(b, &a); err != nil {
			return err
		}
		m := map[string]string{}
		for _, it := range a.Items {
			m[it.Q+":"+it.S] = itemText(f, &it)
		}
		decoded = append(decoded, m)
	}

	// ★ 重排——必经步骤
	anon.Shuffle(decoded)

	var rows []row
	for i, m := range decoded {
		rows = append(rows, row{label: anon.Label(i), items: m})
	}

	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	defer cw.Flush()

	var keys []string
	head := []string{"记录"}
	for _, g := range f.Groups {
		for _, q := range g.Questions {
			for _, sid := range q.SubjectIDs {
				keys = append(keys, q.ID.Hex()+":"+sid.Hex())
				name := ""
				if s := f.Subject(sid); s != nil {
					name = s.Name
				}
				head = append(head, safe(name+" / "+q.Title))
			}
		}
	}
	if err := cw.Write(head); err != nil {
		return err
	}
	for _, r := range rows {
		line := []string{r.label}
		for _, k := range keys {
			line = append(line, safe(r.items[k]))
		}
		if err := cw.Write(line); err != nil {
			return err
		}
	}
	return cw.Error()
}

func itemText(f *model.Form, it *model.AnswerItem) string {
	switch {
	case it.Grade != nil:
		if *it.Grade == model.GradeAbstain {
			return "弃权"
		}
		if *it.Grade >= 0 && *it.Grade < len(f.Grades) {
			return f.Grades[*it.Grade]
		}
		return "弃权"
	case it.Score != nil:
		return itoa(*it.Score)
	case it.Text != "":
		return it.Text
	case it.Opt != "":
		if it.Opt == "__abstain" {
			return "弃权"
		}
		return optLabel(f, it.Opt)
	case len(it.Opts) > 0:
		var parts []string
		for _, o := range it.Opts {
			parts = append(parts, optLabel(f, o))
		}
		return strings.Join(parts, "；")
	}
	return ""
}

func optLabel(f *model.Form, id string) string {
	for _, g := range f.Groups {
		for _, q := range g.Questions {
			for _, o := range q.Options {
				if o.ID.Hex() == id {
					return o.Label
				}
			}
		}
	}
	return id
}

// safe 是公式注入防护（NFR-SEC-040）。
//
// 评语内容由参评人员自由输入。以 = + - @ 开头的单元格会被 Excel 当作
// 公式执行——一条 "=cmd|'/c calc'!A1" 就能在打开报表的机器上执行命令。
// 前置单引号使其被视为文本。
func safe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func itoa(n int) string  { return fmt.Sprintf("%d", n) }
func pct(f float64) string { return fmt.Sprintf("%.1f%%", f) }

// ── HTML 报告 ───────────────────────────────────────────────────────
//
// 与 PDF 由同一份 stats.Result 生成，口径必然一致（LLD 13.5）。
// PDF 版（gopdf + 内嵌思源宋体，ADR-009）尚未实现，见 README。

var reportTmpl = template.Must(template.New("rep").Funcs(template.FuncMap{
	"pct": pct,
	"f1":  func(f float64) string { return fmt.Sprintf("%.1f", f) },
}).Parse(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<title>{{.R.ProjectName}} · 统计报告</title><style>
body{font-family:"Songti SC","SimSun","Noto Serif CJK SC",serif;color:#1b2434;
max-width:900px;margin:0 auto;padding:40px 28px;line-height:1.8}
h1{font-size:22px;text-align:center;margin-bottom:4px}
.sub{text-align:center;color:#5c6675;font-size:13px;margin-bottom:28px}
h2{font-size:16px;border-left:3px solid #9e2b25;padding-left:9px;margin-top:32px}
table{border-collapse:collapse;width:100%;font-size:13px;margin-top:10px}
th,td{border:1px solid #ddd7cb;padding:6px 8px;text-align:center}
th{background:#f3f1ec;font-weight:600}
td.l{text-align:left}
.bad{color:#9e2b25;font-weight:600}
.aux{color:#5c6675;font-size:12px}
.box{border:1px solid #ddd7cb;border-left:3px solid #5c6675;padding:12px 14px;
margin-top:14px;font-size:13px;background:#faf9f6}
.box b{display:block;margin-bottom:6px}
footer{margin-top:40px;border-top:1px solid #ddd7cb;padding-top:12px;
font-size:12px;color:#5c6675;text-align:center}
@media print{body{padding:0}.noprint{display:none}}
</style></head><body>
<h1>{{.R.ProjectName}}</h1>
<div class="sub">民主测评统计报告　·　生成于 {{.Now}}</div>

<h2>一、测评概况</h2>
<table>
<tr><th>应参加人数</th><th>实提交份数</th><th>其中纸质补录</th><th>提交率</th></tr>
<tr><td>{{.R.ExpectedCount}}</td><td>{{.R.Submitted}}</td>
<td>{{.R.PaperEntry}}</td><td>{{pct .R.SubmitRate}}</td></tr>
</table>

<h2>二、等级测评结果</h2>
<table>
<tr><th rowspan="2" class="l">测评对象 / 题目</th><th colspan="5">票数</th>
<th colspan="2">主要指标</th><th rowspan="2">加权<br>得分</th></tr>
<tr><th>优秀</th><th>称职</th><th>基本称职</th><th>不称职</th><th>弃权</th>
<th>优秀率</th><th>称职以上率</th></tr>
{{range .Cells}}
<tr><td class="l">{{.SubjectName}}　{{.QuestionName}}</td>
<td>{{index .Counts 0}}</td><td>{{index .Counts 1}}</td>
<td>{{index .Counts 2}}</td><td class="bad">{{index .Counts 3}}</td>
<td>{{.Abstain}}</td>
<td><b>{{pct .RateExcellent}}</b></td><td>{{pct .RateCompAbove}}</td>
<td class="aux">{{f1 .Weighted}}</td></tr>
{{end}}
</table>

{{if .Texts}}
<h2>三、评语汇总</h2>
<div class="aux">以下评语顺序为随机排列，与提交先后无关。</div>
<table>{{range .Texts}}<tr><td class="l">{{.}}</td></tr>{{end}}</table>
{{end}}

<h2>四、统计口径说明</h2>
<div class="box">
<b>分母定义</b>全部比率指标的分母为<b>应参加人数（{{.R.ExpectedCount}} 人）</b>，
即标准口径，与组织工作通行做法一致。未提交者相当于未投票，不减小分母。
</div>
<div class="box">
<b>弃权处理</b>显式选择"弃权"、选填题未作答、整份未提交，三种情形合并计入
弃权项，均计入分母。四档比率之和 + 弃权率 ≡ 100.0%。
</div>
<div class="box">
<b>纸质补录</b>共 {{.R.Submitted}} 份，其中纸质补录 {{.R.PaperEntry}} 份。
补录记录与在线记录合并统计，在数据中不可区分。
</div>
<div class="box">
<b>档位赋分与加权得分</b>赋分规则 {{.Scores}}；加权得分 =
Σ(各档票数 × 档位赋分) ÷ 有效票数，弃权不计入分子分母。
<b style="display:inline">该指标仅用于同优秀率时的辅助排序，不作为结论表述。</b>
</div>
<div class="box">
<b>舍入规则</b>比率保留一位小数、四舍五入；因舍入导致合计不等于 100.0% 时，
差额调平至当前最大项。
</div>

<footer>
本报告由线上民主测评系统生成，原始数据已加密封存，编号 {{.VerifyCode}}<br>
作答记录与测评人员身份不可关联；本报告不含任何提交时间、IP 或设备信息。
</footer>
</body></html>`))

func ReportHTML(w io.Writer, p *model.Project, r *stats.Result) error {
	return reportTmpl.Execute(w, map[string]any{
		"R": r, "Cells": gradeOnly(r), "Texts": r.AllTexts(),
		"Now":        time.Now().Format("2006-01-02 15:04"),
		"Scores":     fmt.Sprint(r.GradeScores),
		"VerifyCode": strings.ToUpper(p.ID.Hex()[:8]),
	})
}

func gradeOnly(r *stats.Result) []stats.Cell {
	var out []stats.Cell
	for _, c := range r.Cells {
		if c.Kind == model.KindGrade {
			out = append(out, c)
		}
	}
	return out
}
