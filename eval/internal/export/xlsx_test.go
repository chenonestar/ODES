package export

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"odes/internal/model"
	"odes/internal/stats"
)

func openXLSX(t *testing.T, b []byte) *excelize.File {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("生成的不是合法 xlsx: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func mkMixedResult() *stats.Result {
	grade := stats.Cell{
		QuestionName: "德", SubjectName: "张三", Kind: model.KindGrade,
		Counts: [stats.NumGrades]int{7, 11, 3, 1}, Abstain: 8,
		Rates:    [stats.NumGrades]float64{23.3, 36.7, 10.0, 3.3},
		RateAbst: 26.7, RateTotal: 100.0,
		RateExcellent: 23.3, RateCompAbove: 60.0, Weighted: 79.1,
	}
	score := stats.Cell{
		QuestionName: "综合打分", SubjectName: "李四", Kind: model.KindScore,
		ScoreAvg: 88.5, ScoreMedian: 90, ScoreMin: 60, ScoreMax: 100,
	}
	single := stats.Cell{
		QuestionName: "最突出方面", SubjectName: "王五", Kind: model.KindSingle,
		OptionLabels: []string{"担当作为", "廉洁自律", "群众口碑"},
		OptionCounts: map[string]int{"担当作为": 5, "廉洁自律": 2, "群众口碑": 9},
	}
	text := stats.Cell{
		QuestionName: "评语", SubjectName: "赵六", Kind: model.KindText,
		Texts: []string{"工作扎实", "作风严谨"},
	}
	return &stats.Result{
		ProjectName: "测试项目", ExpectedCount: 30, Submitted: 22, PaperEntry: 2,
		SubmitRate: 73.3, GradeScores: []int{100, 80, 60, 0},
		Cells: []stats.Cell{grade, score, single, text},
	}
}

// FR-EXP-011：按题型分 Sheet。混在一张表里会出现大片空列，二次制表易错列。
func TestStatsXLSXSplitsByQuestionKind(t *testing.T) {
	var buf bytes.Buffer
	if err := StatsXLSX(&buf, mkMixedResult()); err != nil {
		t.Fatal(err)
	}
	f := openXLSX(t, buf.Bytes())

	got := map[string]bool{}
	for _, n := range f.GetSheetList() {
		got[n] = true
	}
	for _, want := range []string{"测评概况", "等级题", "打分题", "选择题", "评语汇总"} {
		if !got[want] {
			t.Errorf("缺少 Sheet「%s」，实际有 %v", want, f.GetSheetList())
		}
	}
	if got["Sheet1"] {
		t.Error("默认空白 Sheet1 应被删除，打开时不该是空白页")
	}
}

func TestStatsXLSXGradeSheetMatchesCSVColumns(t *testing.T) {
	r := mkMixedResult()
	var buf bytes.Buffer
	if err := StatsXLSX(&buf, r); err != nil {
		t.Fatal(err)
	}
	f := openXLSX(t, buf.Bytes())
	rows, err := f.GetRows("等级题")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("等级题 Sheet 应有表头与一行数据，得到 %d 行", len(rows))
	}

	// 与 CSV 出口逐列同序：三个出口摆出来的表必须能逐列对上
	var csvBuf bytes.Buffer
	if err := StatsCSV(&csvBuf, r); err != nil {
		t.Fatal(err)
	}
	csvHead := strings.Split(
		strings.SplitN(strings.TrimPrefix(csvBuf.String(), "\uFEFF"), "\n", 2)[0], ",")
	for i := range csvHead {
		csvHead[i] = strings.TrimSpace(strings.Trim(csvHead[i], `"`))
	}
	if len(rows[0]) != len(csvHead) {
		t.Fatalf("xlsx 与 CSV 列数不一致：%d vs %d\nxlsx=%v\ncsv=%v",
			len(rows[0]), len(csvHead), rows[0], csvHead)
	}
	for i := range csvHead {
		if rows[0][i] != csvHead[i] {
			t.Errorf("第 %d 列不一致：xlsx=%q csv=%q", i+1, rows[0][i], csvHead[i])
		}
	}
	if rows[1][12] != "100.0%" {
		t.Errorf("合计列应为 100.0%%，得到 %s", rows[1][12])
	}
}

