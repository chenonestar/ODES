package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"odes/internal/admin/views"
	"odes/internal/anon"
	"odes/internal/model"
	"odes/internal/service"
)

// ── 项目 CRUD 与测评表设计器的处理器 ────────────────────────────────
//
// 处理器只做三件事：解析表单、调服务、渲染。
// **不在这里判断"能不能改"**——草稿态闸门与归属校验全部在 service/design.go，
// 处理器绕过它就等于没有闸门。

// designView 装配设计器共用的视图数据。
func (h *Handler) designView(p *model.Project, f *model.Form) views.DesignView {
	qn := 0
	for _, g := range f.Groups {
		qn += len(g.Questions)
	}
	rn, _ := h.Svc.DB.CountRoster(p.ID)
	return views.DesignView{
		P: p, F: f,
		Editable:      p.Status == model.StatusDraft,
		Items:         f.ItemCount(),
		QuestionCount: qn,
		RosterCount:   rn,
		MaxItems:      service.MaxItems,
		MaxQuestions:  service.MaxQuestions,
		MaxSubjects:   service.MaxSubjects,
	}
}

func (h *Handler) design(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	v := h.designView(p, f)
	v.Err = r.URL.Query().Get("err")
	h.render(w, r, views.Design(h.chrome(), v))
}

// ── 项目 ────────────────────────────────────────────────────────────

func parseProjectForm(r *http.Request) (service.ProjectForm, error) {
	if err := r.ParseForm(); err != nil {
		return service.ProjectForm{}, err
	}
	// datetime-local 送上来的是不带时区的本地时间。用 time.Local 解析
	// 而不是 UTC：管理员填的 14:00 就是现场的 14:00，按 UTC 解析会整体
	// 偏移 8 小时，测评窗口开在半夜。
	pt := func(k string) (time.Time, error) {
		return time.ParseInLocation("2006-01-02T15:04", r.FormValue(k), time.Local)
	}
	start, err := pt("start_at")
	if err != nil {
		return service.ProjectForm{}, fmt.Errorf("开始时间格式有误：%w", err)
	}
	end, err := pt("end_at")
	if err != nil {
		return service.ProjectForm{}, fmt.Errorf("截止时间格式有误：%w", err)
	}
	open, err := pt("result_open_at")
	if err != nil {
		return service.ProjectForm{}, fmt.Errorf("结果开放时间格式有误：%w", err)
	}
	cnt, _ := strconv.Atoi(r.FormValue("expected_count"))
	ap, _ := strconv.Atoi(r.FormValue("ap_count"))
	if ap == 0 {
		ap = 1
	}
	var scores []int
	for _, s := range splitCSV(r.FormValue("grade_scores")) {
		n, err := strconv.Atoi(s)
		if err != nil {
			return service.ProjectForm{}, fmt.Errorf("档位赋分「%s」不是数字", s)
		}
		scores = append(scores, n)
	}
	return service.ProjectForm{
		Name: strings.TrimSpace(r.FormValue("name")), Intro: r.FormValue("intro"),
		StartAt: start, EndAt: end, ResultOpenAt: open,
		ExpectedCount: cnt, APCount: ap, GradeScores: scores,
	}, nil
}

func (h *Handler) newProjectPage(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	h.render(w, r, views.ProjectForm(h.chrome(), views.DesignView{
		P: &model.Project{
			Name: "", StartAt: now.Add(time.Hour), EndAt: now.Add(25 * time.Hour),
			ResultOpenAt: now.Add(25 * time.Hour), ExpectedCount: 30, APCount: 1,
			GradeScores: []int{100, 80, 60, 0},
		},
		FormTitle: "新建测评项目", Action: "/admin/projects/new", Back: "/admin/",
		Err: r.URL.Query().Get("err"),
	}))
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	f, err := parseProjectForm(r)
	if err != nil {
		h.renderProjectForm(w, r, f, err, "新建测评项目", "/admin/projects/new", "/admin/")
		return
	}
	id, err := h.Svc.CreateProject(r.Context(), f)
	if err != nil {
		h.renderProjectForm(w, r, f, err, "新建测评项目", "/admin/projects/new", "/admin/")
		return
	}
	http.Redirect(w, r, "/admin/projects/"+id.Hex()+"/design", http.StatusSeeOther)
}

