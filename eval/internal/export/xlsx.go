package export

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/xuri/excelize/v2"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
	"odes/internal/stats"
)

// ── Excel 导出（FR-EXP-011 / FR-EXP-012 / FR-TKN-034）──────────────
//
// 选型由 HLD 2.1 定：excelize，纯 Go、无 CGO，符合 CON-07。
//
// 与 CSV 出口的关系：CSV 保留不动。CSV 是"一定打得开"的兜底格式——
// 政务机器上 Excel 版本参差，xlsx 偶尔会遇到打不开的情况，而散会后
// 数据取不出来是不可接受的。两个格式的数据完全同源，都取自 stats.Result。

// 表头样式：加粗 + 灰底 + 居中 + 冻结首行。手工核对几十行数据时，
// 表头跟着滚走会显著增加看错行的概率。
func headerStyle(f *excelize.File) (int, error) {
	return f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#EFEDE7"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "bottom", Color: "#BBBBBB", Style: 1},
		},
	})
}

// writeHead 写一行表头并冻结。
func writeHead(f *excelize.File, sheet string, head []string) error {
	st, err := headerStyle(f)
	if err != nil {
		return err
	}
	for i, h := range head {
		cell, err := excelize.CoordinatesToCellName(i+1, 1)
		if err != nil {
			return err
		}
		if err := f.SetCellStr(sheet, cell, h); err != nil {
			return err
		}
	}
	last, err := excelize.CoordinatesToCellName(len(head), 1)
	if err != nil {
		return err
	}
	if err := f.SetCellStyle(sheet, "A1", last, st); err != nil {
		return err
	}
	return f.SetPanes(sheet, &excelize.Panes{
		Freeze: true, Split: false, XSplit: 0, YSplit: 1,
		TopLeftCell: "A2", ActivePane: "bottomLeft",
	})
}

