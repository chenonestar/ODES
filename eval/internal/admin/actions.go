package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"odes/internal/anon"
	"odes/internal/export"
	"odes/internal/model"
)

// ── 纸质密封补录（FR-ANS-090 / LLD 5.3）─────────────────────────────
//
// 界面强制勾选「双人当场拆封、内容已双人核对」并填写复核人姓名。
// 系统不设第二口令、不做技术验证——双人在场由工作程序与纸质签字保障，
// 这一点在页面上如实写明，不做超出能力的承诺。

func (h *Handler) paperForm(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, "paper.html", map[string]any{"P": p, "F": f})
}

func (h *Handler) paperSubmit(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	if r.FormValue("confirm") != "on" {
		h.fail(w, fmt.Errorf("请先勾选确认：密封件须由双人当场拆封、内容已双人核对"))
		return
	}
	reviewer := strings.TrimSpace(r.FormValue("reviewer"))
	if reviewer == "" {
		h.fail(w, fmt.Errorf("请填写复核人姓名"))
		return
	}

	var a model.Answer
	a.V = 1
	for _, g := range f.Groups {
		for _, q := range g.Questions {
			for _, sid := range q.SubjectIDs {
				key := q.ID.Hex() + ":" + sid.Hex()
				val := r.FormValue(key)
				if val == "" {
					continue
				}
				it := model.AnswerItem{Q: q.ID.Hex(), S: sid.Hex()}
				switch q.Kind {
				case model.KindGrade:
					n, err := strconv.Atoi(val)
					if err != nil {
						continue
					}
					it.Grade = &n
				case model.KindScore:
					n, err := strconv.Atoi(val)
					if err != nil {
						continue
					}
					it.Score = &n
				case model.KindText:
					it.Text = val
				default:
					it.Opt = val
				}
				a.Items = append(a.Items, it)
			}
		}
	}
	if err := h.Svc.SubmitPaper(r.Context(), p.ID, &a, reviewer); err != nil {
		h.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+p.ID.Hex(), http.StatusSeeOther)
}

// ── 试填（FR-PRJ-030~033）───────────────────────────────────────────

func (h *Handler) trial(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	toks, _ := h.Svc.DB.TrialTokens(p.ID)
	n, _ := h.Svc.DB.CountTrialAnswers(p.ID)
	h.render(w, "trial.html", map[string]any{
		"P": p, "Toks": toks, "Count": n, "Domain": h.Svc.Domain,
	})
}

// ── 导出（FR-EXP-010~013）───────────────────────────────────────────

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	res, err := h.Svc.Stats(p.ID)
	if err != nil {
		h.fail(w, err)
		return
	}

	switch r.URL.Query().Get("kind") {
	case "csv":
		// 逐项统计表，供二次制表（FR-EXP-011）
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="stats.csv"`)
		_ = export.StatsCSV(w, res)

	case "raw":
		// 原始匿名数据（FR-EXP-012）。导出前统一重排，编号现场生成。
		recs, err := h.Svc.DB.LoadAnswers(p.ID)
		if err != nil {
			h.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="raw-anonymous.csv"`)
		if err := export.RawCSV(w, p, f, recs, h.Svc.Vault); err != nil {
			h.fail(w, err)
			return
		}
		_ = h.Svc.DB.Log(&p.ID, "export_raw", "ok",
			fmt.Sprintf("导出原始匿名数据 %d 份（顺序已重排，编号现场生成）", len(recs)))

	case "archive":
		// 加密归档包（FR-EXP-013 / FR-SYS-030）
		pass := r.URL.Query().Get("pass")
		if len(pass) < 8 {
			h.fail(w, fmt.Errorf("归档口令至少 8 位，且须与登录口令不同"))
			return
		}
		blob, sum, err := h.Svc.BuildArchive(p.ID, pass)
		if err != nil {
			h.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="archive-%s.odes"`, p.ID.Hex()[:8]))
		w.Header().Set("X-Archive-SHA256", sum)
		_, _ = w.Write(blob)

	default:
		// 正式统计报告（HTML）。PDF 版见 README「尚未实现」一节。
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = export.ReportHTML(w, p, res)
	}
}

// ── 归档与安全擦除（FR-SYS-031/032）─────────────────────────────────

func (h *Handler) archive(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	if r.FormValue("saved") != "on" {
		h.fail(w, fmt.Errorf("请先勾选确认：加密归档包已保存至指定介质"))
		return
	}
	sum := strings.TrimSpace(r.FormValue("sha8"))
	pass := r.FormValue("pass")
	if err := h.Svc.ArchiveAndWipe(r.Context(), p.ID, pass, sum); err != nil {
		h.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

var _ = anon.NewID