func (h *Handler) editProjectPage(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.render(w, r, views.ProjectForm(h.chrome(), views.DesignView{
		P: p, FormTitle: p.Name + " · 编辑基本信息",
		Action: "/admin/projects/" + p.ID.Hex() + "/edit",
		Back:   "/admin/projects/" + p.ID.Hex() + "/design",
		Err:    r.URL.Query().Get("err"),
	}))
}

func (h *Handler) updateProject(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	f, err := parseProjectForm(r)
	if err == nil {
		err = h.Svc.UpdateProject(r.Context(), p.ID, f)
	}
	if err != nil {
		h.renderProjectForm(w, r, f, err, p.Name+" · 编辑基本信息",
			"/admin/projects/"+p.ID.Hex()+"/edit",
			"/admin/projects/"+p.ID.Hex()+"/design")
		return
	}
	http.Redirect(w, r, "/admin/projects/"+p.ID.Hex()+"/design", http.StatusSeeOther)
}

func (h *Handler) copyProject(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	id, err := h.Svc.CopyProject(r.Context(), p.ID, strings.TrimSpace(r.FormValue("name")))
	if err != nil {
		h.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+id.Hex()+"/design", http.StatusSeeOther)
}

func (h *Handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Svc.DeleteProject(r.Context(), p.ID); err != nil {
		h.fail(w, err)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

// ── 测评对象 ────────────────────────────────────────────────────────

func (h *Handler) addSubject(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	err = h.Svc.AddSubject(r.Context(), p.ID,
		strings.TrimSpace(r.FormValue("name")),
		strings.TrimSpace(r.FormValue("duty")),
		strings.TrimSpace(r.FormValue("tag")))
	h.backToDesign(w, r, p.ID, err)
}

func (h *Handler) updateSubject(w http.ResponseWriter, r *http.Request) {
	id, err := anon.MustIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	pid, err := h.Svc.DB.SubjectProject(id)
	if err != nil {
		h.fail(w, err)
		return
	}
	err = h.Svc.UpdateSubject(r.Context(), id,
		strings.TrimSpace(r.FormValue("name")),
		strings.TrimSpace(r.FormValue("duty")),
		strings.TrimSpace(r.FormValue("tag")))
	h.backToDesign(w, r, pid, err)
}

func (h *Handler) deleteSubject(w http.ResponseWriter, r *http.Request) {
	id, err := anon.MustIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	pid, err := h.Svc.DB.SubjectProject(id)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.backToDesign(w, r, pid, h.Svc.DeleteSubject(r.Context(), id))
}

// ── 题组 ────────────────────────────────────────────────────────────

func (h *Handler) addGroup(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	err = h.Svc.AddGroup(r.Context(), p.ID,
		strings.TrimSpace(r.FormValue("title")), strings.TrimSpace(r.FormValue("intro")))
	h.backToDesign(w, r, p.ID, err)
}

func (h *Handler) updateGroup(w http.ResponseWriter, r *http.Request) {
	id, pid, err := h.groupIDs(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	err = h.Svc.UpdateGroup(r.Context(), id,
		strings.TrimSpace(r.FormValue("title")), strings.TrimSpace(r.FormValue("intro")))
	h.backToDesign(w, r, pid, err)
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, pid, err := h.groupIDs(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.backToDesign(w, r, pid, h.Svc.DeleteGroup(r.Context(), id))
}

func (h *Handler) groupIDs(r *http.Request) (id, pid anon.ID, err error) {
	id, err = anon.MustIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		return
	}
	pid, err = h.Svc.DB.GroupProject(id)
	return
}

// ── 题目 ────────────────────────────────────────────────────────────

func (h *Handler) newQuestionPage(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	v := h.designView(p, f)
	v.FormTitle = p.Name + " · 新增题目"
	v.Action = "/admin/projects/" + p.ID.Hex() + "/questions"
	v.GroupID = r.URL.Query().Get("group")
	v.Q = &model.Question{Kind: model.KindGrade, Required: true}
	v.AllSubjects = true
	v.Linked = map[string]bool{}
	v.Err = r.URL.Query().Get("err")
	h.render(w, r, views.QuestionForm(h.chrome(), v))
}

func (h *Handler) editQuestionPage(w http.ResponseWriter, r *http.Request) {
	id, err := anon.MustIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	pid, err := h.Svc.DB.QuestionProject(id)
	if err != nil {
		h.fail(w, err)
		return
	}
	p, err := h.Svc.DB.Project(h.Svc.Vault, pid)
	if err != nil {
		h.fail(w, err)
		return
	}
	f, err := h.Svc.DB.Form(h.Svc.Vault, p)
	if err != nil {
		h.fail(w, err)
		return
	}
	var found *model.Question
	var gid anon.ID
	for _, g := range f.Groups {
		for i := range g.Questions {
			if g.Questions[i].ID == id {
				found = &g.Questions[i]
				gid = g.ID
			}
		}
	}
	if found == nil {
		http.NotFound(w, r)
		return
	}
	v := h.designView(p, f)
	v.FormTitle = p.Name + " · 编辑题目"
	v.Action = "/admin/questions/" + id.Hex() + "/update"
	v.GroupID = gid.Hex()
	v.Q = found
	v.Linked = map[string]bool{}
	for _, sid := range found.SubjectIDs {
		v.Linked[sid.Hex()] = true
	}
	// 已关联全部对象时默认勾上"全部"，避免保存一次就把关联缩小
	v.AllSubjects = len(found.SubjectIDs) > 0 && len(found.SubjectIDs) == len(f.Subjects)
	var labels []string
	for _, o := range found.Options {
		labels = append(labels, o.Label)
	}
	v.OptionCSV = strings.Join(labels, ",")
	v.Err = r.URL.Query().Get("err")
	h.render(w, r, views.QuestionForm(h.chrome(), v))
}

func parseQuestionForm(r *http.Request) (service.QuestionForm, error) {
	if err := r.ParseForm(); err != nil {
		return service.QuestionForm{}, err
	}
	gid, err := anon.MustIDFromHex(r.FormValue("group"))
	if err != nil {
		return service.QuestionForm{}, fmt.Errorf("题组无效：%w", err)
	}
	qf := service.QuestionForm{
		GroupID:    gid,
		Kind:       model.QuestionKind(r.FormValue("kind")),
		Title:      strings.TrimSpace(r.FormValue("title")),
		Hint:       strings.TrimSpace(r.FormValue("hint")),
		Required:   r.FormValue("required") == "1",
		Options:    splitCSV(r.FormValue("options")),
		AllSubject: r.FormValue("all_subjects") == "1",
	}
	if g := splitCSV(r.FormValue("grades")); len(g) > 0 {
		qf.Config.Grades = g
	}
	for _, s := range r.Form["subjects"] {
		id, err := anon.MustIDFromHex(s)
		if err != nil {
			return service.QuestionForm{}, fmt.Errorf("测评对象无效：%w", err)
		}
		qf.SubjectIDs = append(qf.SubjectIDs, id)
	}
	return qf, nil
}

func (h *Handler) createQuestion(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	qf, err := parseQuestionForm(r)
	if err == nil {
		err = h.Svc.AddQuestion(r.Context(), p.ID, qf)
	}
	if err != nil {
		// 就地重渲并回填已提交的内容，不重定向。
		// 重定向只带得走一条错误消息，题干、题型、选项、勾选的对象全都丢了——
		// 一道题要填五六个字段，为一个"选项少了一个"从头再来一遍是不可接受的。
		h.renderQuestionForm(w, r, p, f, &qf, err,
			p.Name+" · 新增题目", "/admin/projects/"+p.ID.Hex()+"/questions")
		return
	}
	http.Redirect(w, r, "/admin/projects/"+p.ID.Hex()+"/design", http.StatusSeeOther)
}

// renderQuestionForm 用**提交上来的值**重渲题目表单。
func (h *Handler) renderQuestionForm(w http.ResponseWriter, r *http.Request,
	p *model.Project, f *model.Form, qf *service.QuestionForm, err error,
	title, action string) {

	v := h.designView(p, f)
	v.FormTitle, v.Action = title, action
	v.GroupID = qf.GroupID.Hex()
	v.Q = &model.Question{
		Kind: qf.Kind, Title: qf.Title, Hint: qf.Hint,
		Required: qf.Required, Config: qf.Config,
	}
	v.OptionCSV = strings.Join(qf.Options, ",")
	v.AllSubjects = qf.AllSubject
	v.Linked = map[string]bool{}
	for _, sid := range qf.SubjectIDs {
		v.Linked[sid.Hex()] = true
	}
	if err != nil {
		v.Err = err.Error()
	}
	h.render(w, r, views.QuestionForm(h.chrome(), v))
}

func (h *Handler) updateQuestion(w http.ResponseWriter, r *http.Request) {
	id, err := anon.MustIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	pid, err := h.Svc.DB.QuestionProject(id)
	if err != nil {
		h.fail(w, err)
		return
	}
	qf, err := parseQuestionForm(r)
	if err == nil {
		err = h.Svc.UpdateQuestion(r.Context(), id, qf)
	}
	if err != nil {
		p, ferr := h.Svc.DB.Project(h.Svc.Vault, pid)
		if ferr != nil {
			h.fail(w, ferr)
			return
		}
		f, ferr := h.Svc.DB.Form(h.Svc.Vault, p)
		if ferr != nil {
			h.fail(w, ferr)
			return
		}
		h.renderQuestionForm(w, r, p, f, &qf, err,
			p.Name+" · 编辑题目", "/admin/questions/"+id.Hex()+"/update")
		return
	}
	http.Redirect(w, r, "/admin/projects/"+pid.Hex()+"/design", http.StatusSeeOther)
}

func (h *Handler) deleteQuestion(w http.ResponseWriter, r *http.Request) {
	id, err := anon.MustIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	pid, err := h.Svc.DB.QuestionProject(id)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.backToDesign(w, r, pid, h.Svc.DeleteQuestion(r.Context(), id))
}

// ── 拖拽排序（FR-FRM-022 / FR-FRM-023）─────────────────────────────
//
// 接受完整顺序的 JSON 数组。整体覆盖是幂等的：请求重发一次结果相同，
// 而"上移一位"式的接口一旦丢包，顺序就错乱且无从发现。

func decodeOrder(r *http.Request) ([]anon.ID, error) {
	var hexes []string
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &hexes); err != nil {
		return nil, fmt.Errorf("排序请求格式有误：%w", err)
	}
	out := make([]anon.ID, 0, len(hexes))
	for _, hx := range hexes {
		id, err := anon.MustIDFromHex(hx)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func writeJSONErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "msg": err.Error()})
}

func (h *Handler) reorderQuestions(w http.ResponseWriter, r *http.Request) {
	gid, pid, err := h.groupIDs(r)
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	order, err := decodeOrder(r)
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	if err := h.Svc.ReorderQuestions(r.Context(), pid, gid, order); err != nil {
		writeJSONErr(w, err)
		return
	}
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *Handler) reorderGroups(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	order, err := decodeOrder(r)
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	if err := h.Svc.ReorderGroups(r.Context(), p.ID, order); err != nil {
		writeJSONErr(w, err)
		return
	}
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *Handler) reorderSubjects(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	order, err := decodeOrder(r)
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	if err := h.Svc.ReorderSubjects(r.Context(), p.ID, order); err != nil {
		writeJSONErr(w, err)
		return
	}
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// ── 名单（FR-TKN-010~012）──────────────────────────────────────────

func (h *Handler) rosterPage(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	v := h.designView(p, f)
	v.Err = r.URL.Query().Get("err")
	v.RosterList, _ = h.Svc.DB.Roster(h.Svc.Vault, p.ID)
	h.render(w, r, views.Roster(h.chrome(), v))
}

// uploadRoster 只解析并回显预览，**不写库**（FR-TKN-010 要求导入前确认）。
// 名单是人工誊抄来的，列错位、把表头当数据都很常见，而一旦直接入库再
// 发现，应参加人数就已经错了——它是全部比率的分母。
func (h *Handler) uploadRoster(w http.ResponseWriter, r *http.Request) {
	p, f, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		h.rosterErr(w, r, p.ID, err)
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		h.rosterErr(w, r, p.ID, fmt.Errorf("未收到文件：%w", err))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 8<<20))
	if err != nil {
		h.rosterErr(w, r, p.ID, err)
		return
	}
	names, err := service.ParseRoster(hdr.Filename, data)
	if err != nil {
		h.rosterErr(w, r, p.ID, err)
		return
	}
	v := h.designView(p, f)
	v.Preview = names
	v.RosterList, _ = h.Svc.DB.Roster(h.Svc.Vault, p.ID)
	h.render(w, r, views.Roster(h.chrome(), v))
}

