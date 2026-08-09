package service

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
	"odes/internal/store"
)

// ── 发布前置校验（LLD 7.3）──────────────────────────────────────────

type Check struct {
	Code string
	Msg  string
	OK   bool
}

// Preflight 返回**全部**未通过项，而非只报第一条。
// 现场时间紧张，逐条试错的代价高。
func (s *Service) Preflight(p *model.Project, certOK bool, certMsg string) []Check {
	var out []Check
	add := func(code string, ok bool, msg string) {
		out = append(out, Check{Code: code, OK: ok, Msg: msg})
	}

	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		add("PC-00", false, "读取测评表失败: "+err.Error())
		return out
	}

	add("PC-01", len(f.Subjects) > 0, "至少 1 个测评对象")

	qn, unlinked := 0, ""
	for _, g := range f.Groups {
		for _, q := range g.Questions {
			qn++
			if len(q.SubjectIDs) == 0 && unlinked == "" {
				unlinked = q.Title
			}
		}
	}
	if unlinked != "" {
		add("PC-02", false, fmt.Sprintf("题目「%s」未关联任何测评对象", unlinked))
	} else {
		add("PC-02", qn > 0, "至少 1 道题且每题至少关联 1 个对象")
	}

	add("PC-03", p.StartAt.Before(p.EndAt) && !p.ResultOpenAt.Before(p.EndAt),
		"时间设置合法（开始 < 截止 ≤ 结果开放）")
	add("PC-04", p.ExpectedCount > 0, "已确认应参加人数（比率分母）")

	total, _, _ := s.DB.CountTokens(p.ID)
	add("PC-05", total >= p.ExpectedCount,
		fmt.Sprintf("令牌数量足够（已生成 %d，应参加 %d）", total, p.ExpectedCount))
	add("PC-06", p.PrintedAt != nil, "令牌单已打印")

	trial, _ := s.DB.CountTrialAnswers(p.ID)
	add("PC-07", trial > 0, "试填已完成至少 1 次")

	items := f.ItemCount()
	add("PC-08", items <= MaxItems,
		fmt.Sprintf("展开后作答项 %d ≤ %d", items, MaxItems))

	if certMsg == "" {
		certMsg = "证书剩余有效期充足"
	}
	add("PC-09", certOK, certMsg)
	return out
}

// MaxItems 是展开后作答项上限（NFR-PER-020）。
const MaxItems = 400

func AllOK(cs []Check) bool {
	for _, c := range cs {
		if !c.OK {
			return false
		}
	}
	return true
}

// ── 状态迁移（LLD 7.1）──────────────────────────────────────────────

func (s *Service) Transition(ctx context.Context, id anon.ID, to model.Status) error {
	p, err := s.DB.Project(s.Vault, id)
	if err != nil {
		return err
	}
	if !model.CanTransition(p.Status, to) {
		return fmt.Errorf("不允许的状态迁移：%s → %s", p.Status.Label(), to.Label())
	}

	switch {
	case p.Status == model.StatusPublished && to == model.StatusDraft:
		// 撤回发布：一旦存在提交记录则不可撤回（FR-PRJ-025）
		n, err := s.DB.CountAnswers(id)
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("已有 %d 份提交，不可撤回发布", n)
		}

	case to == model.StatusPublished:
		// 发布的副作用：清空试填数据区（FR-PRJ-033）
		err := s.DB.Tx(ctx, func(tx *sql.Tx) error {
			if err := s.DB.ClearTrialData(tx, id); err != nil {
				return err
			}
			return s.DB.UpdateProjectStatus(tx, id, to, time.Now().Format(time.RFC3339))
		})
		if err != nil {
			return err
		}
		_ = s.DB.Log(&id, "publish", "ok", "发布测评，已清空试填数据")
		return nil

	case to == model.StatusClosed:
		return s.closeProject(ctx, p)
	}

	if err := s.DB.UpdateProjectStatus(nil, id, to, time.Now().Format(time.RFC3339)); err != nil {
		return err
	}
	s.invalidateStats(id)
	_ = s.DB.Log(&id, "transition", "ok", string(p.Status)+" → "+string(to))
	return nil
}

// closeProject 执行进入「已截止」的全部副作用。
//
// wal_checkpoint(TRUNCATE) + VACUUM 是 NFR-ANO-031 的实现，是这次状态
// 迁移的**组成部分而非可选优化**（LLD 7.2）：
//   - 截止前 WAL 里仍按提交先后排列帧，解析 WAL 可还原顺序；
//   - VACUUM 按随机主键重建文件，使物理布局与提交顺序彻底无关。
//
// 执行失败必须告警——此时匿名性承诺尚未成立，NFR-ANO-091 的时序专项
// 测试也应在这一步之后才执行。
func (s *Service) closeProject(ctx context.Context, p *model.Project) error {
	if err := s.DB.UpdateProjectStatus(nil, p.ID, model.StatusClosed,
		time.Now().Format(time.RFC3339)); err != nil {
		return err
	}
	s.invalidateStats(p.ID)

	if err := s.DB.Checkpoint(); err != nil {
		_ = s.DB.Log(&p.ID, "close", "fail", "WAL 截断或 VACUUM 失败: "+err.Error())
		return fmt.Errorf("测评已截止，但匿名化重建失败，匿名性承诺尚未成立：%w", err)
	}
	_ = s.DB.Log(&p.ID, "close", "ok", "已截止；WAL 已截断、数据库已按随机主键重建")
	return nil
}

