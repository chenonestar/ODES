package export

import (
	"bytes"
	"encoding/csv"
	"regexp"
	"strings"
	"testing"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
	"odes/internal/stats"
)

// 三个出口（统计页、正式报告、明细 CSV）必须对同一份数据摆出同一组比率。
// 口径不一致是这类系统最典型的事故：报告里写 92.3%、Excel 里算出 91.8%，
// 一旦出现就直接摧毁整套数据的可信度（HLD 7.3）。
//
// 统计页是 templ 生成物，不便在此渲染；这里守住的是**数据侧**：
// 报告与 CSV 都只从 stats.Cell 取数、不自行相加，因此只要两者与 Cell
// 一致，页面用同样的字段就不会走样。

func mkResult() *stats.Result {
	// 30 人应参加，某题：优秀 7、称职 11、基本称职 3、不称职 1、弃权 8
	c := stats.Cell{
		QuestionID: anon.NewID(), SubjectID: anon.NewID(),
		QuestionName: "德", SubjectName: "张三", Kind: model.KindGrade,
		Counts: [stats.NumGrades]int{7, 11, 3, 1}, Abstain: 8,
		Rates:    [stats.NumGrades]float64{23.3, 36.7, 10.0, 3.3},
		RateAbst: 26.7, RateTotal: 100.0,
		RateExcellent: 23.3, RateCompAbove: 60.0, Weighted: 79.1,
	}
	return &stats.Result{
		ProjectName: "测试项目", ExpectedCount: 30, Submitted: 22,
		Grades:      []string{"优秀", "称职", "基本称职", "不称职"},
		GradeScores: []int{100, 80, 60, 0},
		Cells:       []stats.Cell{c},
	}
}

func TestCSVCarriesEveryRateColumn(t *testing.T) {
	r := mkResult()
	var buf bytes.Buffer
	if err := StatsCSV(&buf, r); err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(buf.String(), "\uFEFF")
	// 口径说明是尾部的变长行，字段数与数据行不同，必须关掉一致性检查
	rd := csv.NewReader(strings.NewReader(body))
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("CSV 至少应有表头与一行数据，得到 %d 行", len(rows))
	}
	head, row := rows[0], rows[1]

	want := map[string]string{
		"优秀率": "23.3%", "称职率": "36.7%", "基本称职率": "10.0%",
		"不称职率": "3.3%", "弃权率": "26.7%", "合计": "100.0%",
		"称职以上率": "60.0%",
	}
	idx := map[string]int{}
	for i, h := range head {
		idx[h] = i
	}
	for col, exp := range want {
		i, ok := idx[col]
		if !ok {
			t.Errorf("CSV 缺少列「%s」", col)
			continue
		}
		if row[i] != exp {
			t.Errorf("CSV 列「%s」= %s，应为 %s", col, row[i], exp)
		}
	}
}

func TestReportCarriesEveryRateColumn(t *testing.T) {
	r := mkResult()
	p := &model.Project{
		ID: anon.NewID(), Name: "测试项目", ExpectedCount: 30,
		GradeScores: []int{100, 80, 60, 0}, CreatedAt: time.Now(),
	}
	var buf bytes.Buffer
	if err := ReportHTML(&buf, p, r); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, h := range []string{"优秀率", "称职率", "基本称职率", "不称职率",
		"弃权率", "合计", "称职以上率"} {
		if !strings.Contains(html, "<th>"+h+"</th>") &&
			!strings.Contains(html, `<th class="aux">`+h+"</th>") {
			t.Errorf("正式报告缺少列「%s」", h)
		}
	}
	for _, v := range []string{"23.3%", "36.7%", "10.0%", "3.3%", "26.7%", "60.0%"} {
		if !strings.Contains(html, v) {
			t.Errorf("正式报告缺少比率值 %s", v)
		}
	}
}

// 合计列的意义在于让「四档 + 弃权 ≡ 100.0%」这句承诺当场可核对。
// 若它不是恒等的 100.0%，这一列反而成了反证，必须守住。
func TestRateTotalIsAlwaysHundred(t *testing.T) {
	r := mkResult()
	var buf bytes.Buffer
	if err := StatsCSV(&buf, r); err != nil {
		t.Fatal(err)
	}
	body := strings.TrimPrefix(buf.String(), "\uFEFF")
	rd := csv.NewReader(strings.NewReader(body))
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	total := -1
	for i, h := range rows[0] {
		if h == "合计" {
			total = i
		}
	}
	if total < 0 {
		t.Fatal("CSV 没有「合计」列")
	}
	num := regexp.MustCompile(`^\d+\.\d%$`)
	for _, row := range rows[1:] {
		if len(row) <= total || !num.MatchString(row[total]) {
			continue // 口径说明等尾部非数据行
		}
		if row[total] != "100.0%" {
			t.Errorf("合计列出现 %s，应恒为 100.0%%", row[total])
		}
	}
}
