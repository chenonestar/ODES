package httpd

import (
	"errors"
	"html/template"
	"net/http"

	"odes/internal/anon"
)

func isErr(err, target error) bool { return errors.Is(err, target) }

func anonVerify(code string) bool { return anon.VerifyShortCode(code) }

var shortTmpl = template.Must(template.New("j").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<title>输入编号进入测评</title><style>
html{font-size:18px}
body{font-family:-apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif;
background:#fbfaf7;color:#1b2434;margin:0;padding:56px 22px;line-height:1.8}
.wrap{max-width:420px;margin:0 auto}
h1{font-size:1.15em;margin:0 0 6px}
p.sub{color:#5c6675;font-size:.85em;margin:0 0 26px}
input{width:100%;font-size:1.6em;letter-spacing:.16em;text-align:center;padding:16px 10px;
border:1px solid #ddd7cb;border-radius:4px;text-transform:uppercase;font-family:inherit}
input:focus{outline:none;border-color:#9e2b25;box-shadow:0 0 0 3px rgba(158,43,37,.12)}
button{width:100%;margin-top:16px;min-height:52px;font-size:1em;font-weight:600;
background:#9e2b25;color:#fff;border:none;border-radius:4px;cursor:pointer}
.err{background:#fdf1f0;border-left:3px solid #9e2b25;padding:10px 12px;margin-bottom:18px;
font-size:.88em}
.tip{margin-top:28px;font-size:.8em;color:#5c6675;border-top:1px solid #ddd7cb;padding-top:14px}
</style></head><body><div class="wrap">
<h1>输入令牌单上的编号</h1>
<p class="sub">扫码不成功时使用。编号共 8 位，在令牌单下方。</p>
{{if .Err}}<div class="err">{{.Err}}</div>{{end}}
<form method="post" action="/j">
  <input name="code" maxlength="9" autocomplete="off" autocapitalize="characters"
         autocorrect="off" spellcheck="false" inputmode="latin" placeholder="K7M4QX9B" autofocus>
  <button type="submit">进入测评</button>
</form>
<div class="tip">手机提示"无法上网"属正常现象——测评网络本就不连互联网，
选择"仍然连接"即可。</div>
</div></body></html>`))

func shortCodePage(w http.ResponseWriter, errMsg, shortEntry string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = shortTmpl.Execute(w, map[string]any{"Err": errMsg, "Entry": shortEntry})
}
