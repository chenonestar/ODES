package export

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/signintech/gopdf"
	"github.com/signintech/gopdf/fontmaker/core"

	"odes/internal/model"
	"odes/internal/stats"
)

// ── PDF 正式报告（ADR-009 / FR-EXP-010 / FR-EXP-020 / FR-EXP-021）──
//
// 选型由 LLD 13.1 定：gopdf + 内嵌完整中文字库，不做子集化。
// 排除无头浏览器是因为它依赖外部程序，直接违背 NFR-POR-010 的单文件零依赖。
//
// 字体不做子集化的理由见 LLD 13.3：干部姓名里有生僻字，子集化的字库
// 会在最不该出错的地方印出方框——一份把姓名印成 □ 的正式报告是不能上报的。

// ErrNoFont 表示尚未放置报告字体。
//
// 单独成一个错误值而不是笼统报"生成失败"：管理员需要知道这是**放个文件
// 就能解决**的事，而不是数据或程序出了问题。散会后要交材料的场合，
// 这两种判断的代价差别很大。
var ErrNoFont = errors.New(
	"未放置报告字体：PDF 报告需要一份完整覆盖的中文字库，" +
		"请按 web/fonts/README.md 放置 web/fonts/report.ttf 后重新构建；" +
		"HTML 报告与 xlsx / CSV 导出不受影响，可照常使用")

const (
	fontName = "report"
	pageW    = 595.28 // A4 纵向，单位 pt
	pageH    = 841.89
	margin   = 42.0
	lineH    = 16.0
)

// ReportPDF 生成正式统计报告。fontTTF 为 nil 时返回 ErrNoFont。
func ReportPDF(w io.Writer, p *model.Project, r *stats.Result,
	verifyCode string, fontTTF []byte) error {

	if len(fontTTF) == 0 {
		return ErrNoFont
	}

	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: gopdf.Rect{W: pageW, H: pageH}})

	if err := pdf.AddTTFFontData(fontName, fontTTF); err != nil {
		// 最常见的原因是放了 CFF 轮廓的 .otf（思源宋体、Noto Serif CJK
		// 的官方 .otf 都是），gopdf 只认 glyf。提示里直接给出自查命令，
		// 免得管理员对着一句"invalid font"无从下手。
		return fmt.Errorf("装载报告字体失败：%w\n"+
			"若放的是 .otf，很可能是 CFF 轮廓——gopdf 只支持 glyf 轮廓的 TrueType。"+
			"可先运行 go run ./tools/checkfont web/fonts/report.ttf 自查", err)
	}

	// 字形覆盖必须**先查再排版**。gopdf 遇到字库里没有的字是直接丢掉，
	// 既不报错也不画方框——报告上只会少几个字。姓名里少一个字的报告
	// 是会被当成"打印机卡了"而直接上报的，没人能发现数据错了。
	// LLD 13.3 要求不做子集化正是为这件事，这里把它变成一道硬检查。
	if missing := missingGlyphs(fontTTF, reportText(p, r, verifyCode)); len(missing) > 0 {
		return fmt.Errorf(
			"报告字体缺少 %d 个字形，PDF 未生成：%s\n"+
				"缺字会被静默丢弃（不显示方框），姓名少字的报告无法发现。"+
				"请换用完整覆盖的中文字库，不要用子集化版本（LLD 13.3）",
			len(missing), string(missing))
	}

	d := &doc{pdf: pdf, verify: verifyCode}
	d.newPage()

	d.title(r.ProjectName)
	d.subtitle("民主测评统计报告")

	d.heading("一、测评概况")
	d.table(
		[]string{"应参加人数", "实提交份数", "其中纸质补录", "提交率"},
		[][]string{{
			itoa(r.ExpectedCount), itoa(r.Submitted),
			itoa(r.PaperEntry), pct(r.SubmitRate),
		}},
		[]float64{120, 120, 120, 120},
	)

	d.heading("二、等级测评结果")
	head := []string{"测评对象 / 题目", "优", "称", "基", "不", "弃",
		"优秀率", "称职率", "基本称职率", "不称职率", "弃权率", "合计", "称职以上率"}
	widths := []float64{132, 22, 22, 22, 22, 22, 40, 40, 46, 44, 40, 40, 48}
	var rows [][]string
	for _, c := range gradeOnly(r) {
		rows = append(rows, []string{
			c.SubjectName + " / " + c.QuestionName,
			itoa(c.Counts[0]), itoa(c.Counts[1]), itoa(c.Counts[2]),
			itoa(c.Counts[3]), itoa(c.Abstain),
			pct(c.Rates[0]), pct(c.Rates[1]), pct(c.Rates[2]), pct(c.Rates[3]),
			pct(c.RateAbst), pct(c.RateTotal), pct(c.RateCompAbove),
		})
	}
	d.table(head, rows, widths)

	if scores := cellsOf(r, model.KindScore); len(scores) > 0 {
		d.heading("三、打分题")
		var sr [][]string
		for _, c := range scores {
			sr = append(sr, []string{
				c.SubjectName + " / " + c.QuestionName,
				f1(c.ScoreAvg), f1(c.ScoreMedian), itoa(c.ScoreMin), itoa(c.ScoreMax),
			})
		}
		d.table([]string{"测评对象 / 题目", "平均分", "中位数", "最低", "最高"},
			sr, []float64{240, 70, 70, 60, 60})
	}

	if texts := r.AllTexts(); len(texts) > 0 {
		d.heading("四、评语汇总")
		d.note("以下评语顺序为随机排列，与提交先后无关。")
		for _, t := range texts {
			d.bullet(t)
		}
	}

	// FR-EXP-020：口径说明页。单独起一页，便于随报告整页复印留档。
	d.newPage()
	d.heading("统计口径说明")
	d.para("分母定义", fmt.Sprintf(
		"全部比率指标的分母为应参加人数（%d 人），即标准口径，与组织工作通行做法一致。"+
			"未提交者相当于未投票，不减小分母。", r.ExpectedCount))
	d.para("弃权处理",
		"显式选择「弃权」、选填题未作答、整份未提交，三种情形合并计入弃权项，均计入分母。"+
			"四档比率之和 + 弃权率 ≡ 100.0%。")
	d.para("纸质补录", fmt.Sprintf(
		"共 %d 份，其中纸质补录 %d 份。补录记录与在线记录合并统计，在数据中不可区分。",
		r.Submitted, r.PaperEntry))
	d.para("档位赋分与加权得分", fmt.Sprintf(
		"赋分规则 %v；加权得分 = Σ(各档票数 × 档位赋分) ÷ 有效票数，弃权不计入分子分母。"+
			"该指标仅用于同优秀率时的辅助排序，不作为结论表述。", r.GradeScores))
	d.para("舍入规则",
		"比率保留一位小数、四舍五入；因舍入导致合计不等于 100.0% 时，差额调平至当前最大项。"+
			"全程整数定点运算，不经二进制浮点。")
	d.para("匿名性",
		"作答记录与令牌之间不存在任何关联字段，记录顺序与提交先后无关。"+
			"本报告不含、也无法还原任何个人身份信息。")

	d.footerAll()
	return pdf.Write(w)
}

