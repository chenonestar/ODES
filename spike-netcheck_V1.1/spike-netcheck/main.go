// spike-netcheck 是「线上民主测评系统」技术尖刺 ① 的验证程序。
//
// 验证目标：单个 Go 可执行文件能否在 Windows / macOS / Linux 上同时承担
// DHCP、DNS、连通性探测应答与 HTTPS 服务，使参评人员的手机连上一个
// 完全离线的无线网络后，能够正常打开作答页且不被系统判定为"无法上网"。
//
// 通过标准：
//  1. 手机连上 WiFi 后自动取得 192.168.66.50-250 的地址；
//  2. 手机不提示"无法上网"，且 Android 不自动切回移动数据；
//  3. 浏览器打开 https://192-168-66-1.nip.io:8443/ 显示验证页，无证书告警；
//  4. iOS 与 Android 各两个版本、含国产品牌，行为一致。
//
// 本程序仅依赖标准库。internal/netsvc 可直接演进为正式项目的同名包。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"dpcp/spike-netcheck/internal/netsvc"
)

func main() {
	var (
		ipStr     = flag.String("ip", "192.168.66.1", "本机在测评网络中的静态地址")
		poolStart = flag.String("pool-start", "192.168.66.50", "DHCP 地址池起始")
		poolEnd   = flag.String("pool-end", "192.168.66.250", "DHCP 地址池结束")
		httpsPort = flag.Int("https-port", 8443, "HTTPS 端口")
		domain    = flag.String("domain", "eval.example.gov.cn", "证书域名（考察部门自有域名，对应 config.toml 的 domain.primary）")
		shortName = flag.String("short-entry", "e.example.gov.cn", "短码入口域名（对应 domain.short_entry）")
		certFile  = flag.String("cert", "", "证书文件（留空则用自签名，仅供开发与连通性验证）")
		keyFile   = flag.String("key", "", "私钥文件")
		noDHCP    = flag.Bool("no-dhcp", false, "关闭内置 DHCP（改由外置路由承担时使用）")
		noDNS     = flag.Bool("no-dns", false, "关闭内置 DNS")
	)
	flag.Parse()

	serverIP := net.ParseIP(*ipStr)
	if serverIP == nil || serverIP.To4() == nil {
		log.Fatalf("无效的 IPv4 地址: %s", *ipStr)
	}
	certHost := *domain

	logf := func(f string, a ...any) {
		fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(f, a...))
	}

	banner(serverIP, certHost, *shortName, *httpsPort, *poolStart, *poolEnd)
	preflight(serverIP, *noDHCP, *noDNS)

	stats := &netsvc.PortalStats{}
	portal := &netsvc.Portal{
		ServerIP: serverIP, CertHost: certHost,
		HTTPSPort: *httpsPort, Stats: stats, Log: logf,
	}

	var (
		wg    sync.WaitGroup
		fails []string
		mu    sync.Mutex
	)
	fail := func(name string, err error) {
		mu.Lock()
		fails = append(fails, fmt.Sprintf("%s: %v", name, err))
		mu.Unlock()
		logf("!! %s 启动失败: %v", name, err)
	}

	dhcp := &netsvc.DHCPServer{
		ServerIP:  serverIP,
		Mask:      net.IPv4Mask(255, 255, 255, 0),
		PoolStart: net.ParseIP(*poolStart),
		PoolEnd:   net.ParseIP(*poolEnd),
		LeaseTime: 2 * time.Hour,
		Log:       logf,
	}
	if !*noDHCP {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := dhcp.ListenAndServe(); err != nil {
				fail("DHCP/67", err)
			}
		}()
	}

	if !*noDNS {
		dns := &netsvc.DNSServer{
			ServerIP: serverIP,
			Static: map[string]net.IP{
				certHost:   serverIP,
				*shortName: serverIP,
			},
			Log: logf,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := dns.ListenAndServe(); err != nil {
				fail("DNS/53", err)
			}
		}()
	}

	// HTTP :80 —— 连通性探测应答与跳转
	httpSrv := &http.Server{Addr: ":80", Handler: portal.Handler()}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fail("HTTP/80", err)
		}
	}()

	// HTTPS —— 验证页
	tlsCfg, selfSigned, checks, err := loadTLS(*certFile, *keyFile, certHost)
	printCertChecks(checks, selfSigned, certHost)
	if err != nil {
		log.Fatalf("证书装载失败: %v", err)
	}
	httpsSrv := &http.Server{
		Addr: fmt.Sprintf(":%d", *httpsPort), Handler: portal.TestPageHandler(), TLSConfig: tlsCfg,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := httpsSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fail(fmt.Sprintf("HTTPS/%d", *httpsPort), err)
		}
	}()

	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	n := len(fails)
	mu.Unlock()
	if n > 0 {
		fmt.Println()
		logf("有服务未能启动，见上方 !! 行。常见原因：")
		logf("  · Windows 需在防火墙提示中放行，或以管理员身份运行；")
		logf("  · Linux 绑定 53/67/80 需要 root 或 CAP_NET_BIND_SERVICE；")
		logf("  · 本机已有 DNS/DHCP 服务占用端口（如 systemd-resolved）。")
	}

	fmt.Println()
	logf("就绪。请用手机连接测评网络，然后打开：https://%s:%d/", certHost, *httpsPort)
	logf("按 Ctrl+C 结束并输出验证小结。")
	fmt.Println()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
	httpsSrv.Shutdown(ctx)
	summary(stats, dhcp, selfSigned)
}