// ── 令牌生成（LLD 4.3）──────────────────────────────────────────────

// SpareRatio 是备用令牌比例（FR-TKN-035，暂定 10%，见 TBD-06）。
const SpareRatio = 0.10

// GenerateTokens 生成正式令牌与备用令牌。
//
// 三个步骤缺一不可：
//  1. 生成 —— 令牌值取 crypto/rand，主键为随机 UUID；
//  2. 随机重排 —— 打印前对令牌集合执行一次 Fisher–Yates（FR-TKN-025）。
//     V1.0 中令牌"按名单顺序生成、按打印页位置分发"，使得打印底稿即
//     人码对照表。这一步切断的是该链路的第一环；
//  3. 分配 AP 序号 —— 重排后按位置轮转。因集合已随机，分流天然随机，
//     不会形成"某处室全在同一 AP"的群体聚集（SRS 8.3.2）。
func (s *Service) GenerateTokens(ctx context.Context, projectID anon.ID,
	count, batches, apCount int) error {

	if count <= 0 {
		return fmt.Errorf("令牌数量须大于 0")
	}
	if apCount < 1 {
		apCount = 1
	}
	spare := int(math.Ceil(float64(count) * SpareRatio))

	mk := func(isSpare bool, n int) []model.Token {
		out := make([]model.Token, 0, n)
		seen := map[string]bool{}
		for len(out) < n {
			v, c := anon.NewToken(), anon.NewShortCode()
			if seen[v] || seen[c] {
				continue // 生成时做唯一性校验并重试（LLD 4.2）
			}
			seen[v], seen[c] = true, true
			out = append(out, model.Token{
				ID: anon.NewID(), ProjectID: projectID,
				Value: v, ShortCode: c, IsSpare: isSpare, BatchNo: 1, APIndex: 1,
			})
		}
		return out
	}

	formal := mk(false, count)

	// ★ 随机重排——这一步不可省略，见上方说明
	anon.Shuffle(formal)

	perBatch := count
	if batches > 1 {
		perBatch = int(math.Ceil(float64(count) / float64(batches)))
	}
	for i := range formal {
		formal[i].APIndex = i%apCount + 1
		formal[i].BatchNo = i/perBatch + 1
	}

	all := append(formal, mk(true, spare)...)
	if err := s.DB.InsertTokens(ctx, all); err != nil {
		return err
	}

	// 试填令牌：独立标识，可反复使用不失效（FR-PRJ-030）
	for i := 0; i < 3; i++ {
		if err := s.DB.InsertTrialToken(projectID, anon.NewToken()); err != nil {
			return err
		}
	}
	return s.DB.Log(&projectID, "generate_tokens", "ok",
		fmt.Sprintf("生成正式令牌 %d 个、备用 %d 个、试填 3 个，已随机重排并按 %d 台 AP 分流",
			count, spare, apCount))
}

// PrintSheets 返回按 AP 分组的令牌单数据，并记录打印留痕（PC-06）。
//
// 再次 Shuffle：即便数据库读出顺序已因随机主键而无序，也不把任何
// 可能的顺序带进打印稿。
func (s *Service) PrintSheets(projectID anon.ID) ([]model.Token, error) {
	toks, err := s.DB.Tokens(projectID, false)
	if err != nil {
		return nil, err
	}
	anon.Shuffle(toks)
	if err := s.DB.MarkPrinted(projectID, s.Domain); err != nil {
		return nil, err
	}
	_ = s.DB.Log(&projectID, "print_tokens", "ok",
		fmt.Sprintf("打印令牌单 %d 张，域名快照 %s", len(toks), s.Domain))
	return toks, nil
}

// CheckDomainConsistency 实现 LLD 12.3 的二维码一致性约束。
//
// 令牌单一经打印其 URL 即固化。若打印后修改了 domain.primary，
// 已打印的二维码将失效——必须在启动自检时报错，而不是等到现场。
func (s *Service) CheckDomainConsistency(p *model.Project) error {
	if p.PrintedAt == nil || p.TokenDomain == "" || p.TokenDomain == s.Domain {
		return nil
	}
	return fmt.Errorf(
		"项目「%s」的令牌单使用的域名为 %s，与当前配置 %s 不一致：已打印的二维码将无法访问",
		p.Name, p.TokenDomain, s.Domain)
}

var _ = store.ErrNotFound