// ── 排版辅助 ────────────────────────────────────────────────────────

type doc struct {
	pdf    *gopdf.GoPdf
	y      float64
	verify string
	pages  int
}

func (d *doc) newPage() {
	d.pdf.AddPage()
	d.pages++
	d.y = margin
}

// ensure 在剩余高度不足时换页。表格逐行调用，避免整表被截断。
func (d *doc) ensure(h float64) {
	if d.y+h > pageH-margin-24 { // 24 留给页脚
		d.newPage()
	}
}

func (d *doc) setFont(size float64) {
	_ = d.pdf.SetFont(fontName, "", size)
}

func (d *doc) text(s string, size, x float64) {
	d.setFont(size)
	d.pdf.SetXY(x, d.y)
	_ = d.pdf.Cell(nil, s)
	d.y += size + 6
}

func (d *doc) title(s string) {
	d.ensure(30)
	d.text(s, 18, margin)
}

func (d *doc) subtitle(s string) {
	d.ensure(20)
	d.text(s, 10, margin)
	d.y += 6
}

func (d *doc) heading(s string) {
	d.ensure(28)
	d.y += 8
	d.text(s, 13, margin)
	d.pdf.SetLineWidth(0.8)
	d.pdf.Line(margin, d.y-2, pageW-margin, d.y-2)
	d.y += 6
}

func (d *doc) note(s string) {
	d.ensure(18)
	d.text(s, 8, margin)
}

func (d *doc) bullet(s string) {
	d.ensure(lineH)
	d.setFont(9)
	d.pdf.SetXY(margin, d.y)
	// MultiCell 自动折行：评语可以很长，Cell 会直接溢出到页面外
	_ = d.pdf.MultiCell(&gopdf.Rect{W: pageW - 2*margin, H: pageH}, "· "+s)
	d.y = d.pdf.GetY() + 4
}

func (d *doc) para(label, body string) {
	d.ensure(40)
	d.text(label, 10, margin)
	d.setFont(9)
	d.pdf.SetXY(margin, d.y)
	_ = d.pdf.MultiCell(&gopdf.Rect{W: pageW - 2*margin, H: pageH}, body)
	d.y = d.pdf.GetY() + 8
}

