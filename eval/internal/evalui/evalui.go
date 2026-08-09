// Package evalui 是作答端：自包含单页 + 提交接收。
//
// 为什么不用服务端翻页（ADR-001）：htmx 那类模型每次翻页要一次网络请求，
// 参评人员翻到第 3 位测评对象时若无线信号波动，页面就卡在那里。而这
// 恰恰是会场里最可能发生的情形——两百人同时在场，人体对 5GHz 的吸收
// 使后排信号明显劣化。失败代价不对称：管理端卡一下可以刷新重来，
// 参评人员卡住则可能直接放弃，散会后无法补测。
//
// 因此：服务端一次性下发完整页面（含全部题目数据），此后翻页、填写、
// 校验全部在浏览器本地完成，只有提交才再次接触网络。
package evalui

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"

	"odes/internal/anon"
	"odes/internal/model"
	"odes/web"
)

//go:embed page.html
var pageHTML string

var tmpl = template.Must(template.New("eval").Parse(pageHTML))

// pageData 是内嵌到页面里的数据契约（LLD 9.1）。
//
// 姓名与说明在服务端解密后写入，页面内是明文——参评人员必须看到姓名，
// 这是必然的；传输安全由 HTTPS 保证。
type pageData struct {
	Token     string     `json:"token"`
	Trial     bool       `json:"trial"`
	SubmitURL string     `json:"submitURL"`
	Title     string     `json:"title"`
	Intro     string     `json:"intro"`
	Grades    []string   `json:"grades"`
	Subjects  []subjectJ `json:"subjects"`
	Groups    []groupJ   `json:"groups"`
}

type subjectJ struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Duty string `json:"duty,omitempty"`
}

type groupJ struct {
	Title     string      `json:"title"`
	Questions []questionJ `json:"questions"`
}

type questionJ struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Hint     string    `json:"hint,omitempty"`
	Required bool      `json:"required"`
	Subjects []string  `json:"subjects"`
	Options  []optionJ `json:"options,omitempty"`
	ScoreMin int       `json:"scoreMin,omitempty"`
	ScoreMax int       `json:"scoreMax,omitempty"`
	MaxChars int       `json:"maxChars,omitempty"`
}

type optionJ struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Render 生成自包含单页。
func Render(w http.ResponseWriter, p *model.Project, f *model.Form,
	token string, trial bool, submitURL string) error {

	d := pageData{
		Token: token, Trial: trial, SubmitURL: submitURL,
		Title: p.Name, Intro: p.Intro, Grades: f.Grades,
	}
	for _, s := range f.Subjects {
		d.Subjects = append(d.Subjects, subjectJ{ID: s.ID.Hex(), Name: s.Name, Duty: s.Duty})
	}
	for _, g := range f.Groups {
		gj := groupJ{Title: g.Title}
		for _, q := range g.Questions {
			qj := questionJ{
				ID: q.ID.Hex(), Kind: string(q.Kind), Title: q.Title, Hint: q.Hint,
				Required: q.Required, ScoreMin: q.Config.ScoreMin,
				ScoreMax: q.Config.ScoreMax, MaxChars: q.Config.MaxChars,
			}
			for _, sid := range q.SubjectIDs {
				qj.Subjects = append(qj.Subjects, sid.Hex())
			}
			for _, o := range q.Options {
				qj.Options = append(qj.Options, optionJ{ID: o.ID.Hex(), Label: o.Label})
			}
			gj.Questions = append(gj.Questions, qj)
		}
		if len(gj.Questions) > 0 {
			d.Groups = append(d.Groups, gj)
		}
	}

	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	// 数据以 <script type="application/json"> 内嵌。必须转义 "</script"，
	// 否则测评对象姓名或评语提示里的该字符串会提前闭合脚本块（XSS）。
	safe := strings.ReplaceAll(string(raw), "</", `<\/`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// 作答页不加载任何外部资源，用 CSP 把这一点固化下来：
	// 即便将来有人不慎引入了 CDN 链接，浏览器也会拒绝加载。
	// 用的是 Alpine 的 CSP 构建，表达式只能是属性名/方法名，因此**不需要**
	// 'unsafe-eval'。作答页得以保持 default-src 'none' 的严格 CSP：
	// 页面不加载任何外部资源，即便将来有人不慎引入 CDN 链接也会被浏览器拒绝。
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
			"connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")

	return tmpl.Execute(w, map[string]any{
		"Title":    p.Name,
		"FormJSON": template.JS(safe),
		"CSS":      template.CSS(web.EvalCSS),
		"Alpine":   template.JS(web.Alpine),
	})
}

// DonePage 是重复访问已使用令牌时的提示页（FR-ANS-018）。
func DonePage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>测评</title><style>
body{font-family:-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;background:#fbfaf7;
color:#1b2434;margin:0;padding:80px 24px;text-align:center;font-size:18px;line-height:1.9}
.t{font-size:56px;color:#2f6f4f}p{color:#5c6675}</style></head><body>
<div class="t">&#10003;</div><p>` + template.HTMLEscapeString(msg) + `</p></body></html>`))
}

var _ = anon.NewID