func (h *Handler) confirmRoster(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.rosterErr(w, r, p.ID, err)
		return
	}
	if err := h.Svc.ImportRoster(r.Context(), p.ID, r.Form["label"]); err != nil {
		h.rosterErr(w, r, p.ID, err)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+p.ID.Hex()+"/roster", http.StatusSeeOther)
}

func (h *Handler) clearRoster(w http.ResponseWriter, r *http.Request) {
	p, _, err := h.load(r)
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := h.Svc.ClearRoster(r.Context(), p.ID); err != nil {
		h.rosterErr(w, r, p.ID, err)
		return
	}
	http.Redirect(w, r, "/admin/projects/"+p.ID.Hex()+"/roster", http.StatusSeeOther)
}

// ── 小工具 ──────────────────────────────────────────────────────────

// backToDesign 回设计器页；有错时把错误带在查询串上就地展示，
// 而不是跳到一个独立的错误页——设计器上一次操作的上下文（刚填了什么、
// 改到哪一组）在错误页上全部丢失，管理员得从头再来一遍。
func (h *Handler) backToDesign(w http.ResponseWriter, r *http.Request, pid anon.ID, err error) {
	u := "/admin/projects/" + pid.Hex() + "/design"
	if err != nil {
		u += "?err=" + urlErr(err)
	}
	http.Redirect(w, r, u, http.StatusSeeOther)
}