// setRow 写一行。字符串一律走 SetCellStr：
// 用 SetCellValue 时以 = 开头的内容有被当作公式的风险，而测评对象姓名
// 与评语都是外部输入。CSV 出口用 safe() 加前导单引号解决同一问题，
// xlsx 这里用"显式声明为字符串"解决，两者目的相同。
func setRow(f *excelize.File, sheet string, row int, vals []any) error {
	for i, v := range vals {
		cell, err := excelize.CoordinatesToCellName(i+1, row)
		if err != nil {
			return err
		}
		switch t := v.(type) {
		case string:
			err = f.SetCellStr(sheet, cell, t)
		default:
			err = f.SetCellValue(sheet, cell, v)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func autoWidth(f *excelize.File, sheet string, widths map[string]float64) {
	for col, w := range widths {
		_ = f.SetColWidth(sheet, col, col, w)
	}
}

// StatsXLSX 输出按题型分 Sheet 的逐项统计表（FR-EXP-011）。
//
// 分 Sheet 而不是堆在一张表里：等级题、打分题、选择题的指标列完全不同，
// 混在一起会出现大片空列，二次制表时极易错列。
func StatsXLSX(w io.Writer, r *stats.Result) error {
	f := excelize.NewFile()
	defer f.Close()

	if err := statsOverviewSheet(f, r); err != nil {
		return err
	}
	if err := statsGradeSheet(f, r); err != nil {
		return err
	}
	if err := statsScoreSheet(f, r); err != nil {
		return err
	}
	if err := statsOptionSheet(f, r); err != nil {
		return err
	}
	if err := statsTextSheet(f, r); err != nil {
		return err
	}

	// 默认的 Sheet1 没用上就删掉，免得打开是空白页
	if idx, _ := f.GetSheetIndex("Sheet1"); idx >= 0 {
		_ = f.DeleteSheet("Sheet1")
	}
	f.SetActiveSheet(0)
	return f.Write(w)
}

func newSheet(f *excelize.File, name string) error {
	if _, err := f.NewSheet(name); err != nil {
		return err
	}
	return nil
}

func statsOverviewSheet(f *excelize.File, r *stats.Result) error {
	const sh = "测评概况"
	if err := newSheet(f, sh); err != nil {
		return err
	}
	if err := writeHead(f, sh, []string{"项目", "值"}); err != nil {
		return err
	}
	rows := [][]any{
		{"项目名称", r.ProjectName},
		{"应参加人数（比率分母）", r.ExpectedCount},
		{"实提交份数", r.Submitted},
		{"其中纸质补录", r.PaperEntry},
		{"提交率", fmt.Sprintf("%.1f%%", r.SubmitRate)},
		{"", ""},
		{"口径说明", "全部比率的分母为应参加人数（标准口径）。未提交者视同弃权、计入分母。"},
		{"", "显式弃权、选填未作答、整份未提交三种情形合并计入弃权项。"},
		{"", "四档比率之和 + 弃权率 ≡ 100.0%；比率保留一位小数，差额调平至最大项。"},
		{"", "加权得分仅用于同优秀率时的辅助排序，不作为结论表述。"},
	}
	for i, row := range rows {
		if err := setRow(f, sh, i+2, row); err != nil {
			return err
		}
	}
	autoWidth(f, sh, map[string]float64{"A": 26, "B": 80})
	return nil
}

func statsGradeSheet(f *excelize.File, r *stats.Result) error {
	const sh = "等级题"
	if err := newSheet(f, sh); err != nil {
		return err
	}
	// 列序与统计页、正式报告、明细 CSV 完全一致
	head := []string{"测评对象", "题目", "优秀", "称职", "基本称职", "不称职", "弃权",
		"优秀率", "称职率", "基本称职率", "不称职率", "弃权率", "合计",
		"称职以上率", "加权得分"}
	if err := writeHead(f, sh, head); err != nil {
		return err
	}
	n := 2
	for _, c := range r.Cells {
		if c.Kind != model.KindGrade {
			continue
		}
		if err := setRow(f, sh, n, []any{
			c.SubjectName, c.QuestionName,
			c.Counts[0], c.Counts[1], c.Counts[2], c.Counts[3], c.Abstain,
			pct(c.Rates[0]), pct(c.Rates[1]), pct(c.Rates[2]), pct(c.Rates[3]),
			pct(c.RateAbst), pct(c.RateTotal),
			pct(c.RateCompAbove), c.Weighted,
		}); err != nil {
			return err
		}
		n++
	}
	autoWidth(f, sh, map[string]float64{"A": 16, "B": 24})
	return nil
}

func statsScoreSheet(f *excelize.File, r *stats.Result) error {
	const sh = "打分题"
	if err := newSheet(f, sh); err != nil {
		return err
	}
	if err := writeHead(f, sh, []string{"测评对象", "题目", "平均分", "中位数", "最低", "最高"}); err != nil {
		return err
	}
	n := 2
	for _, c := range r.Cells {
		if c.Kind != model.KindScore {
			continue
		}
		if err := setRow(f, sh, n, []any{
			c.SubjectName, c.QuestionName,
			c.ScoreAvg, c.ScoreMedian, c.ScoreMin, c.ScoreMax,
		}); err != nil {
			return err
		}
		n++
	}
	autoWidth(f, sh, map[string]float64{"A": 16, "B": 24})
	return nil
}

func statsOptionSheet(f *excelize.File, r *stats.Result) error {
	const sh = "选择题"
	if err := newSheet(f, sh); err != nil {
		return err
	}
	if err := writeHead(f, sh, []string{"测评对象", "题目", "选项", "票数"}); err != nil {
		return err
	}
	n := 2
	for _, c := range r.Cells {
		if c.Kind != model.KindSingle && c.Kind != model.KindMulti {
			continue
		}
		// 按题目定义的选项顺序输出，不按 map 遍历顺序——
		// map 顺序每次都不同，同一份数据导两次列不上。
		for _, label := range c.OptionLabels {
			if err := setRow(f, sh, n, []any{
				c.SubjectName, c.QuestionName, label, c.OptionCounts[label],
			}); err != nil {
				return err
			}
			n++
		}
	}
	autoWidth(f, sh, map[string]float64{"A": 16, "B": 24, "C": 24})
	return nil
}

func statsTextSheet(f *excelize.File, r *stats.Result) error {
	const sh = "评语汇总"
	if err := newSheet(f, sh); err != nil {
		return err
	}
	if err := writeHead(f, sh, []string{"评语（顺序随机，与提交先后无关）"}); err != nil {
		return err
	}
	// AllTexts 内部已 anon.Shuffle
	for i, t := range r.AllTexts() {
		if err := setRow(f, sh, i+2, []any{t}); err != nil {
			return err
		}
	}
	autoWidth(f, sh, map[string]float64{"A": 100})
	return nil
}

// RawXLSX 输出原始匿名数据（FR-EXP-012）。
//
// 与 RawCSV 同源同规则：记录顺序每次导出重新随机重排，编号是重排后的
// 序号，不含时间、IP、设备等任何可追溯信息。
func RawXLSX(w io.Writer, p *model.Project, form *model.Form,
	recs []model.AnswerRecord, v *crypto.Vault) error {

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
			m[it.Q+":"+it.S] = itemText(form, &it)
		}
		decoded = append(decoded, m)
	}

	// ★ 重排——必经步骤（NFR-ANO-030）
	anon.Shuffle(decoded)

	f := excelize.NewFile()
	defer f.Close()
	const sh = "原始匿名数据"
	if err := newSheet(f, sh); err != nil {
		return err
	}

	var keys []string
	head := []string{"记录"}
	for _, g := range form.Groups {
		for _, q := range g.Questions {
			for _, sid := range q.SubjectIDs {
				keys = append(keys, q.ID.Hex()+":"+sid.Hex())
				name := ""
				if s := form.Subject(sid); s != nil {
					name = s.Name
				}
				head = append(head, name+" / "+q.Title)
			}
		}
	}
	if err := writeHead(f, sh, head); err != nil {
		return err
	}
	for i, m := range decoded {
		row := []any{anon.Label(i)}
		for _, k := range keys {
			row = append(row, m[k])
		}
		if err := setRow(f, sh, i+2, row); err != nil {
			return err
		}
	}
	autoWidth(f, sh, map[string]float64{"A": 14})

	if idx, _ := f.GetSheetIndex("Sheet1"); idx >= 0 {
		_ = f.DeleteSheet("Sheet1")
	}
	f.SetActiveSheet(0)
	return f.Write(w)
}

// TokenListXLSX 输出令牌清单（FR-TKN-034），供现场核对与补发。
//
// **清单不含任何人员信息**，这是硬约束：清单一旦带上姓名或名单序号，
// 它就成了人码对照表，整套匿名性当场作废。因此这里只接受短码与 AP 序号，
// 连令牌值都不输出——令牌值是入场凭证，落到一份会在现场传阅的表格上
// 没有任何好处。
type TokenListRow struct {
	ShortCode string
	APIndex   int
	Spare     bool
}

func TokenListXLSX(w io.Writer, projectName string, rows []TokenListRow) error {
	f := excelize.NewFile()
	defer f.Close()
	const sh = "令牌清单"
	if err := newSheet(f, sh); err != nil {
		return err
	}
	if err := writeHead(f, sh, []string{"序号", "短码", "无线网络", "类别"}); err != nil {
		return err
	}
	for i, r := range rows {
		kind := "正式"
		if r.Spare {
			kind = "备用"
		}
		code := r.ShortCode
		if len(code) == 8 {
			code = code[:4] + "-" + code[4:]
		}
		if err := setRow(f, sh, i+2, []any{
			i + 1, code, fmt.Sprintf("KCZ-EVAL-%d", r.APIndex), kind,
		}); err != nil {
			return err
		}
	}
	// 这里的"序号"只是清单行号，与令牌集合的物理顺序、生成顺序无关——
	// 令牌在生成时已随机重排，行号不承载任何可追溯含义。
	note := len(rows) + 3
	if err := setRow(f, sh, note, []any{
		"说明", "本清单不含任何人员姓名或编号；序号仅为清单行号，与发放对象无关。",
	}); err != nil {
		return err
	}
	autoWidth(f, sh, map[string]float64{"A": 8, "B": 16, "C": 18, "D": 10})

	if idx, _ := f.GetSheetIndex("Sheet1"); idx >= 0 {
		_ = f.DeleteSheet("Sheet1")
	}
	f.SetActiveSheet(0)
	return f.Write(w)
}
