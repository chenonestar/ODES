// Package service 是业务编排层，也是**唯一可以开启事务的层**。
//
// 依赖方向（HLD 4）：admin / evalui → service → store / crypto / stats / anon。
// 反向依赖一律禁止。
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"odes/internal/anon"
	"odes/internal/crypto"
	"odes/internal/model"
	"odes/internal/stats"
	"odes/internal/store"
)

// 面向参评人员的错误码（LLD 8.4）。
var (
	ErrTokenInvalid = errors.New("TOKEN_INVALID")
	ErrTokenUsed    = errors.New("TOKEN_USED")
	ErrNotStarted   = errors.New("NOT_STARTED")
	ErrClosed       = errors.New("CLOSED")
	ErrIncomplete   = errors.New("INCOMPLETE")
)

type Service struct {
	DB     *store.DB
	Vault  *crypto.Vault
	Domain string // config.toml 的 domain.primary，用于二维码 URL 与一致性校验

	// statsCache 缓存截止后的聚合结果：数据不再变化，重复查看统计页
	// 无需重新解密（HLD 7.2）。
	statsCache map[anon.ID]*stats.Result

	mu sync.Mutex
	// lastArchive 记住最近一次构建并下载的归档包。归档确认要比对的是
	// 管理员真正保存到介质上的那一份，不能临时重新生成（见 archive.go）。
	lastArchive map[anon.ID]archiveBlob
}

type archiveBlob struct {
	data []byte
	sha  string
}

func New(db *store.DB, v *crypto.Vault, domain string) *Service {
	return &Service{
		DB: db, Vault: v, Domain: domain,
		statsCache:  map[anon.ID]*stats.Result{},
		lastArchive: map[anon.ID]archiveBlob{},
	}
}

// ── 提交事务（LLD 5.1、ADR-004）─────────────────────────────────────

// Submit 是参评人员提交作答的唯一入口。
//
// 两条写入放在**同一事务**内，且顺序为「先写作答、后置位令牌」。
//
// 为什么同事务：需求 NFR-REL-011（幂等）与 NFR-ANO-020（切断时序）在此
// 正面相撞。分开两个事务能让 WAL 里两条记录分离，但中间崩溃会产生
// "令牌已用但作答丢失"——参评人员看到成功页、票却没了，这是对测评
// 有效性的直接破坏，且散会后无法补救。失败代价不对称，故取同事务。
// 同事务的弱点（WAL 中两条记录相邻）只在取证条件下成立，已明确排除在
// 承诺边界之外，且截止时的 VACUUM 会消除该痕迹。
//
// 为什么先写作答：即便有人解析 WAL，也需要额外推断两条记录的配对关系，
// 而不是直接按"令牌置位紧跟着作答插入"读出来。
func (s *Service) Submit(ctx context.Context, tokenValue string, answer *model.Answer) error {
	tk, err := s.DB.AnyTokenByValue(tokenValue)
	if err != nil {
		return ErrTokenInvalid
	}
	p, err := s.DB.Project(s.Vault, tk.ProjectID)
	if err != nil {
		return ErrTokenInvalid
	}
	if err := s.checkWindow(p); err != nil {
		return err
	}
	if tk.Used {
		return ErrTokenUsed
	}

	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return err
	}
	// 服务端重新执行完整校验，不信任前端的必填检查——
	// 前端校验是体验优化，不是安全边界（LLD 8.3）。
	if miss := Validate(f, answer); len(miss) > 0 {
		return &incompleteError{Missing: miss}
	}

	plain, err := json.Marshal(normalize(answer))
	if err != nil {
		return err
	}
	payload, err := s.Vault.Seal(plain, p.ID.Bytes())
	if err != nil {
		return err
	}

	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		// ① 先写作答
		rec := &model.AnswerRecord{
			ID:        anon.NewID(),
			ProjectID: p.ID,
			BatchNo:   tk.BatchNo,
			Payload:   payload,
		}
		if err := store.InsertAnswerTx(tx, rec); err != nil {
			return err
		}
		// ② 再条件置位令牌：影响行数为 0 说明并发提交已被抢先，
		//    回滚使刚写入的作答一并撤销。
		n, err := store.MarkTokenUsedTx(tx, tk.ID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrTokenUsed
		}
		return nil
	})
}

// SubmitTrial 走试填数据区：不消耗令牌、不进统计、不进导出（FR-PRJ-031）。
func (s *Service) SubmitTrial(ctx context.Context, projectID anon.ID, answer *model.Answer) error {
	p, err := s.DB.Project(s.Vault, projectID)
	if err != nil {
		return err
	}
	plain, _ := json.Marshal(normalize(answer))
	payload, err := s.Vault.Seal(plain, p.ID.Bytes())
	if err != nil {
		return err
	}
	return s.DB.InsertTrialAnswer(projectID, payload)
}

