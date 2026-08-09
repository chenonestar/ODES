package httpd

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"odes/internal/evalui"
	"odes/internal/model"
	"odes/internal/service"
	"odes/internal/store"
)

// EvalRoutes 装配作答端路由（LLD 8.1）。
func EvalRoutes(s *service.Service, shortEntry string) http.Handler {
	mux := http.NewServeMux()
	rl := newRateLimiter()

	// GET /e/{token} —— 扫码直达作答页
	mux.HandleFunc("GET /e/{token}", func(w http.ResponseWriter, r *http.Request) {
		serveEvalPage(w, r, s, r.PathValue("token"))
	})

	// POST /e/{token}/submit
	mux.HandleFunc("POST /e/{token}/submit", func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r), rateMaxSubmit) {
			writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "请稍后重试", nil)
			return
		}
		submit(w, r, s, r.PathValue("token"))
	})

	// GET/POST /j —— 短码入口（FR-ANS-010）
	mux.HandleFunc("GET /j", func(w http.ResponseWriter, r *http.Request) {
		shortCodePage(w, "", shortEntry)
	})
	mux.HandleFunc("POST /j", func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r), rateMaxEntry) {
			shortCodePage(w, "请求过于频繁，请稍后重试", shortEntry)
			return
		}
		code := strings.ToUpper(strings.TrimSpace(r.FormValue("code")))
		code = strings.ReplaceAll(code, "-", "")
		code = strings.ReplaceAll(code, " ", "")
		// 校验位先在本地判，输错即时提示而非提交后报错（FR-TKN-023）
		if !anonVerify(code) {
			shortCodePage(w, "编号有误，请核对令牌单上的 8 位编号", shortEntry)
			return
		}
		tk, err := s.DB.AnyTokenByShortCode(code)
		if err != nil {
			shortCodePage(w, "编号无效", shortEntry)
			return
		}
		http.Redirect(w, r, "/e/"+tk.Value, http.StatusSeeOther)
	})

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/j", http.StatusSeeOther)
	})
	return mux
}

func serveEvalPage(w http.ResponseWriter, r *http.Request, s *service.Service, tokenValue string) {
	tk, err := s.DB.AnyTokenByValue(tokenValue)
	if err != nil {
		// 不是正式令牌，再看是不是试填令牌：试填令牌可反复使用不失效，
		// 且不受项目状态与时间窗限制——它的用途正是在发布**之前**
		// 走通一遍流程（FR-PRJ-030、现场部署规程第 4 步）。
		if pid, ok := s.TrialProject(tokenValue); ok {
			p, err := s.DB.Project(s.Vault, pid)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			f, err := s.DB.Form(s.Vault, p)
			if err != nil {
				http.Error(w, "读取测评表失败", http.StatusInternalServerError)
				return
			}
			if err := evalui.Render(w, p, f, tokenValue, true,
				"/e/"+tokenValue+"/submit"); err != nil {
				http.Error(w, "渲染失败", http.StatusInternalServerError)
			}
			return
		}
		// 无效令牌一律 404，不区分"不存在"与其它情形，避免被枚举探测
		// （LLD 8.1）。
		http.NotFound(w, r)
		return
	}
	p, err := s.DB.Project(s.Vault, tk.ProjectID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case tk.Used:
		evalui.DonePage(w, "您已完成测评，感谢参与")
		return
	case p.Status == model.StatusClosed || p.Status == model.StatusArchived:
		evalui.DonePage(w, "测评已结束")
		return
	case p.Status == model.StatusDraft:
		http.NotFound(w, r)
		return
	case time.Now().Before(p.StartAt):
		evalui.DonePage(w, "测评尚未开始")
		return
	case !time.Now().Before(p.EndAt):
		evalui.DonePage(w, "测评已结束")
		return
	}

	f, err := s.DB.Form(s.Vault, p)
	if err != nil {
		http.Error(w, "读取测评表失败", http.StatusInternalServerError)
		return
	}
	if err := evalui.Render(w, p, f, tokenValue, false, "/e/"+tokenValue+"/submit"); err != nil {
		http.Error(w, "渲染失败", http.StatusInternalServerError)
	}
}