func (h *Handler) rosterErr(w http.ResponseWriter, r *http.Request, pid anon.ID, err error) {
	http.Redirect(w, r, "/admin/projects/"+pid.Hex()+"/roster?err="+urlErr(err),
		http.StatusSeeOther)
}

func urlErr(err error) string { return url.QueryEscape(err.Error()) }

func splitCSV(s string) []string {
	// 同时接受中英文逗号：管理员在中文输入法下打出来的是全角逗号，
	// 只认半角会让他反复"保存没反应"却看不出哪里错了。
	s = strings.ReplaceAll(s, "，", ",")
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// renderProjectForm 用提交上来的值重渲项目表单，理由同 renderQuestionForm。
//
// 时间字段解析失败时 f 里是零值，直接回填会把三个时间框清空。
// 这里退回用**原始表单值**：管理员看到自己填错的那串字符，才知道该改哪里。
func (h *Handler) renderProjectForm(w http.ResponseWriter, r *http.Request,
	f service.ProjectForm, err error, title, action, back string) {

	p := &model.Project{
		Name: f.Name, Intro: f.Intro,
		StartAt: f.StartAt, EndAt: f.EndAt, ResultOpenAt: f.ResultOpenAt,
		ExpectedCount: f.ExpectedCount, APCount: f.APCount, GradeScores: f.GradeScores,
	}
	if p.Name == "" {
		p.Name = r.FormValue("name")
	}
	if p.StartAt.IsZero() {
		p.StartAt = time.Now().Add(time.Hour)
	}
	if p.EndAt.IsZero() {
		p.EndAt = p.StartAt.Add(24 * time.Hour)
	}
	if p.ResultOpenAt.IsZero() {
		p.ResultOpenAt = p.EndAt
	}
	if p.APCount == 0 {
		p.APCount = 1
	}
	v := views.DesignView{P: p, FormTitle: title, Action: action, Back: back}
	if err != nil {
		v.Err = err.Error()
	}
	h.render(w, r, views.ProjectForm(h.chrome(), v))
}
