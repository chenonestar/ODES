package admin

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"odes/internal/admin/views"
	"odes/internal/crypto"
)

// Sessions 是管理端会话。全部状态只在内存中。
//
// 单管理员模式（CON-03），因此这里没有用户表、没有角色——
// 引入第二个账号即触碰 1.4 的非目标「多管理员与权限分级」。
type Sessions struct {
	mu       sync.Mutex
	tokens   map[string]time.Time
	Timeout  time.Duration // 默认 30 分钟，现场监控场景可调至 4 小时
	fails    int
	lockedTo time.Time
}

func NewSessions(timeout time.Duration) *Sessions {
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &Sessions{tokens: map[string]time.Time{}, Timeout: timeout}
}

const cookieName = "odes_sid"

// Login 校验口令。连续 5 次失败锁定 15 分钟（FR-SYS-010）。
func (s *Sessions) Login(w http.ResponseWriter, password string, m crypto.Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.lockedTo) {
		return errLocked{until: s.lockedTo}
	}
	if !crypto.VerifyPassword(password, m) {
		s.fails++
		if s.fails >= 5 {
			s.lockedTo = time.Now().Add(15 * time.Minute)
			s.fails = 0
		}
		return errBadPassword{}
	}
	s.fails = 0

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.tokens[tok] = time.Now().Add(s.Timeout)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (s *Sessions) Valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[c.Value]
	if !ok || time.Now().After(exp) {
		delete(s.tokens, c.Value)
		return false
	}
	return true
}

func (s *Sessions) Touch(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[c.Value]; ok {
		s.tokens[c.Value] = time.Now().Add(s.Timeout)
	}
}

func (s *Sessions) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.tokens, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
}

type errBadPassword struct{}

func (errBadPassword) Error() string { return "口令不正确" }

type errLocked struct{ until time.Time }

func (e errLocked) Error() string {
	return "连续失败次数过多，请于 " + e.until.Format("15:04") + " 后重试"
}

// ── 登录处理器 ──────────────────────────────────────────────────────

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, views.Login(""))
}

func (h *Handler) doLogin(w http.ResponseWriter, r *http.Request) {
	meta, err := h.loadMeta()
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := h.SessionMgr.Login(w, r.FormValue("password"), meta); err != nil {
		h.render(w, r, views.Login(err.Error()))
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	h.SessionMgr.Logout(w, r)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) loadMeta() (crypto.Meta, error) {
	var m crypto.Meta
	var err error
	if m.KEKSalt, err = h.Svc.DB.GetMeta("kek_salt"); err != nil {
		return m, err
	}
	if m.WrappedDEK, err = h.Svc.DB.GetMeta("wrapped_dek"); err != nil {
		return m, err
	}
	m.PwdHash, err = h.Svc.DB.GetMeta("admin_pwd_hash")
	return m, err
}