func submit(w http.ResponseWriter, r *http.Request, s *service.Service, tokenValue string) {
	var a model.Answer
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&a); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "提交内容格式有误", nil)
		return
	}

	// 试填令牌走独立数据区，不消耗令牌、不进统计（FR-PRJ-031）
	if tk, err := s.DB.AnyTokenByValue(tokenValue); err != nil {
		if pid, trial := s.TrialProject(tokenValue); trial {
			if err := s.SubmitTrial(r.Context(), pid, &a); err != nil {
				writeErr(w, http.StatusInternalServerError, "ERROR", "试填提交失败", nil)
				return
			}
			writeOK(w)
			return
		}
		_ = tk
		writeErr(w, http.StatusNotFound, "TOKEN_INVALID", "令牌无效", nil)
		return
	}

	err := s.Submit(r.Context(), tokenValue, &a)
	switch {
	case err == nil:
		writeOK(w)
	case isErr(err, service.ErrTokenUsed):
		writeErr(w, http.StatusConflict, "TOKEN_USED", "您已完成测评，感谢参与", nil)
	case isErr(err, service.ErrClosed):
		writeErr(w, http.StatusGone, "CLOSED", "测评已结束", nil)
	case isErr(err, service.ErrNotStarted):
		writeErr(w, 425, "NOT_STARTED", "测评尚未开始", nil)
	case isErr(err, service.ErrIncomplete):
		writeErr(w, http.StatusUnprocessableEntity, "INCOMPLETE",
			"有必填项未完成，已为您定位到第一处", service.MissingOf(err))
	case isErr(err, service.ErrTokenInvalid):
		writeErr(w, http.StatusNotFound, "TOKEN_INVALID", "令牌无效", nil)
	default:
		writeErr(w, http.StatusInternalServerError, "ERROR", "提交失败，请重试", nil)
	}
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func writeErr(w http.ResponseWriter, status int, code, msg string, missing []string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": false, "code": code, "msg": msg, "missing": missing,
	})
}

// ── 限流（FR-ANS-031 / NFR-ANO-040）─────────────────────────────────
//
// IP 频率限制**仅在内存中实施**。IP 不得写入数据库、不得写入日志、
// 不得与令牌同时出现在任何持久化记录中；进程退出即消失。
// 这个结构体因此没有任何落盘路径，也不接受注入 logger。

type rateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{hits: map[string][]time.Time{}}
	go func() {
		for range time.Tick(time.Minute) {
			rl.mu.Lock()
			for k, ts := range rl.hits {
				if len(ts) == 0 || time.Since(ts[len(ts)-1]) > 5*time.Minute {
					delete(rl.hits, k)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

const rateWindow = time.Minute

// 两个阈值，因为两个端点面对的威胁不同：
//
//   - 短码入口 /j 是唯一可枚举的面（8 位短码，其中 7 位随机）。
//     限得紧一些。
//   - 提交端点几乎无枚举价值：令牌值有 ~158 位熵，穷举不可行。
//     这里限流只是防误触发的重试风暴，限得太紧反而会误伤合法场景——
//     测评网络里存在"无手机者借用他人手机"的正常情况，同一 IP 连续
//     提交数次是预期行为。
const (
	rateMaxSubmit = 60
	rateMaxEntry  = 20
)

func (rl *rateLimiter) allow(ip string, max int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	ts := rl.hits[ip]
	keep := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < rateWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) >= max {
		rl.hits[ip] = keep
		return false
	}
	rl.hits[ip] = append(keep, now)
	return true
}

// clientIP 只用于内存限流的键。返回值不得进入任何持久化路径。
func clientIP(r *http.Request) string {
	// 刻意不读 X-Forwarded-For：测评网络里没有反向代理，
	// 读它只会让限流被伪造头绕过。
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

var _ = store.ErrNotFound