// printCertChecks 输出 CK-01 ~ CK-05 的逐项结果。
// CK-02 失败是最隐蔽的故障：证书有效、未过期，但签发时用的主机名与配置
// 的域名不一致，现场每部手机照样红屏。必须在办公室拦住。
func printCertChecks(checks []certCheck, selfSigned bool, domain string) {
	if selfSigned {
		fmt.Printf("  [证书] !! 自签名证书，手机将出现安全告警（属预期）\n")
		fmt.Printf("         完整验证需用公共 CA 为 %s 签发的真实证书：-cert / -key\n\n", domain)
		return
	}
	fail := 0
	for _, c := range checks {
		mark := "✓"
		if !c.OK {
			mark, fail = "!!", fail+1
		}
		fmt.Printf("  [证书] %s %s %-18s %s\n", mark, c.Code, c.Name, c.Msg)
	}
	if fail > 0 {
		fmt.Printf("\n  证书自检未通过 %d 项。CK-02 失败意味着证书虽有效，但签发时用的\n", fail)
		fmt.Printf("  主机名与配置域名不符 —— 现场每部手机都会红屏，务必在出发前解决。\n")
	}
	fmt.Println()
}

func banner(ip net.IP, certHost, shortName string, port int, ps, pe string) {
	fmt.Printf(`
┌──────────────────────────────────────────────────────────────┐
│  线上民主测评系统 · 技术尖刺 ① 内置网络服务验证                │
└──────────────────────────────────────────────────────────────┘
  本机地址    %s
  地址池      %s – %s
  证书域名    %s
  短码入口    %s
  HTTPS       :%d          HTTP :80        DNS :53      DHCP :67
  探测域名    已覆盖 %d 个（含小米/华为/OPPO/vivo/三星）

`, ip, ps, pe, certHost, shortName, port, netsvc.ProbeDomainCount())
}

// preflight 检查本机网卡是否已配置为约定的静态地址。
// 现场最常见的失误就是忘了改网卡地址，导致一切正常但手机连不上。
func preflight(want net.IP, noDHCP, noDNS bool) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	found := false
	var have []string
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			have = append(have, ipn.IP.String())
			if ipn.IP.Equal(want) {
				found = true
			}
		}
	}
	if !found {
		fmt.Printf("  [自检] !! 未在本机找到地址 %s\n", want)
		fmt.Printf("         当前网卡地址：%s\n", strings.Join(have, ", "))
		fmt.Printf("         请先把有线网卡设为静态 %s / 255.255.255.0\n\n", want)
	} else {
		fmt.Printf("  [自检] 网卡地址 %s 正常\n\n", want)
	}
	if noDHCP {
		fmt.Println("  [自检] 内置 DHCP 已关闭，须由外置路由分配地址，且 DNS 必须指向本机")
	}
	if noDNS {
		fmt.Println("  [自检] 内置 DNS 已关闭，证书域名将无法在离线环境下解析")
	}
}

func summary(s *netsvc.PortalStats, d *netsvc.DHCPServer, selfSigned bool) {
	pass := func(b bool) string {
		if b {
			return "通过"
		}
		return "未观察到"
	}
	leases := d.LeaseCount()
	probes := s.Probe204.Load() + s.ProbeApple.Load() + s.ProbeMS.Load()
	fmt.Printf(`
┌──────────────── 验证小结 ────────────────┐
  DHCP 分配地址        %-6d 台      %s
  连通性探测应答       %-6d 次      %s
    Android 系          %d
    Apple 系            %d
    Windows 系          %d
  验证页访问           %-6d 次      %s
  证书                 %s
└──────────────────────────────────────────┘

判定要点：
  · "连通性探测应答" 为 0 说明探测请求没有到达本机，
    多为 DNS 未生效或 80 端口被拦截，此时手机很可能已切回移动数据。
  · 若手机全程未提示"无法上网"，且上表三项均有计数，尖刺 ① 通过。
  · 请分别用 iOS、原生 Android、以及至少两台国产品牌手机各测一次。

`, leases, pass(leases > 0),
		probes, pass(probes > 0),
		s.Probe204.Load(), s.ProbeApple.Load(), s.ProbeMS.Load(),
		s.PageHits.Load(), pass(s.PageHits.Load() > 0),
		map[bool]string{true: "自签名（手机会告警，属预期）", false: "真实证书"}[selfSigned])
}
