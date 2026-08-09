package stats

import (
	"testing"
)

// ── ST-02 分母口径（LLD 11.2）───────────────────────────────────────
//
// 判据：同一数据集下标准口径与实到口径的结果符合预期差值。
//
// 已有的 TestDenominatorIsExpectedCount 只验了标准口径那一侧的数值。
// ST-02 要验的是**两个口径之间的关系**——这正是最容易在汇报现场被追问、
// 也最容易被算错的地方：
//
//	标准口径分母 = 应参加人数（未提交者视同弃权，计入分母）
//	实到口径分母 = 实提交份数（只在核对时用，不进正式报告主表）
//
// 两者的比值恒等于提交率。若有人为了让数字好看而在某处偷换了分母，
// 这条恒等关系立刻不成立。

func TestST02_StandardVsActualDenominator(t *testing.T) {
	v := testVault(t)
	defer v.Close()

	// LLD 6.4 的样例：应参加 187 人，实提交 181 份
	// 优秀 92 / 称职 74 / 基本称职 12 / 不称职 3，显式弃权 0，未提交 6
	const expected, excellent = 187, 92
	p, f, recs := synth(t, v, expected, [4]int{92, 74, 12, 3})
	submitted := len(recs)
	if submitted != 181 {
		t.Fatalf("夹具应产生 181 份提交，实际 %d 份", submitted)
	}

	res, err := Aggregate(p, f, recs, v)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Cells[0]

	// ① 标准口径：分母是应参加人数
	wantStd := round1(float64(excellent) * 100 / float64(expected)) // 49.2
	if c.RateExcellent != wantStd {
		t.Errorf("标准口径优秀率应为 %.1f%%（%d/%d），得到 %.1f%%",
			wantStd, excellent, expected, c.RateExcellent)
	}

	// ② 实到口径：分母换成实提交份数，比率必然**更高**
	wantAct := round1(float64(excellent) * 100 / float64(submitted)) // 50.8
	if wantAct <= wantStd {
		t.Fatalf("夹具设计有误：实到口径应高于标准口径，%.1f%% vs %.1f%%", wantAct, wantStd)
	}

	// ③ 两个口径的关系：标准 / 实到 = 提交率
	//    49.2 / 50.8 ≈ 0.9685，提交率 181/187 ≈ 0.9679，差值应在舍入误差内
	ratio := c.RateExcellent / wantAct
	subRate := float64(submitted) / float64(expected)
	if diff := ratio - subRate; diff > 0.002 || diff < -0.002 {
		t.Errorf("两个口径的比值应等于提交率：得到 %.4f，提交率 %.4f，差 %.4f",
			ratio, subRate, diff)
	}

	// ④ 提交率本身要按标准口径算
	wantSubmitRate := round1(float64(submitted) * 100 / float64(expected)) // 96.8
	if res.SubmitRate != wantSubmitRate {
		t.Errorf("提交率应为 %.1f%%（%d/%d），得到 %.1f%%",
			wantSubmitRate, submitted, expected, res.SubmitRate)
	}

	// ⑤ 未提交的 6 份必须折进弃权，而不是消失
	//    四档票数合计 181，弃权应为 187-181 = 6
	sum := 0
	for _, n := range c.Counts {
		sum += n
	}
	if sum != submitted {
		t.Fatalf("四档票数合计应为 %d，得到 %d", submitted, sum)
	}
	if c.Abstain != expected-submitted {
		t.Errorf("弃权应为 %d（未提交折入），得到 %d", expected-submitted, c.Abstain)
	}

	t.Logf("ST-02：标准口径 %.1f%%（分母 %d）、实到口径 %.1f%%（分母 %d）、"+
		"提交率 %.1f%%，比值 %.4f≈提交率 %.4f",
		c.RateExcellent, expected, wantAct, submitted, res.SubmitRate, ratio, subRate)
}

// 分母被写小的情形必须报错而不是静默出一个 >100% 的比率。
//
// 这是现场真会发生的误操作：应参加人数填成了实到人数，甚至填成了
// 测评对象的人数。若静默通过，报表上会出现优秀率 120% 这种数字，
// 而它出现在正式材料上就是事故。
func TestST02_SubmittedOverExpectedIsRejected(t *testing.T) {
	v := testVault(t)
	defer v.Close()

	// 应参加填成 10，却收到 20 份
	p, f, recs := synth(t, v, 10, [4]int{20, 0, 0, 0})
	if _, err := Aggregate(p, f, recs, v); err == nil {
		t.Fatal("作答数超过应参加人数时应报错，而不是产出 >100% 的比率")
	} else {
		t.Logf("按预期拒绝：%v", err)
	}
}
