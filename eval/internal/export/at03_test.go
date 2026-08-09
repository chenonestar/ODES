package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
)

// ── AT-03 输出顺序随机（LLD 11.1）────────────────────────────────────
//
// NFR-ANO-030 要求对外输出的记录顺序**每次读取时重新随机排列**。
// 已有的 TestShuffleBeforeOutput 只是个粗检查——它确认包里出现过
// anon.Shuffle 的调用，但不检查顺序是否真的每次都变。AT-03 补的正是
// 这一段：连续导出 10 次，两两比较记录顺序的秩相关系数。
//
// 为什么这条重要：若导出顺序稳定，把两次导出并排一放就能看出哪一行
// 对应哪一份作答；再结合"第几个交卷"的现场观察，就能往回推人。

// 判据修订（同 AT-01 的那处）
//
// 文档原文是「两两之间相关系数均 < 0.15」。这个判据在正确实现上也会
// 大概率失败：10 次导出有 45 对，n = 200 时随机排列下 ρ 的标准差为
// 1/√199 ≈ 0.0709，0.15 只相当于 2.12σ，单对自然超出的概率约 3.4%。
// 蒙特卡洛实测（2000 次试验）：**按字面判据有 80% 的构建会红**，
// 而红的时候并不代表匿名性出了问题。
//
// 改为与 AT-01 一致的两段判据：
//   - 45 对 |ρ| 的**中位数** < 0.15（随机下实测均值 0.048，最大 0.080）
//   - 任一对不得超过 5σ ≈ 0.354（随机下 2000 次试验最大 0.312）
//
// 一旦顺序真的固定下来，每一对都会接近 1.0，两段判据同时触发。
const (
	at03Exports   = 10
	at03Records   = 200
	at03MedianMax = 0.15
	at03PairMax   = 0.3544 // 5 / √199
)

func TestAT03_ExportOrderIsRandomEachTime(t *testing.T) {
	if testing.Short() {
		t.Skip("AT-03 要跑 10 次全量导出，-short 下跳过")
	}
	v, _, err := crypto.Init("Test-Password-123")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	p, f, recs := at03Fixture(t, v, at03Records)

	orders := make([][]int, 0, at03Exports)
	for i := 0; i < at03Exports; i++ {
		orders = append(orders, at03ExportOrder(t, p, f, recs, v))
	}

	var rs []float64
	for i := 0; i < len(orders); i++ {
		for j := i + 1; j < len(orders); j++ {
			r := absF(spearmanPair(orders[i], orders[j]))
			rs = append(rs, r)
			if r > at03PairMax {
				t.Errorf("第 %d 次与第 %d 次导出的顺序相关系数 %.4f 超过单对上限 %.4f",
					i+1, j+1, r, at03PairMax)
			}
		}
	}
	sort.Float64s(rs)
	median := rs[len(rs)/2]
	t.Logf("AT-03：%d 次导出、%d 对比较，|ρ| 中位数 %.4f，最大 %.4f（判据：中位数 < %.2f）",
		at03Exports, len(rs), median, rs[len(rs)-1], at03MedianMax)
	if median >= at03MedianMax {
		t.Fatalf("导出顺序疑似可预测：|ρ| 中位数 %.4f ≥ %.2f", median, at03MedianMax)
	}
}

// 对照组：顺序固定时本测试必须失败，否则它只是个摆设。
func TestAT03_ControlStableOrderIsDetected(t *testing.T) {
	fixed := make([]int, at03Records)
	for i := range fixed {
		fixed[i] = i
	}
	var rs []float64
	for i := 0; i < at03Exports; i++ {
		for j := i + 1; j < at03Exports; j++ {
			rs = append(rs, absF(spearmanPair(fixed, fixed)))
		}
	}
	sort.Float64s(rs)
	if median := rs[len(rs)/2]; median < at03MedianMax {
		t.Fatalf("顺序完全固定时中位数应为 1.0000，得到 %.4f——判据失效", median)
	}
	t.Logf("对照组：顺序固定时 |ρ| = %.4f，判据确实能发现问题", rs[0])
}

// at03Fixture 造 n 份内容互不相同的作答记录。
// 内容必须唯一：导出后要靠它把行认回来，重复内容会让顺序无从比较。
func at03Fixture(t *testing.T, v *crypto.Vault, n int) (
	*model.Project, *model.Form, []model.AnswerRecord) {
	t.Helper()

	pid, qid, sid := anon.NewID(), anon.NewID(), anon.NewID()
	p := &model.Project{ID: pid, Name: "AT-03", ExpectedCount: n}
	f := &model.Form{
		Subjects: []model.Subject{{ID: sid, Name: "张三"}},
		Groups: []model.QuestionGroup{{
			ID: anon.NewID(), Title: "组",
			Questions: []model.Question{{
				ID: qid, Kind: model.KindText, Title: "评语",
				SubjectIDs: []anon.ID{sid},
			}},
		}},
	}

	recs := make([]model.AnswerRecord, 0, n)
	for i := 0; i < n; i++ {
		a := model.Answer{V: 1, Items: []model.AnswerItem{{
			Q: qid.Hex(), S: sid.Hex(), Text: at03Mark(i),
		}}}
		plain, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := v.Seal(plain, pid.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, model.AnswerRecord{
			ID: anon.NewID(), ProjectID: pid, Payload: payload,
		})
	}
	return p, f, recs
}

// at03Mark 生成可从导出内容里认回来的唯一标记。
func at03Mark(i int) string {
	return "记录标记#" + itoa(i) + "#"
}

// at03ExportOrder 导出一次，返回"第 k 行是第几号记录"。
func at03ExportOrder(t *testing.T, p *model.Project, f *model.Form,
	recs []model.AnswerRecord, v *crypto.Vault) []int {
	t.Helper()

	var buf bytes.Buffer
	if err := RawCSV(&buf, p, f, recs, v); err != nil {
		t.Fatal(err)
	}
	rd := csv.NewReader(strings.NewReader(
		strings.TrimPrefix(buf.String(), "\uFEFF")))
	rd.FieldsPerRecord = -1
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("导出应有表头与 %d 行数据，实际 %d 行", len(recs), len(rows))
	}

	out := make([]int, 0, len(recs))
	for _, row := range rows[1:] {
		idx := -1
		for _, cell := range row {
			if k := strings.Index(cell, "记录标记#"); k >= 0 {
				rest := cell[k+len("记录标记#"):]
				if e := strings.Index(rest, "#"); e > 0 {
					idx = atoiSafe(rest[:e])
				}
			}
		}
		if idx < 0 {
			t.Fatalf("导出行里找不到记录标记：%v", row)
		}
		out = append(out, idx)
	}
	if len(out) != len(recs) {
		t.Fatalf("导出行数 %d，应为 %d", len(out), len(recs))
	}
	return out
}

// spearmanPair 计算两个排列之间的秩相关系数。
func spearmanPair(a, b []int) float64 {
	n := len(a)
	if n < 2 || len(b) != n {
		return 0
	}
	posB := make(map[int]int, n)
	for i, v := range b {
		posB[v] = i
	}
	var sumD2 float64
	for i, v := range a {
		d := float64(i - posB[v])
		sumD2 += d * d
	}
	fn := float64(n)
	return 1 - 6*sumD2/(fn*(fn*fn-1))
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
