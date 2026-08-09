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
func (s *Service) Preflight(p *model.Project, certUsable bool, certMsg string) []Check {
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

	// NFR-PER-020 是三个分项上限，不是一个。原先只查了作答项，
	// 题目数与对象数两条没有把关。
	add("PC-08a", len(f.Subjects) <= MaxSubjects,
		fmt.Sprintf("测评对象 %d ≤ %d", len(f.Subjects), MaxSubjects))
	add("PC-08b", qn <= MaxQuestions,
		fmt.Sprintf("题目数 %d ≤ %d", qn, MaxQuestions))
	items := f.ItemCount()
	add("PC-08", items <= MaxItems,
		fmt.Sprintf("展开后作答项 %d ≤ %d", items, MaxItems))

	// PC-09 的判据是**证书剩余有效期 ≥1 天**（LLD 7.3），比启动自检的
	// CK-04（≥21 天）宽。两者用途不同：CK-04 是出发前的提醒，PC-09 是
	// 发布的硬门槛。原先直接复用了 certOK，等于把 21 天的提醒变成了
	// 发布拦截——证书剩 10 天时文档允许发布，实现却拦住了。
	add("PC-09", certUsable, certMsg)
	return out
}

// MaxSubjects / MaxQuestions / MaxItems 是 NFR-PER-020 的三条分项容量上限。
const (
	MaxSubjects  = 20
	MaxQuestions = 50
)

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

// ── 推进器（LLD 7.1）────────────────────────────────────────────────
//
// 迁移表里有两行的触发者是「推进器」而不是管理员：
//
//	published → running   到达 start_at，自动
//	running   → closed    到达 end_at，自动
//
// 原先这段逻辑写在 cmd/eval 的 ticker 里，既没有测试覆盖，也没法在
// 启动时先补一次。移到服务层的直接原因是 M-2：迁移表里本来就没有
// published → closed 这一行，删掉它之后，"发布后到 start_at 之前"
// 这段时间里项目既不能作答也不能截止——推进器是唯一的出路，它必须
// 是可测的。
//
// 刻意做成"到点即推进"的幂等操作而不是一次性定时触发：考察装备长期
// 关机，start_at 很可能整个是在关机状态下过去的，开机后必须自己追上，
// 不能依赖"那一刻恰好有人在跑程序"。

// Advanced 记录一次实际发生的自动迁移，供调用方打印现场日志。
type Advanced struct {
	Name     string
	From, To model.Status
}

// Advance 把所有项目推进到与当前时间相符的状态。幂等：重复调用无副作用。
func (s *Service) Advance(ctx context.Context, now time.Time) ([]Advanced, error) {
	ps, err := s.DB.Projects(s.Vault)
	if err != nil {
		return nil, err
	}
	var done []Advanced
	var firstErr error
	for _, p := range ps {
		// for 而非 if：关机数日后开机时，published 应当一路走到 closed，
		// 而不是先停在 running 等下一次 tick。
		for {
			from := p.Status
			var to model.Status
			switch {
			case from == model.StatusPublished && !now.Before(p.StartAt):
				to = model.StatusRunning
			case from == model.StatusRunning && !now.Before(p.EndAt):
				to = model.StatusClosed
			default:
				to = ""
			}
			if to == "" {
				break
			}
			if err := s.Transition(ctx, p.ID, to); err != nil {
				// 一个项目推进失败不连累其它项目，但错误必须往上报：
				// 自动截止里含 VACUUM，失败即意味着匿名性承诺尚未成立。
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			done = append(done, Advanced{Name: p.Name, From: from, To: to})
			p.Status = to
		}
	}
	return done, firstErr
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
