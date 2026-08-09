package netsvc

import "strings"

// ProbeKind 表示某个域名对应的连通性探测类型。
type ProbeKind int

const (
	ProbeNone      ProbeKind = iota
	Probe204                 // 期望 HTTP 204 空响应（Android 系）
	ProbeApple               // 期望包含 Success 的 HTML（iOS / macOS）
	ProbeMSConnect           // 期望正文 "Microsoft Connect Test"
	ProbeMSNcsi              // 期望正文 "Microsoft NCSI"
)

// probeDomains 是需要由本机应答的连通性探测域名。
//
// 说明：国行手机占绝对多数，各厂商在 AOSP 之外另有自己的探测域名，
// 只覆盖 gstatic 是不够的——小米/华为/OPPO/vivo 的机器仍会判定
// "无法上网" 并触发自动切回移动数据。下表按厂商分组，便于后续增补。
var probeDomains = map[string]ProbeKind{
	// —— Android 原生 ——
	"connectivitycheck.gstatic.com": Probe204,
	"connectivitycheck.android.com": Probe204,
	"clients3.google.com":           Probe204,
	"www.gstatic.com":               Probe204,
	"android.clients.google.com":    Probe204,

	// —— 小米 / Redmi ——
	"connect.rom.miui.com": Probe204,
	"cn.wifi.miui.com":     Probe204,

	// —— 华为 / 荣耀 ——
	"connectivitycheck.platform.hicloud.com": Probe204,
	"connectivitycheck.hihonor.com":          Probe204,

	// —— OPPO / 一加 / realme ——
	"conn1.oppomobile.com": Probe204,
	"conn2.oppomobile.com": Probe204,

	// —— vivo ——
	"wifi.vivo.com.cn": Probe204,

	// —— 三星 ——
	"connectivitycheck.samsung.com": Probe204,

	// —— iOS / macOS ——
	"captive.apple.com": ProbeApple,
	"www.apple.com":     ProbeApple,

	// —— Windows ——
	"www.msftconnecttest.com":  ProbeMSConnect,
	"ipv6.msftconnecttest.com": ProbeMSConnect,
	"www.msftncsi.com":         ProbeMSNcsi,
}

// msNcsiDNSProbe 是 Windows 的 DNS 侧探测：它要求 dns.msftncsi.com
// 必须解析为 131.107.255.255 这个确切地址。若解析到别的地址（例如
// 我们自己的 IP），Windows 会判定网络被劫持并标记为"无 Internet"。
// 因此这一条必须返回真实的期望值，不能指向本机。
const (
	msNcsiDNSName = "dns.msftncsi.com"
	msNcsiDNSAddr = "131.107.255.255"
)

// LookupProbe 判断域名是否为连通性探测域名。
func LookupProbe(name string) ProbeKind {
	if k, ok := probeDomains[strings.ToLower(strings.TrimSuffix(name, "."))]; ok {
		return k
	}
	return ProbeNone
}

// ProbeDomainCount 供启动自检输出。
func ProbeDomainCount() int { return len(probeDomains) }