// SubmitPaper 是纸质密封补录（FR-ANS-090）。
//
// 不消耗令牌，直接写 answer_record，并对 paper_entry_count 加一。
// 记录本身与在线提交**完全同构、不可区分**——若打上 is_paper_entry 标记，
// 补录者就构成一个可识别的小群体，而不在场者名单是管理员知道的，
// 这等于给这几份作答贴上身份标签（LLD 1.2 修正二）。
//
// 双人在场由工作程序与纸质签字保障，系统不设第二口令、不做技术验证；
// 此处只负责把复核人姓名与份数写入操作日志留痕（LLD 5.3）。
func (s *Service) SubmitPaper(ctx context.Context, projectID anon.ID,
	answer *model.Answer, reviewer string) error {

	if reviewer == "" {
		return errors.New("请填写复核人姓名：补录须双人当场拆封、双人核对")
	}
	p, err := s.DB.Project(s.Vault, projectID)
	if err != nil {
		return err
	}
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return err
	}
	if miss := Validate(f, answer); len(miss) > 0 {
		return &incompleteError{Missing: miss}
	}
	plain, _ := json.Marshal(normalize(answer))
	payload, err := s.Vault.Seal(plain, p.ID.Bytes())
	if err != nil {
		return err
	}
	err = s.DB.Tx(ctx, func(tx *sql.Tx) error {
		rec := &model.AnswerRecord{
			ID: anon.NewID(), ProjectID: p.ID, BatchNo: 1, Payload: payload,
		}
		if err := store.InsertAnswerTx(tx, rec); err != nil {
			return err
		}
		return s.DB.IncPaperEntry(tx, p.ID)
	})
	if err != nil {
		return err
	}
	// 日志记份数与复核人，不含任何作答内容。
	// 这不构成匿名性泄露：补录记录与在线提交同构不可分，审计者可知
	// "有 N 份是补录的"，但无从知道是哪 N 份。
	return s.DB.Log(&p.ID, "paper_entry", "ok",
		fmt.Sprintf("纸质补录 1 份，累计 %d 份，复核人：%s", p.PaperEntryCount+1, reviewer))
}

func (s *Service) checkWindow(p *model.Project) error {
	now := time.Now()
	switch {
	case p.Status == model.StatusDraft:
		return ErrTokenInvalid
	case p.Status == model.StatusClosed || p.Status == model.StatusArchived:
		return ErrClosed
	case now.Before(p.StartAt):
		return ErrNotStarted
	case !now.Before(p.EndAt):
		return ErrClosed
	}
	return nil
}

// normalize 在序列化前按 (q,s) 排序。
//
// 关键：数组顺序若沿用作答先后，就会泄露答题顺序——"某人先看了谁、
// 跳过了哪几题"本身即身份线索（LLD 3.1）。
func normalize(a *model.Answer) *model.Answer {
	out := &model.Answer{V: 1, Items: append([]model.AnswerItem(nil), a.Items...)}
	sortItems(out.Items)
	return out
}

func sortItems(items []model.AnswerItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			if items[j].Q < items[j-1].Q ||
				(items[j].Q == items[j-1].Q && items[j].S < items[j-1].S) {
				items[j], items[j-1] = items[j-1], items[j]
			} else {
				break
			}
		}
	}
}

// Validate 执行必填校验，返回未完成项的 "qid:sid" 列表，供前端滚动定位。
func Validate(f *model.Form, a *model.Answer) []string {
	got := map[string]bool{}
	for _, it := range a.Items {
		filled := it.Grade != nil || it.Score != nil || it.Opt != "" ||
			len(it.Opts) > 0 || it.Text != ""
		if filled {
			got[it.Q+":"+it.S] = true
		}
	}
	var miss []string
	for _, g := range f.Groups {
		for _, q := range g.Questions {
			if !q.Required {
				continue
			}
			for _, sid := range q.SubjectIDs {
				k := q.ID.Hex() + ":" + sid.Hex()
				if !got[k] {
					miss = append(miss, k)
				}
			}
		}
	}
	return miss
}

// ── 统计 ────────────────────────────────────────────────────────────

func (s *Service) Stats(projectID anon.ID) (*stats.Result, error) {
	p, err := s.DB.Project(s.Vault, projectID)
	if err != nil {
		return nil, err
	}
	if r, ok := s.statsCache[projectID]; ok && p.Status == model.StatusClosed {
		return r, nil
	}
	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		return nil, err
	}
	recs, err := s.DB.LoadAnswers(projectID)
	if err != nil {
		return nil, err
	}
	r, err := stats.Aggregate(p, f, recs, s.Vault)
	if err != nil {
		return nil, err
	}
	if p.Status == model.StatusClosed {
		s.statsCache[projectID] = r
	}
	return r, nil
}

func (s *Service) invalidateStats(projectID anon.ID) { delete(s.statsCache, projectID) }

// ── 试填 ────────────────────────────────────────────────────────────

// TrialProject 判断某个令牌值是否为试填令牌，并返回其所属项目。
// 试填令牌可反复使用不失效（FR-PRJ-030）。
func (s *Service) TrialProject(tokenValue string) (anon.ID, bool) {
	ps, err := s.DB.Projects(s.Vault)
	if err != nil {
		return anon.ID{}, false
	}
	for _, p := range ps {
		ok, err := s.DB.IsTrialToken(p.ID, tokenValue)
		if err == nil && ok {
			return p.ID, true
		}
	}
	return anon.ID{}, false
}

// MissingOf 从 ErrIncomplete 里取出未完成项列表，供前端滚动定位。
func MissingOf(err error) []string {
	var m *incompleteError
	if errors.As(err, &m) {
		return m.Missing
	}
	return nil
}

type incompleteError struct{ Missing []string }

func (e *incompleteError) Error() string {
	return fmt.Sprintf("%s: 有 %d 项必填未完成", ErrIncomplete.Error(), len(e.Missing))
}
func (e *incompleteError) Unwrap() error { return ErrIncomplete }
