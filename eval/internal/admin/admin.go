// Package admin 是管理端：服务端渲染的 HTML + 表单。
//
// 与作答端形态不同是刻意的（HLD 3.2）：管理端跑在本机回环上，零丢包、
// 零延迟，用户只有 1 人且熟悉系统，失败可重试；作答端在无线环境下面对
// 最多 200 名首次使用者，失败即不可补测。两种失败代价决定了两种形态。
//
// 交互用 htmx（HLD 技术选型表）：管理端在本机回环上，网络绝对可靠，
// htmx 的服务端驱动模型完全适用。作答端则相反，必须自包含（ADR-001）。
package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"odes/internal/admin/views"
	"odes/internal/anon"
	"odes/internal/model"
	"odes/internal/qr"
	"odes/internal/service"
	"odes/web"
)

type Handler struct {
	Svc *service.Service
	// CertUsable / CertMsg 是 PC-09 的判据（证书剩余 ≥1 天）；
	// CertAlert 是启动自检里未通过的项，只用于首页告警（含 CK-04 的 21 天提醒）。
	// 两者判据不同，不能混用——见 config.Status 的说明。
	CertUsable  bool
	CertMsg     string
	CertAlert   string
	SelfSigned  bool
	Preflight   []string // 启动自检的告警行
	SessionMgr  *Sessions
	ShortEntry  string
	LeaseCount  func() int
	DomainWarns []string
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	// 静态资源：样式与 htmx。都是 embed 进二进制的本地文件，
	// 不存在任何外部请求（CON-02 全程离线）。
	r.Get("/static/admin.css", serveAsset("text/css; charset=utf-8", web.AdminCSS))
	r.Get("/static/htmx.min.js", serveAsset("text/javascript; charset=utf-8", web.HTMX))
	r.Get("/static/admin.js", serveAsset("text/javascript; charset=utf-8", web.AdminJS))
	r.Get("/favicon.svg", serveAsset("image/svg+xml", web.Favicon))
	r.Get("/favicon.ico", serveAsset("image/svg+xml", web.Favicon))

	r.Get("/admin/login", h.loginPage)
	r.Post("/admin/login", h.doLogin)
	r.Post("/admin/logout", h.logout)

	// 需要登录的部分挂在一个子路由组上：认证是中间件语义，
	// 逐条路由包一层 auth() 只要漏一次就是个洞。
	r.Route("/admin/projects", func(r chi.Router) {
		r.Use(h.requireLogin)
		// 静态段要排在 /{id} 前面：chi 虽然优先匹配静态段，
		// 但把它写在前面能让读路由表的人一眼看出 new 不是一个项目 ID。
		r.Get("/new", h.newProjectPage)
		r.Post("/new", h.createProject)
		r.Get("/{id}", h.project)
		r.Get("/{id}/design", h.design)
		r.Get("/{id}/edit", h.editProjectPage)
		r.Post("/{id}/edit", h.updateProject)
		r.Post("/{id}/copy", h.copyProject)
		r.Post("/{id}/delete", h.deleteProject)
		r.Post("/{id}/subjects", h.addSubject)
		r.Post("/{id}/subjects/reorder", h.reorderSubjects)
		r.Post("/{id}/groups", h.addGroup)
		r.Post("/{id}/groups/reorder", h.reorderGroups)
		r.Get("/{id}/questions/new", h.newQuestionPage)
		r.Post("/{id}/questions", h.createQuestion)
		r.Get("/{id}/roster", h.rosterPage)
		r.Post("/{id}/roster/upload", h.uploadRoster)
		r.Post("/{id}/roster/confirm", h.confirmRoster)
		r.Post("/{id}/roster/clear", h.clearRoster)
		r.Post("/{id}/tokens", h.genTokens)
		r.Get("/{id}/print", h.printSheets)
		r.Post("/{id}/status", h.setStatus)
		r.Get("/{id}/stats", h.stats)
		r.Get("/{id}/progress", h.progress)
		r.Get("/{id}/paper", h.paperForm)
		r.Post("/{id}/paper", h.paperSubmit)
		r.Get("/{id}/export", h.export)
		r.Get("/{id}/trial", h.trial)
		r.Post("/{id}/archive", h.archive)
	})

	// 子对象用自己的 ID 定位，不必再带项目 ID：项目归属由服务层
	// 从子对象回查（service/design.go），URL 里再带一个项目 ID
	// 只会多出一条"两个 ID 对不上"的分支，而它并不增加安全性。
	r.Route("/admin/subjects", func(r chi.Router) {
		r.Use(h.requireLogin)
		r.Post("/{id}/update", h.updateSubject)
		r.Post("/{id}/delete", h.deleteSubject)
	})
	r.Route("/admin/groups", func(r chi.Router) {
		r.Use(h.requireLogin)
		r.Post("/{id}/update", h.updateGroup)
		r.Post("/{id}/delete", h.deleteGroup)
		r.Post("/{id}/reorder", h.reorderQuestions)
	})
	r.Route("/admin/questions", func(r chi.Router) {
		r.Use(h.requireLogin)
		r.Get("/{id}/edit", h.editQuestionPage)
		r.Post("/{id}/update", h.updateQuestion)
		r.Post("/{id}/delete", h.deleteQuestion)
	})

	r.Group(func(r chi.Router) {
		r.Use(h.requireLogin)
		r.Get("/admin/", h.home)
	})

	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/admin/", http.StatusSeeOther)
	})
	return r
}