// 选项必须按题目定义的顺序输出。按 map 遍历顺序会导致同一份数据
// 导两次列不上，核对的人无从判断哪一次是对的。
func TestStatsXLSXOptionOrderIsStable(t *testing.T) {
	r := mkMixedResult()
	var a, b bytes.Buffer
	if err := StatsXLSX(&a, r); err != nil {
		t.Fatal(err)
	}
	if err := StatsXLSX(&b, r); err != nil {
		t.Fatal(err)
	}
	rowsOf := func(buf bytes.Buffer) [][]string {
		f := openXLSX(t, buf.Bytes())
		rows, err := f.GetRows("选择题")
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	ra, rb := rowsOf(a), rowsOf(b)
	if len(ra) != len(rb) {
		t.Fatalf("两次导出行数不同：%d vs %d", len(ra), len(rb))
	}
	for i := range ra {
		if strings.Join(ra[i], "|") != strings.Join(rb[i], "|") {
			t.Fatalf("第 %d 行两次导出不一致：\n%v\n%v", i+1, ra[i], rb[i])
		}
	}
	want := []string{"担当作为", "廉洁自律", "群众口碑"}
	for i, w := range want {
		if ra[i+1][2] != w {
			t.Errorf("选项顺序应与题目定义一致，第 %d 项为 %s，应为 %s", i+1, ra[i+1][2], w)
		}
	}
}

// FR-TKN-034 的硬约束：清单不含任何人员信息。
// 一旦带上姓名或名单序号，它就成了人码对照表，整套匿名性当场作废。
func TestTokenListCarriesNoPersonalInfo(t *testing.T) {
	rows := []TokenListRow{
		{ShortCode: "ABCD2345", APIndex: 1},
		{ShortCode: "EFGH6789", APIndex: 2, Spare: true},
	}
	var buf bytes.Buffer
	if err := TokenListXLSX(&buf, "测试项目", rows); err != nil {
		t.Fatal(err)
	}
	f := openXLSX(t, buf.Bytes())
	got, err := f.GetRows("令牌清单")
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Join(got[0], "|")
	for _, forbidden := range []string{"姓名", "名单", "人员", "编号对照", "令牌值"} {
		if strings.Contains(head, forbidden) {
			t.Errorf("令牌清单表头出现 %q：清单不得含任何人员信息（FR-TKN-034）", forbidden)
		}
	}
	// 令牌值本身也不输出：它是入场凭证，不该落到会在现场传阅的表格上
	all := ""
	for _, r := range got {
		all += strings.Join(r, "|")
	}
	if strings.Contains(all, "ABCD2345EFGH") {
		t.Error("清单中不应出现完整令牌值")
	}
	if !strings.Contains(all, "ABCD-2345") {
		t.Error("短码应以 4-4 分组展示，便于现场口头核对")
	}
	if !strings.Contains(all, "备用") {
		t.Error("备用令牌应有明确标识（FR-TKN-035）")
	}
}

// 姓名与评语是外部输入，以 = 开头时不得被 Excel 当作公式执行。
func TestXLSXDoesNotCreateFormulas(t *testing.T) {
	r := mkMixedResult()
	r.Cells[0].SubjectName = `=cmd|' /c calc'!A1`
	var buf bytes.Buffer
	if err := StatsXLSX(&buf, r); err != nil {
		t.Fatal(err)
	}
	f := openXLSX(t, buf.Bytes())
	formula, err := f.GetCellFormula("等级题", "A2")
	if err != nil {
		t.Fatal(err)
	}
	if formula != "" {
		t.Fatalf("以 = 开头的姓名被写成了公式：%q", formula)
	}
	v, err := f.GetCellValue("等级题", "A2")
	if err != nil {
		t.Fatal(err)
	}
	if v != r.Cells[0].SubjectName {
		t.Fatalf("内容应原样保留为文本，得到 %q", v)
	}
}
