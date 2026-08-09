// Package admin 是管理端：服务端渲染的 HTML + 表单。
//
// 与作答端形态不同是刻意的（HLD 3.2）：管理端跑在本机回环上，零丢包、
// 零延迟，用户只有 1 人且熟悉系统，失败可重试；作答端在无线环境下面对
// 最多 200 名首次使用者，失败即不可补测。两种失败代价决定了两种形态。
//
// 此处用 html/template 服务端渲染，未引入 templ / htmx / Tailwind——
// 见 README「与文档的偏差」一节。
package admin

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
	"odes/internal/service"
	"odes/internal/stats"
)

//go:embed templates/*.html
var files embed.FS

var funcs = template.FuncMap{
	"pct":   func(f float64) string { return fmt.Sprintf("%.1f%%", f) },
	"f1":    func(f float64) string { return fmt.Sprintf("%.1f", f) },
	"date":  func(t time.Time) string { return t.Format("2006-01-02 15:04") },
	"hex":   func(id anon.ID) string { return id.Hex() },
	"inc":   func(i int) int { return i + 1 },
	"lower": strings.ToLower,
}

var tmpl = template.Must(template.New("").Funcs(funcs).ParseFS(files, "templates/*.html"))

type Handler struct {
	Svc *service.Service
	// CertOK / CertMsg 来自启动自检，供发布前置校验 PC-09 与首页告警使用。
	CertOK      bool
	CertMsg     string
	SelfSigned  bool
	Preflight   []string // 启动自检的告警行
	SessionMgr  *Sessions
	ShortEntry  string
	LeaseCount  func() int
	DomainWarns []string
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin/login", h.loginPage)
	mux.HandleFunc("POST /admin/login", h.doLogin)
	mux.HandleFunc("POST /admin/logout", h.logout)

	auth := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !h.SessionMgr.Valid(r) {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			h.SessionMgr.Touch(w, r)
			f(w, r)
		}
	}

	mux.HandleFunc("GET /admin/", auth(h.home))
	mux.HandleFunc("GET /admin/projects/{id}", auth(h.project))
	mux.HandleFunc("POST /admin/projects/{id}/tokens", auth(h.genTokens))
	mux.HandleFunc("GET /admin/projects/{id}/print", auth(h.printSheets))
	mux.HandleFunc("POST /admin/projects/{id}/status", auth(h.setStatus))
	mux.HandleFunc("GET /admin/projects/{id}/stats", auth(h.stats))
	mux.HandleFunc("GET /admin/projects/{id}/paper", auth(h.paperForm))
	mux.HandleFunc("POST /admin/projects/{id}/paper", auth(h.paperSubmit))
	mux.HandleFunc("GET /admin/projects/{id}/export", auth(h.export))
	mux.HandleFunc("GET /admin/projects/{id}/trial", auth(h.trial))
	mux.HandleFunc("POST /admin/projects/{id}/archive", auth(h.archive))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
	})
	return mux
}

func (h *Handler) render(w http.ResponseWriter, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["CertOK"] = h.CertOK
	data["CertMsg"] = h.CertMsg
	data["SelfSigned"] = h.SelfSigned
	data["PreflightWarn"] = h.Preflight
	data["DomainWarns"] = h.DomainWarns
	if h.LeaseCount != nil {
		data["Leases"] = h.LeaseCount()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "渲染失败: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	h.render(w, "error.html", map[string]any{"Err": err.Error()})
}

// ── 首页 ────────────────────────────────────────────────────────────

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	ps, err := h.Svc.DB.Projects(h.Svc.Vault)
	if err != nil {
		h.fail(w, err)
		return
	}
	type row struct {
		P                 *model.Project
		Submitted, Tokens int
	}
	var rows []row
	for _, p := range ps {
		n, _ := h.Svc.DB.CountAnswers(p.ID)
		t, _, _ := h.Svc.DB.CountTokens(p.ID)
		rows = append(rows, row{P: p, Submitted: n, Tokens: t})
	}
	archives, _ := h.Svc.DB.ArchiveMetas()
	h.render(w, "home.html", map[string]any{"Rows": rows, "Archives": archives})
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

	h.render(w, "project.html", map[string]any{
		"P": p, "F": f, "Items": f.ItemCount(),
		"Tokens": total, "Used": used, "Submitted": submitted,
		"Trials": trials, "Logs": logs,
		"Checks": h.Svc.Preflight(p, h.CertOK, h.CertMsg),
		"CanPub": p.Status == model.StatusDraft,
	})
}

func (h *Handler) load(r *http.Request) (*model.Project, *model.Form, error) {
	id, err := anon.MustIDFromHex(r.PathValue("id"))
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
	type sheet struct {
		SSID, Code, URL string
	}
	var sheets []sheet
	for _, t := range toks {
		sheets = append(sheets, sheet{
			SSID: fmt.Sprintf("KCZ-EVAL-%d", t.APIndex),
			Code: t.ShortCode[:4] + "-" + t.ShortCode[4:],
			URL:  fmt.Sprintf("https://%s:8443/e/%s", h.Svc.Domain, t.Value),
		})
	}
	h.render(w, "print.html", map[string]any{
		"P": p, "Sheets": sheets, "Entry": h.ShortEntry,
	})
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
		if cs := h.Svc.Preflight(p, h.CertOK, h.CertMsg); !service.AllOK(cs) {
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
		h.render(w, "stats_locked.html", map[string]any{
			"P": p, "Submitted": submitted,
		})
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
	h.render(w, "stats.html", map[string]any{
		"P": p, "R": res, "Tags": tags, "Texts": res.AllTexts(),
		"GradeCells": gradeCells(res),
	})
}

func gradeCells(r *stats.Result) []stats.Cell {
	var out []stats.Cell
	for _, c := range r.Cells {
		if c.Kind == model.KindGrade {
			out = append(out, c)
		}
	}
	return out
}
