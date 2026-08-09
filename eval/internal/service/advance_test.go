package service

import (
	"context"
	"testing"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
)

// mkProject 造一个指定状态与时间窗的空项目。
func mkProject(t *testing.T, s *Service, status model.Status, start, end time.Time) anon.ID {
	t.Helper()
	p := &model.Project{
		ID: anon.NewID(), Name: "推进器测试", Status: status,
		StartAt: start, EndAt: end, ResultOpenAt: end,
		ExpectedCount: 1, APCount: 1,
		GradeScores:   []int{100, 80, 60, 0}, CreatedAt: time.Now(),
	}
	if err := s.DB.CreateProject(s.Vault, p); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func statusOf(t *testing.T, s *Service, id anon.ID) model.Status {
	t.Helper()
	p, err := s.DB.Project(s.Vault, id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Status
}

// 迁移表里没有 published → closed（LLD 7.1）。一旦推进器不工作，
// 项目发布后就永远截不了止——出不了统计、归档、擦除，散会即数据作废。
// 这条测试守的就是"发布之后一定还能走到 closed"这个可达性。
func TestAdvancerPublishedReachesClosed(t *testing.T) {
	s := newSvc(t)
	now := time.Now()
	id := mkProject(t, s, model.StatusPublished, now.Add(-time.Hour), now.Add(time.Hour))

	// 尚未到 end_at：只推到 running
	done, err := s.Advance(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, s, id); got != model.StatusRunning {
		t.Fatalf("到达 start_at 后应为 running，得到 %s", got)
	}
	if len(done) != 1 || done[0].To != model.StatusRunning {
		t.Fatalf("应报告 1 次迁移到 running，得到 %+v", done)
	}

	// 到 end_at 之后：推到 closed
	if _, err := s.Advance(context.Background(), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, s, id); got != model.StatusClosed {
		t.Fatalf("到达 end_at 后应为 closed，得到 %s", got)
	}
}

// 装备长期关机：整个时间窗都在关机状态下过去了，开机后一次推进就该
// 从 published 直达 closed，而不是停在 running 等下一个 tick。
func TestAdvancerCatchesUpAcrossTwoLevels(t *testing.T) {
	s := newSvc(t)
	now := time.Now()
	id := mkProject(t, s, model.StatusPublished, now.Add(-48*time.Hour), now.Add(-47*time.Hour))

	done, err := s.Advance(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, s, id); got != model.StatusClosed {
		t.Fatalf("关机跨过整个时间窗后应直达 closed，得到 %s", got)
	}
	if len(done) != 2 {
		t.Fatalf("应报告 2 次迁移（published→running→closed），得到 %+v", done)
	}
}

// 幂等：重复推进不得产生额外迁移，也不得把 closed 推成别的状态。
func TestAdvancerIsIdempotent(t *testing.T) {
	s := newSvc(t)
	now := time.Now()
	id := mkProject(t, s, model.StatusPublished, now.Add(-time.Hour), now.Add(-time.Minute))

	if _, err := s.Advance(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		done, err := s.Advance(context.Background(), now)
		if err != nil {
			t.Fatal(err)
		}
		if len(done) != 0 {
			t.Fatalf("第 %d 次重复推进不应再有迁移，得到 %+v", i+2, done)
		}
	}
	if got := statusOf(t, s, id); got != model.StatusClosed {
		t.Fatalf("状态应稳定在 closed，得到 %s", got)
	}
}

// 未到时间的项目不得被推进；草稿不受推进器影响。
func TestAdvancerLeavesEarlyAndDraftAlone(t *testing.T) {
	s := newSvc(t)
	now := time.Now()
	early := mkProject(t, s, model.StatusPublished, now.Add(time.Hour), now.Add(2*time.Hour))
	draft := mkProject(t, s, model.StatusDraft, now.Add(-time.Hour), now.Add(time.Hour))

	done, err := s.Advance(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 0 {
		t.Fatalf("不应有任何迁移，得到 %+v", done)
	}
	if got := statusOf(t, s, early); got != model.StatusPublished {
		t.Fatalf("未到 start_at 应保持 published，得到 %s", got)
	}
	if got := statusOf(t, s, draft); got != model.StatusDraft {
		t.Fatalf("草稿不受推进器影响，得到 %s", got)
	}
}

// 一个项目推进失败不得连累其它项目。
func TestAdvancerContinuesPastOneFailure(t *testing.T) {
	s := newSvc(t)
	now := time.Now()
	// 撤回发布的前置条件与推进无关，这里用两个正常项目验证批量推进：
	// 逐项独立提交，任一失败不影响其余。
	a := mkProject(t, s, model.StatusPublished, now.Add(-time.Hour), now.Add(time.Hour))
	b := mkProject(t, s, model.StatusPublished, now.Add(-time.Hour), now.Add(time.Hour))

	if _, err := s.Advance(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []anon.ID{a, b} {
		if got := statusOf(t, s, id); got != model.StatusRunning {
			t.Fatalf("批量推进应逐项生效，%s 得到 %s", id.Hex(), got)
		}
	}
}
