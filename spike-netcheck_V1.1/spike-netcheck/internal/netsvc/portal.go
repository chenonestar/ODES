package netsvc

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
)

// ── 连通性探测应答（FR-NET-030）─────────────────────────────────────
//
// 这是尖刺①要验证的核心：测评网络不通互联网，手机的联网自检必然失败。
// 后果是 Android 的"自动切换网络"会把手机切回移动数据，此后再也访问
// 不到本机。必须由服务端应答系统级探测，使手机判定"网络可用"。

type PortalStats struct {
	Probe204   atomic.Int64
	ProbeApple atomic.Int64
	ProbeMS    atomic.Int64
	PageHits   atomic.Int64
	Other      atomic.Int64
}

type Portal struct {
	ServerIP  net.IP
	CertHost  string // 192-168-66-1.nip.io
	HTTPSPort int
	Stats     *PortalStats
	Log       func(format string, a ...any)
}

func (p *Portal) logf(f string, a ...any) {
	if p.Log != nil {
		p.Log(f, a...)
	}
}

func (p *Portal) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.route)
	return mux
}

func (p *Portal) route(w http.ResponseWriter, r *http.Request) {
	host := stripPort(r.Host)
	kind := LookupProbe(host)

	// 部分客户端直接按路径探测，不看 Host
	if kind == ProbeNone {
		switch {
		case strings.HasSuffix(r.URL.Path, "/generate_204"):
			kind = Probe204
		case strings.Contains(r.URL.Path, "hotspot-detect"):
			kind = ProbeApple
		case strings.HasSuffix(r.URL.Path, "connecttest.txt"):
			kind = ProbeMSConnect
		case strings.HasSuffix(r.URL.Path, "ncsi.txt"):
			kind = ProbeMSNcsi
		}
	}

	switch kind {
	case Probe204:
		p.Stats.Probe204.Add(1)
		p.logf("探测  Android  %s%s → 204", host, r.URL.Path)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNoContent)
		return

	case ProbeApple:
		p.Stats.ProbeApple.Add(1)
		p.logf("探测  Apple    %s%s → Success", host, r.URL.Path)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>")
		return

	case ProbeMSConnect:
		p.Stats.ProbeMS.Add(1)
		p.logf("探测  Windows  %s%s → Connect Test", host, r.URL.Path)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Microsoft Connect Test")
		return

	case ProbeMSNcsi:
		p.Stats.ProbeMS.Add(1)
		p.logf("探测  Windows  %s%s → NCSI", host, r.URL.Path)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Microsoft NCSI")
		return
	}

	// 非探测请求：一律跳转到 HTTPS（正式版行为）
	p.Stats.Other.Add(1)
	target := fmt.Sprintf("https://%s:%d%s", p.CertHost, p.HTTPSPort, r.URL.RequestURI())
	p.logf("HTTP  %s%s → 301 %s", host, r.URL.Path, target)
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// TestPageHandler 是尖刺阶段的替身作答页，用于在手机上确认
// HTTPS 证书无告警、页面可正常打开。正式版本由作答端路由替代。
func (p *Portal) TestPageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.Stats.PageHits.Add(1)
		p.logf("页面  来自 %s 的访问（第 %d 次）", stripPort(r.RemoteAddr), p.Stats.PageHits.Load())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, testPage, p.CertHost, stripPort(r.RemoteAddr))
	})
}

const testPage = `<!DOCTYPE html><html lang="zh-CN"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>测评网络验证</title><style>
body{font-family:-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;
background:#fbfaf7;color:#1b2434;margin:0;padding:32px 20px;line-height:1.9}
.ok{font-size:56px;text-align:center;color:#2f6f4f;margin:10px 0}
h1{font-size:21px;text-align:center;margin:0 0 24px}
.card{background:#fff;border:1px solid #ddd7cb;border-left:3px solid #9e2b25;
padding:16px 18px;margin-bottom:14px;font-size:15px}
b{color:#9e2b25}code{background:#eef0f3;padding:1px 5px;border-radius:2px;font-size:13px}
</style></head><body>
<div class="ok">&#10003;</div>
<h1>测评网络连通验证通过</h1>
<div class="card">域名 <code>%s</code> 解析正常，本机 DNS 生效。</div>
<div class="card">本机取得地址 <b>%s</b>，DHCP 分配正常。</div>
<div class="card">页面经 <b>HTTPS</b> 加载且未出现证书告警，说明证书方案可用。</div>
<div class="card">若手机在此期间<b>没有</b>提示"无法上网"并切回移动数据，
说明连通性探测应答生效，尖刺验证通过。</div>
</body></html>`

func stripPort(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}
