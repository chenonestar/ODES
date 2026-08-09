package stats

import (
	"encoding/json"
	"math/rand"
	"testing"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
)

func testVault(t *testing.T) *crypto.Vault {
	t.Helper()
	v, _, err := crypto.Init("Test-Password-123")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// ST-01　口径恒等式（LLD 11.2）
//
// 随机穷举 20000 组（分母 3–300，各档计数随机），四档 + 弃权比率
// 合计必须恒为 100.0%。这条是硬断言：宁可导出失败，也不能输出
// "四档加起来 99.9%" 这种自相矛盾的报表。
func TestST01_RateIdentityExhaustive(t *testing.T) {
	rng := rand.New(rand.NewSource(20260809))
	v := testVault(t)
	defer v.Close()

	rounds := 20000
	if testing.Short() {
		rounds = 2000 // go test -short 时缩短，完整穷举留给 CI
	}
	for i := 0; i < rounds; i++ {
		denom := 3 + rng.Intn(298)
		counts := [4]int{}
		left := denom
		for g := 0; g < 4 && left > 0; g++ {
			counts[g] = rng.Intn(left + 1)
			left -= counts[g]
		}
		// left 即弃权数

		p, f, recs := synth(t, v, denom, counts)
		res, err := Aggregate(p, f, recs, v)
		if err != nil {
			t.Fatalf("第 %d 组聚合失败（分母 %d，计数 %v）: %v", i, denom, counts, err)
		}
		for _, c := range res.Cells {
			if c.Kind != model.KindGrade {
				continue
			}
			total := c.Rates[0] + c.Rates[1] + c.Rates[2] + c.Rates[3] + c.RateAbst
			if round1(total) != 100.0 {
				t.Fatalf("恒等式不成立：分母 %d、计数 %v → 合计 %.1f%%",
					denom, counts, total)
			}
			// 四档计数 + 弃权 ≡ 应参加人数
			sum := c.Abstain
			for _, n := range c.Counts {
				sum += n
			}
			if sum != denom {
				t.Fatalf("计数不守恒：分母 %d，四档+弃权 = %d", denom, sum)
			}
		}
	}
	t.Logf("随机穷举 %d 组，恒等式失败 0 组", rounds)
}

// TestDenominatorIsExpectedCount 验证分母取应参加人数而非实提交份数。
//
// 这是文档里"最重要的一条"（SRS 6.2）：同一批数据，两种分母能算出
// 差异显著的两个优秀率。
func TestDenominatorIsExpectedCount(t *testing.T) {
	v := testVault(t)
	defer v.Close()

	// LLD 6.4 的样例：应参加 187 人，实提交 181 份
	// 优秀 92 / 称职 74 / 基本称职 12 / 不称职 3 / 弃权 6
	p, f, recs := synth(t, v, 187, [4]int{92, 74, 12, 3})
	res, err := Aggregate(p, f, recs, v)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Cells[0]

	// 标准口径：92/187 = 49.2%
	if c.RateExcellent != 49.2 {
		t.Errorf("标准口径优秀率应为 49.2%%，得到 %.1f%%", c.RateExcellent)
	}
	// 称职以上率 (92+74)/187 = 88.8%
	if c.RateCompAbove != 88.8 {
		t.Errorf("称职以上率应为 88.8%%，得到 %.1f%%", c.RateCompAbove)
	}
	// 若误用实到口径（181），优秀率会变成 50.8% —— 相差 1.6 个百分点
	wrong := round1(92 * 100.0 / 181)
	t.Logf("标准口径 %.1f%% vs 实到口径 %.1f%%，相差 %.1f 个百分点",
		c.RateExcellent, wrong, wrong-c.RateExcellent)
}

// TestUnsubmittedCountsAsAbstain 验证未提交部分并入弃权、计入分母。
func TestUnsubmittedCountsAsAbstain(t *testing.T) {
	v := testVault(t)
	defer v.Close()

	// 应参加 100，只有 60 份提交且全部选优秀
	p, f, recs := synth(t, v, 100, [4]int{60, 0, 0, 0})
	res, err := Aggregate(p, f, recs, v)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Cells[0]
	if c.RateExcellent != 60.0 {
		t.Errorf("优秀率应为 60.0%%（60/100），得到 %.1f%%", c.RateExcellent)
	}
	if c.Abstain != 40 {
		t.Errorf("未提交的 40 份应并入弃权，得到弃权 %d", c.Abstain)
	}
	if c.RateAbst != 40.0 {
		t.Errorf("弃权率应为 40.0%%，得到 %.1f%%", c.RateAbst)
	}
}

// TestSubmittedExceedsExpectedIsRejected 验证提交数超过应参加人数时
// 明确报错而非静默截断——静默截断会让分母口径失去意义。
func TestSubmittedExceedsExpectedIsRejected(t *testing.T) {
	v := testVault(t)
	defer v.Close()
	p, f, recs := synth(t, v, 5, [4]int{10, 0, 0, 0})
	if _, err := Aggregate(p, f, recs, v); err == nil {
		t.Fatal("提交数超过应参加人数时应报错，实际通过了")
	} else {
		t.Logf("按预期报错：%v", err)
	}
}

// TestWeightedScoreExcludesAbstain 验证加权得分不把弃权计入分子分母。
func TestWeightedScoreExcludesAbstain(t *testing.T) {
	v := testVault(t)
	defer v.Close()
	// 10 人应参加，4 优秀 + 2 称职，其余 4 人未提交（并入弃权）
	p, f, recs := synth(t, v, 10, [4]int{4, 2, 0, 0})
	res, err := Aggregate(p, f, recs, v)
	if err != nil {
		t.Fatal(err)
	}
	// (4×100 + 2×80) / 6 = 93.3
	if got := res.Cells[0].Weighted; got != 93.3 {
		t.Errorf("加权得分应为 93.3（弃权不计入），得到 %.1f", got)
	}
}

// TestSmallSampleProtection 验证分组对象数 < 3 时在服务层拦截。
func TestSmallSampleProtection(t *testing.T) {
	v := testVault(t)
	defer v.Close()
	p, f, recs := synth(t, v, 10, [4]int{5, 0, 0, 0})
	f.Subjects[0].Tag = "小组"
	res, _ := Aggregate(p, f, recs, v)
	if _, err := res.FilterByTag(f, "小组"); err != ErrSampleTooSmall {
		t.Fatalf("分组内仅 1 个对象时应返回 ErrSampleTooSmall，得到 %v", err)
	}
}

// synth 造一个单题单对象的项目与相应作答记录。
func synth(t *testing.T, v *crypto.Vault, expected int, counts [4]int) (
	*model.Project, *model.Form, []model.AnswerRecord) {
	t.Helper()

	pid, qid, sid := anon.NewID(), anon.NewID(), anon.NewID()
	p := &model.Project{
		ID: pid, Name: "测试", ExpectedCount: expected,
		GradeScores: []int{100, 80, 60, 0},
	}
	f := &model.Form{
		Grades:   []string{"优秀", "称职", "基本称职", "不称职"},
		Subjects: []model.Subject{{ID: sid, ProjectID: pid, Name: "张三"}},
		Groups: []model.QuestionGroup{{
			ID: anon.NewID(), ProjectID: pid, Title: "组",
			Questions: []model.Question{{
				ID: qid, Kind: model.KindGrade, Title: "题", Required: true,
				SubjectIDs: []anon.ID{sid},
			}},
		}},
	}

	var recs []model.AnswerRecord
	add := func(grade int) {
		g := grade
		a := model.Answer{V: 1, Items: []model.AnswerItem{
			{Q: qid.Hex(), S: sid.Hex(), Grade: &g},
		}}
		b, _ := json.Marshal(a)
		ct, err := v.Seal(b, pid.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, model.AnswerRecord{
			ID: anon.NewID(), ProjectID: pid, BatchNo: 1, Payload: ct,
		})
	}
	for g, n := range counts {
		for i := 0; i < n; i++ {
			add(g)
		}
	}
	return p, f, recs
}
