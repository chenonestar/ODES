package service

import (
	"context"
	"fmt"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
)

// SeedDemo 生成一份演示项目，供开发机上立刻走通全流程。
//
// 用的是 SRS 附录里的"德能勤绩廉"标准模板结构（FR-FRM-062）。
// 姓名为虚构，仅用于调试。
func (s *Service) SeedDemo(ctx context.Context) (anon.ID, error) {
	now := time.Now()
	p := &model.Project{
		ID:            anon.NewID(),
		Name:          "2026 年度 XX 单位领导班子民主测评（演示）",
		Intro:         "请根据该同志本年度工作表现，客观公正地作出评价。本次测评全程匿名，作答记录与您的身份不可关联。",
		Status:        model.StatusDraft,
		StartAt: now.Add(-time.Hour),
		EndAt:   now.Add(72 * time.Hour),
		// 结果开放时间不得早于截止时间（schema CHECK 约束）。
		// 演示时想立刻看统计，就在管理端点「手动截止」——
		// 那正是现场的真实流程，也会顺带跑一遍 WAL 截断 + VACUUM。
		ResultOpenAt: now.Add(72 * time.Hour),
		ExpectedCount: 30,
		APCount:       4,
		GradeScores:   []int{100, 80, 60, 0},
		CreatedAt:     now,
	}
	if err := s.DB.CreateProject(s.Vault, p); err != nil {
		return p.ID, err
	}

	subjects := []struct{ name, duty, tag string }{
		{"张 三", "局长", "班子成员"},
		{"李 四", "副局长", "班子成员"},
		{"王 五", "副局长", "班子成员"},
		{"赵 六", "党组成员", "班子成员"},
	}
	var sids []anon.ID
	for i, sj := range subjects {
		s0 := &model.Subject{
			ID: anon.NewID(), ProjectID: p.ID,
			Name: sj.name, Duty: sj.duty, Tag: sj.tag, SortNo: i,
		}
		if err := s.DB.CreateSubject(s.Vault, s0); err != nil {
			return p.ID, err
		}
		sids = append(sids, s0.ID)
	}

	groups := []struct {
		title string
		qs    []struct {
			title string
			kind  model.QuestionKind
			req   bool
		}
	}{
		{"德（政治品德）", []struct {
			title string
			kind  model.QuestionKind
			req   bool
		}{
			{"政治立场和党性修养", model.KindGrade, true},
			{"道德品质和个人修养", model.KindGrade, true},
		}},
		{"能（工作能力）", []struct {
			title string
			kind  model.QuestionKind
			req   bool
		}{
			{"业务水平与专业素养", model.KindGrade, true},
			{"组织协调与决策能力", model.KindGrade, true},
		}},
		{"勤（工作态度）", []struct {
			title string
			kind  model.QuestionKind
			req   bool
		}{
			{"事业心与责任感", model.KindGrade, true},
		}},
		{"绩（工作实绩）", []struct {
			title string
			kind  model.QuestionKind
			req   bool
		}{
			{"完成本职工作的实际成效", model.KindGrade, true},
		}},
		{"廉（廉洁自律）", []struct {
			title string
			kind  model.QuestionKind
			req   bool
		}{
			{"遵守廉洁自律各项规定", model.KindGrade, true},
		}},
		{"综合评价", []struct {
			title string
			kind  model.QuestionKind
			req   bool
		}{
			{"总体评价", model.KindGrade, true},
			{"请写下具体意见和建议", model.KindText, false},
		}},
	}

	sort := 0
	for gi, g := range groups {
		grp := &model.QuestionGroup{
			ID: anon.NewID(), ProjectID: p.ID, Title: g.title, SortNo: gi,
		}
		if err := s.DB.CreateGroup(grp); err != nil {
			return p.ID, err
		}
		for _, q := range g.qs {
			qq := &model.Question{
				ID: anon.NewID(), GroupID: grp.ID, Kind: q.kind,
				Title: q.title, Required: q.req, SortNo: sort,
				SubjectIDs: sids,
			}
			if q.kind == model.KindText {
				qq.Config.MaxChars = 500
				qq.Hint = "选填。请写下具体意见（不超过 500 字）"
			}
			if err := s.DB.CreateQuestion(p.ID, qq); err != nil {
				return p.ID, err
			}
			sort++
		}
	}

	// 名单：只用于确定令牌数量与核对应参加人数，与令牌表无任何字段关联
	for i := 0; i < p.ExpectedCount; i++ {
		if err := s.DB.InsertRoster(s.Vault, p.ID, fmt.Sprintf("参评人员 %02d", i+1)); err != nil {
			return p.ID, err
		}
	}

	if err := s.GenerateTokens(ctx, p.ID, p.ExpectedCount, 1, p.APCount); err != nil {
		return p.ID, err
	}
	_ = s.DB.Log(&p.ID, "seed", "ok", "生成演示项目（4 位测评对象 × 8 题，30 份令牌）")
	return p.ID, nil
}