// table 画一张带表头的表。每页重画表头——跨页的表格若不重画表头，
// 第二页起就是一堆没有列名的数字，核对时极易看错列。
func (d *doc) table(head []string, rows [][]string, widths []float64) {
	d.ensure(lineH * 2)
	d.tableHead(head, widths)
	for _, r := range rows {
		if d.y+lineH > pageH-margin-24 {
			d.newPage()
			d.tableHead(head, widths)
		}
		d.tableRow(r, widths, 8)
	}
	d.y += 6
}

func (d *doc) tableHead(head []string, widths []float64) {
	d.tableRow(head, widths, 8)
	d.pdf.SetLineWidth(0.6)
	d.pdf.Line(margin, d.y-2, margin+sum(widths), d.y-2)
	d.y += 2
}

func (d *doc) tableRow(cells []string, widths []float64, size float64) {
	d.setFont(size)
	x := margin
	for i, c := range cells {
		w := 60.0
		if i < len(widths) {
			w = widths[i]
		}
		d.pdf.SetXY(x, d.y)
		_ = d.pdf.CellWithOption(&gopdf.Rect{W: w, H: lineH}, c,
			gopdf.CellOption{Align: gopdf.Left | gopdf.Middle})
		x += w
	}
	d.y += lineH
}

// footerAll 给每一页画页脚（FR-EXP-021）。
// 核验编号必须每页都有：报告常被拆页复印，只印在末页等于没有。
func (d *doc) footerAll() {
	total := d.pdf.GetNumberOfPages()
	for i := 1; i <= total; i++ {
		d.pdf.SetPage(i)
		d.setFont(7.5)
		d.pdf.SetXY(margin, pageH-margin+6)
		_ = d.pdf.Cell(nil, fmt.Sprintf(
			"本报告由线上民主测评系统生成，原始数据已加密封存，编号 %s", d.verify))
		d.pdf.SetXY(pageW-margin-60, pageH-margin+6)
		_ = d.pdf.Cell(nil, fmt.Sprintf("第 %d / %d 页", i, total))
	}
}

func sum(v []float64) float64 {
	t := 0.0
	for _, x := range v {
		t += x
	}
	return t
}

func f1(f float64) string { return fmt.Sprintf("%.1f", f) }

func cellsOf(r *stats.Result, k model.QuestionKind) []stats.Cell {
	var out []stats.Cell
	for _, c := range r.Cells {
		if c.Kind == k {
			out = append(out, c)
		}
	}
	return out
}

// ── 字形覆盖检查 ────────────────────────────────────────────────────

// reportText 汇总报告里会被打印的全部文字。
//
// 覆盖检查必须基于**实际要打的内容**，而不是"常用汉字表"：
// 出问题的恰恰是姓名里的生僻字，它们按定义不在常用表里。
func reportText(p *model.Project, r *stats.Result, verifyCode string) string {
	var sb strings.Builder
	sb.WriteString(r.ProjectName)
	sb.WriteString(p.Name)
	sb.WriteString(verifyCode)
	for _, c := range r.Cells {
		sb.WriteString(c.SubjectName)
		sb.WriteString(c.QuestionName)
		for _, t := range c.Texts {
			sb.WriteString(t)
		}
		for _, l := range c.OptionLabels {
			sb.WriteString(l)
		}
	}
	// 固定文案（标题、表头、口径说明页）也要覆盖
	sb.WriteString(fixedReportText)
	return sb.String()
}

// fixedReportText 是模板里写死的那些字。集中在一处，
// 改文案时不至于忘了同步覆盖检查。
const fixedReportText = "民主测评统计报告一二三四、测评概况应参加人数实提交份数其中纸质补录提交率" +
	"等级测评结果对象题目优称基不弃秀率职本称职以上合计打分平均中位数最低最高" +
	"评语汇总以下顺序为随机排列与提交先后无关口径说明分母定义全部比指标的" +
	"即标准与组织工作通行做法一致未者相当于投票减小处理显式选择填未作答整份" +
	"三种情形并入均之和≡舍五档位赋加权得规则Σ各票数×÷有效仅用同时的辅助排序" +
	"不作结论表述舍入保留一位小因导致等值差额调至当前最大项程整定点运算经二进制浮" +
	"匿名性记录与令牌间存在任何关联字段本含也无法还原个人身信息由线上系统生成" +
	"原始据已加密封编号第页份人0123456789.%/()（）：，、。"

// missingGlyphs 返回字库里没有的字符（去重、保序）。
func missingGlyphs(fontTTF []byte, text string) []rune {
	var p core.TTFParser
	if err := p.ParseFontData(fontTTF); err != nil {
		// 解析失败留给 AddTTFFontData 去报——那里的提示更具体
		return nil
	}
	chars := p.Chars()
	seen := map[rune]bool{}
	var out []rune
	for _, r := range text {
		if r == ' ' || r == '\n' || r == '\t' || seen[r] {
			continue
		}
		seen[r] = true
		if _, ok := chars[int(r)]; !ok {
			out = append(out, r)
		}
	}
	return out
}