// serveAsset 下发内嵌的静态资源。资源随二进制走、内容不变，
// 因此可以放心长缓存。
func serveAsset(ctype, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		_, _ = io.WriteString(w, body)
	}
}

// requireLogin 是管理端的认证中间件。
//
// 注意它不负责网络隔离——管理端根本不在测评网络上监听（httpd.Server
// 用两个独立监听器），这里挡的是本机上的未登录访问与会话超时。
func (h *Handler) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.SessionMgr.Valid(r) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		h.SessionMgr.Touch(w, r)
		next.ServeHTTP(w, r)
	})
}

// chrome 装配每页共用的外壳数据（启动自检告警、接入终端数）。
func (h *Handler) chrome() views.Chrome {
	c := views.Chrome{
		SelfSigned: h.SelfSigned, CertAlert: h.CertAlert,
		Warns: h.Preflight, DomainWarns: h.DomainWarns,
	}
	if h.LeaseCount != nil {
		c.Leases = h.LeaseCount()
	}
	return c
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	if err := c.Render(ctx, w); err != nil {
		http.Error(w, "渲染失败: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	h.render(w, nil, views.Error(h.chrome(), err.Error()))
}

// ── 首页 ────────────────────────────────────────────────────────────

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	ps, err := h.Svc.DB.Projects(h.Svc.Vault)
	if err != nil {
		h.fail(w, err)
		return
	}
	var rows []views.HomeRow
	for _, p := range ps {
		n, _ := h.Svc.DB.CountAnswers(p.ID)
		t, _, _ := h.Svc.DB.CountTokens(p.ID)
		rows = append(rows, views.HomeRow{P: p, Submitted: n, Tokens: t})
	}
	archives, _ := h.Svc.DB.ArchiveMetas()
	h.render(w, r, views.Home(h.chrome(), rows, archives))
}

// progress 是 htmx 轮询的提交进度片段（FR-STA-010）。
//
// 这正是 htmx 服务端驱动模型合适的地方：管理端在本机回环上，往返几乎零
// 成本，返回一个数字片段比让前端自己拼 JSON 再渲染简单得多。作答端则
// 相反——那边网络不可靠，必须自包含（ADR-001）。
func (h *Handler) progress(w http.ResponseWriter, r *http.Request) {
	id, err := anon.MustIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	n, err := h.Svc.DB.CountAnswers(id)
	if err != nil {
		http.Error(w, "读取失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "%d", n)
}

// ── 项目详情 ────────────────────────────────────────────────────────

func (h *Handler) project(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	total, used, _ := h.Svc.DB.CountTokens(p.ID)
	submitted, _ := h.Svc.DB.CountAnswers(p.ID)
	trials, _ := h.Svc.DB.TrialTokens(p.ID)
	logs, _ := h.Svc.DB.OpLogs(p.ID, 20)

	h.render(w, r, views.Project(h.chrome(), views.ProjectView{
		P: p, F: f, Items: f.ItemCount(),
		Tokens: total, Used: used, Submitted: submitted,
		Trials: trials, Logs: logs,
		Checks: h.Svc.Preflight(p, h.CertUsable, h.CertMsg),
	}))
}

func (h *Handler) load(r *http.Request) (*model.Project, *model.Form, error) {
	id, err := anon.MustIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		return nil, nil, err
	}
	p, err := h.Svc.DB.Project(h.Svc.Vault, id)
	if err != nil {
		return nil, nil, err
	}
	f, err := h.Svc.DB.Form(h.Svc.Vault, p)
	return p, f, err
}

// ── 令牌 ────────────────────────────────────────────────────────────

func (h *Handler) genTokens(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	n, _ := strconv.Atoi(r.FormValue("count"))
	if n == 0 {
		n = p.ExpectedCount
	}
	batches, _ := strconv.Atoi(r.FormValue("batches"))
	if batches < 1 {
		batches = 1
	}
	if err := h.Svc.GenerateTokens(r.Context(), p.ID, n, batches, p.APCount); err != nil {
		h.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+p.ID.Hex(), http.StatusSeeOther)
}

func (h *Handler) printSheets(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	toks, err := h.Svc.PrintSheets(p.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	var sheets []views.PrintSheet
	for _, t := range toks {
		url := fmt.Sprintf("https://%s:8443/e/%s", h.Svc.Domain, t.Value)
		// 二维码生成失败不能静默：那张单子发下去参评人员扫不出来，
		// 而现场没人会去核对每一张。宁可整页报错也不出半成品。
		code, err := qr.DataURI(url)
		if err != nil {
			h.fail(w, fmt.Errorf("生成二维码失败，令牌单未输出：%w", err))
			return
		}
		sheets = append(sheets, views.PrintSheet{
			SSID: fmt.Sprintf("KCZ-EVAL-%d", t.APIndex),
			Code: t.ShortCode[:4] + "-" + t.ShortCode[4:],
			URL:  url,
			QR:   code,
		})
	}
	h.render(w, r, views.Print(h.chrome(), views.ProjectView{P: p}, sheets, h.ShortEntry))
}

// ── 状态迁移 ────────────────────────────────────────────────────────

func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	to := model.Status(r.FormValue("to"))

	if to == model.StatusPublished {
		if cs := h.Svc.Preflight(p, h.CertUsable, h.CertMsg); !service.AllOK(cs) {
			var bad []string
			for _, c := range cs {
				if !c.OK {
					bad = append(bad, c.Code+" "+c.Msg)
				}
			}
			h.fail(w, fmt.Errorf("发布前置校验未通过，共 %d 项：\n%s",
				len(bad), strings.Join(bad, "\n")))
			return
		}
	}
	if err := h.Svc.Transition(r.Context(), p.ID, to); err != nil {
		h.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+p.ID.Hex(), http.StatusSeeOther)
}

// ── 统计 ────────────────────────────────────────────────────────────

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	// 结果查看时机受结果开放时间控制：进行中仅可查看提交进度，
	// 不可查看内容统计（FR-STA-020）。
	if time.Now().Before(p.ResultOpenAt) && p.Status != model.StatusClosed {
		submitted, _ := h.Svc.DB.CountAnswers(p.ID)
		h.render(w, r, views.StatsLocked(h.chrome(), views.StatsView{P: p, Submitted: submitted}))
		return
	}
	res, err := h.Svc.Stats(p.ID)
	if err != nil {
		h.fail(w, err)
		return
	}
	if tag := r.URL.Query().Get("tag"); tag != "" {
		filtered, ferr := res.FilterByTag(f, tag)
		if ferr != nil {
			h.fail(w, ferr)
			return
		}
		res = filtered
	}
	var tags []string
	seen := map[string]bool{}
	for _, s := range f.Subjects {
		if s.Tag != "" && !seen[s.Tag] {
			seen[s.Tag] = true
			tags = append(tags, s.Tag)
		}
	}
	h.render(w, r, views.Stats(h.chrome(), views.StatsView{
		P: p, R: res, Submitted: res.Submitted, Tags: tags,
		Texts: res.AllTexts(), GradeCells: views.GradeCells(res),
		ScoreCells: views.ScoreCells(res), OptCells: views.OptionCells(res),
		Matrix: views.BuildMatrix(res),
	}))
}
