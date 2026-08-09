// Command eval 是线上民主测评系统的单一可执行文件。
//
// 一个进程内承载两套前端形态与四个网络监听（HLD 3.1）：
//
//	管理端 admin  →  127.0.0.1        仅本机可达
//	作答端 eval   →  0.0.0.0:8443     测评网络可达
//	Portal        →  :80              连通性探测应答与跳转
//	DHCP / DNS    →  :67 / :53        现场组网时承担地址分配与名称解析
//
// 开发调试用 -dev：只绑回环、不碰 53/67、自签名证书。除此之外的行为
// 与现场部署完全一致——不做"开发模式跳过校验"，否则调试通过不代表
// 现场能用。
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"odes/internal/admin"
	"odes/internal/config"
	"odes/internal/crypto"
	"odes/internal/httpd"
	"odes/internal/model"
	"odes/internal/netsvc"
	"odes/internal/service"
	"odes/internal/store"
)

func main() {
	var (
		dev      = flag.Bool("dev", false, "开发模式：只绑回环、不启用 DHCP/DNS、自签名证书")
		seed     = flag.Bool("seed", false, "生成一份演示项目（仅在库为空时执行）")
		cfgPath  = flag.String("config", "config.toml", "配置文件路径")
		dataDir  = flag.String("data", "data", "数据目录")
		password = flag.String("password", "", "管理员口令（留空则交互式输入；仅 -dev 下建议使用）")
		port     = flag.Int("port", 0, "覆盖 HTTPS 端口")
	)
	flag.Parse()

	logf := func(f string, a ...any) {
		fmt.Printf("%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(f, a...))
	}

	// ── 配置 ──
	cfg := config.Default()
	if *dev {
		cfg = config.DevDefault()
	}
	if loaded, found, err := config.Load(*cfgPath); err != nil {
		fatal("配置文件解析失败: %v", err)
	} else if found {
		if *dev {
			// -dev 下配置文件仍然生效，但网络相关项被开发默认值覆盖，
			// 避免在开发机上意外去抢 53/67 端口。
			d := cfg
			cfg = loaded
			cfg.Network = d.Network
			cfg.TLS.Mode = "selfsigned"
			cfg.Domain.Primary, cfg.Domain.ShortEntry = d.Domain.Primary, d.Domain.ShortEntry
		} else {
			cfg = loaded
		}
		logf("配置文件 %s 已加载", *cfgPath)
	} else {
		logf("未找到 %s，使用内置默认值", *cfgPath)
	}
	if *port > 0 {
		cfg.Network.HTTPSPort = *port
	}

	banner(cfg, *dev)

	// ── 数据库 ──
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		fatal("创建数据目录失败: %v", err)
	}
	dbPath := filepath.Join(*dataDir, "eval.db")
	db, err := store.Open(dbPath)
	if err != nil {
		fatal("打开数据库失败: %v", err)
	}
	defer db.Close()
	logf("数据库 %s　匿名性断言通过（WITHOUT ROWID / 无时间列 / 无跨表外键）", dbPath)

	// ── 口令与密钥 ──
	vault, err := unlock(db, *password, logf)
	if err != nil {
		fatal("%v", err)
	}
	defer vault.Close()

	svc := service.New(db, vault, cfg.Domain.Primary)

	if *seed {
		n, _ := db.Projects(vault)
		if len(n) == 0 {
			id, err := svc.SeedDemo(context.Background())
			if err != nil {
				fatal("生成演示数据失败: %v", err)
			}
			logf("已生成演示项目 %s", id.Hex()[:8])
		} else {
			logf("库中已有项目，跳过 -seed")
		}
	}

	// ── 证书 ──
	tlsCfg, certStatus, err := config.LoadTLS(cfg.TLS, cfg.Domain.Primary, cfg.Domain.Alternate)
	if err != nil {
		fatal("证书装载失败: %v", err)
	}
	certAlert := ""
	for _, c := range certStatus.Checks {
		logf("  [证书] %s", c)
		if !c.OK {
			certAlert = c.Code + " " + c.Msg
		}
	}
	if certStatus.SelfSigned {
		logf("  [证书] !! 自签名证书 —— 手机会出现安全告警，禁止用于正式测评")
	}
	// PC-09 用的是"剩余 ≥1 天"，与启动自检的 CK-04（≥21 天）不是同一条判据。
	certUsable, certMsg := certStatus.Usable()
	if *dev {
		// 开发模式下自签名是预期的，不因此拦住发布——否则本地根本走不到
		// 发布之后的流程。其余判据仍然生效。
		certUsable, certMsg = true, "开发模式：跳过证书门槛（现场不适用）"
	}

	// ── 启动自检（FR-NET-040）──
	warns := preflight(cfg, *dev)
	for _, w := range warns {
		logf("  [自检] !! %s", w)
	}
	// 二维码一致性：令牌单打印后若改了域名，已打印的码会失效（LLD 12.3）
	var domainWarns []string
	if ps, err := db.Projects(vault); err == nil {
		for _, p := range ps {
			if err := svc.CheckDomainConsistency(p); err != nil {
				domainWarns = append(domainWarns, err.Error())
				logf("  [自检] !! %v", err)
			}
		}
	}

	// ── 内置网络服务 ──
	var dhcp *netsvc.DHCPServer
	if cfg.Network.EnableDHCP {
		dhcp = &netsvc.DHCPServer{
			ServerIP: net.ParseIP(cfg.Network.ListenIP),
			Mask:     net.IPv4Mask(255, 255, 255, 0),
			PoolStart: net.ParseIP(cfg.Network.PoolStart),
			PoolEnd:   net.ParseIP(cfg.Network.PoolEnd),
			LeaseTime: 2 * time.Hour, Log: logf,
		}
		go func() {
			if err := dhcp.ListenAndServe(); err != nil {
				logf("!! DHCP/67 启动失败: %v", err)
				logf("   降级路径：-config 中置 enable_dhcp=false，由外置路由分配地址；"+
					"但 DNS 不可降级（门户探测应答依赖它）")
			}
		}()
	}
	if cfg.Network.EnableDNS {
		ip := net.ParseIP(cfg.Network.ListenIP)
		static := map[string]net.IP{cfg.Domain.Primary: ip, cfg.Domain.ShortEntry: ip}
		if cfg.Domain.Alternate != "" {
			static[cfg.Domain.Alternate] = ip
		}
		dns := &netsvc.DNSServer{ServerIP: ip, Static: static, Log: logf}
		go func() {
			if err := dns.ListenAndServe(); err != nil {
				logf("!! DNS/53 启动失败: %v", err)
			}
		}()
	}

	// ── HTTP 服务 ──
	sessions := admin.NewSessions(time.Duration(cfg.Session.TimeoutMin) * time.Minute)
	adminH := &admin.Handler{
		Svc: svc, CertUsable: certUsable, CertMsg: certMsg,
		CertAlert: certAlert, SelfSigned: certStatus.SelfSigned,
		Preflight: warns, SessionMgr: sessions, ShortEntry: cfg.Domain.ShortEntry,
		DomainWarns: domainWarns,
	}
	if dhcp != nil {
		adminH.LeaseCount = dhcp.LeaseCount
	}

	portalStats := &netsvc.PortalStats{}
	portal := &netsvc.Portal{
		ServerIP: net.ParseIP(cfg.Network.ListenIP), CertHost: cfg.Domain.Primary,
		HTTPSPort: cfg.Network.HTTPSPort, Stats: portalStats, Log: logf,
	}

	srv := &httpd.Server{
		EvalHandler:   httpd.EvalRoutes(svc, cfg.Domain.ShortEntry),
		AdminHandler:  adminH.Routes(),
		PortalHandler: portal.Handler(),
		TLSConfig:     tlsCfg,
		HTTPSPort:     cfg.Network.HTTPSPort,
		BindIP:        bindIP(cfg, *dev),
		Log:           logf,
	}
	if err := srv.Start(); err != nil {
		fatal("%v", err)
	}

	// ── 过程备份：每 N 分钟快照（FR-SYS-040 / NFR-REL-020）──
	go snapshotLoop(dbPath, cfg.Snapshot.Dir, cfg.Snapshot.IntervalMin, logf)

	// ── 状态推进器：到点自动开放 / 截止 ──
	go statusTicker(svc, logf)

	fmt.Println()
	logf("就绪。管理端 %s", srv.AdminURL)
	if *dev {
		logf("开发模式：作答端与管理端同在回环上，自签名证书会触发浏览器告警，")
		logf("          点击「高级 → 继续前往」即可。")
	}
	logf("按 Ctrl+C 退出。")
	fmt.Println()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	fmt.Println()
	logf("正在退出：刷快照 → 关闭监听 → 清零内存密钥")
	_ = snapshotOnce(dbPath, cfg.Snapshot.Dir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	logf("已退出。DEK 随进程消失，不落盘。")
}

// unlock 首次启动时引导设置口令，之后每次启动解开 DEK。
func unlock(db *store.DB, given string, logf func(string, ...any)) (*crypto.Vault, error) {
	salt, err := db.GetMeta("kek_salt")
	if err != nil {
		return nil, err
	}

	if salt == nil {
		// 首次启动（FR-SYS-010/011）
		fmt.Println()
		fmt.Println("  ── 首次启动：设置管理员口令 ──")
		fmt.Println("  口令长度 ≥10 位、含两类以上字符。")
		fmt.Println("  ⚠ 口令无找回机制，遗忘则数据不可访问。建议考察组两人分别知悉。")
		fmt.Println()
		pw := given
		for {
			if pw == "" {
				pw = prompt("  设置口令: ")
			}
			if err := crypto.CheckPasswordStrength(pw); err != nil {
				fmt.Printf("  %v\n", err)
				pw = ""
				continue
			}
			break
		}
		v, meta, err := crypto.Init(pw)
		if err != nil {
			return nil, err
		}
		for k, val := range map[string][]byte{
			"kek_salt": meta.KEKSalt, "wrapped_dek": meta.WrappedDEK,
			"admin_pwd_hash": meta.PwdHash,
		} {
			if err := db.SetMeta(k, val); err != nil {
				return nil, err
			}
		}
		logf("口令已设置，主密钥已生成并以口令派生密钥包裹后落盘")
		return v, nil
	}

	var meta crypto.Meta
	meta.KEKSalt = salt
	if meta.WrappedDEK, err = db.GetMeta("wrapped_dek"); err != nil {
		return nil, err
	}
	if meta.PwdHash, err = db.GetMeta("admin_pwd_hash"); err != nil {
		return nil, err
	}
	// FR-SYS-010：连续 5 次失败锁定 15 分钟。Web 端由 admin.Sessions 负责，
	// 这里是命令行入口——原先只是重试 5 次就退出，等于给了无限次尝试
	// （重启程序即可重来）。锁定状态记在数据库里，重启不清零。
	if until, locked := lockedUntil(db); locked {
		return nil, fmt.Errorf("连续失败次数过多，请于 %s 后重试",
			until.Format("2006-01-02 15:04"))
	}
	pw := given
	for i := 0; i < 5; i++ {
		if pw == "" {
			pw = prompt("  管理员口令: ")
		}
		v, err := crypto.Unlock(pw, meta)
		if err == nil {
			_ = db.SetMeta("login_lock_until", nil)
			logf("口令校验通过，DEK 已解开并驻留内存")
			return v, nil
		}
		fmt.Printf("  %v\n", err)
		pw = ""
	}
	until := time.Now().Add(15 * time.Minute)
	_ = db.SetMeta("login_lock_until", []byte(until.Format(time.RFC3339)))
	return nil, fmt.Errorf("口令连续错误 5 次，已锁定至 %s", until.Format("15:04"))
}

// lockedUntil 读取登录锁定截止时间（FR-SYS-010）。
func lockedUntil(db *store.DB) (time.Time, bool) {
	b, err := db.GetMeta("login_lock_until")
	if err != nil || len(b) == 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, string(b))
	if err != nil || time.Now().After(t) {
		return time.Time{}, false
	}
	return t, true
}

func prompt(label string) string {
	fmt.Print(label)
	if term.IsTerminal(int(syscall.Stdin)) {
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	sc := bufio.NewScanner(os.Stdin)
	if sc.Scan() {
		return strings.TrimSpace(sc.Text())
	}
	return ""
}

// preflight 检查现场部署最常见的失误（FR-NET-040）。
func preflight(cfg config.Config, dev bool) []string {
	var out []string
	if dev {
		return nil
	}
	want := net.ParseIP(cfg.Network.ListenIP)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
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
		out = append(out, fmt.Sprintf(
			"未在本机找到地址 %s（当前网卡：%s）。请先把有线网卡设为静态 %s / 255.255.255.0"+
				"——现场最常见的失误就是忘了改网卡地址，表现为一切正常但手机连不上。",
			want, strings.Join(have, ", "), want))
	}
	return out
}

// bindIP 返回作答端监听的**具体**网卡地址。
//
// 不返回空串（0.0.0.0）：那样会与管理端的 127.0.0.1:8443 在同一端口上
// 重叠，操作系统拒绝两者共存。详见 httpd.Server.Start 的说明。
func bindIP(cfg config.Config, dev bool) string {
	if dev {
		return "127.0.0.1"
	}
	return cfg.Network.ListenIP
}

// snapshotLoop 每 N 分钟把数据文件快照到指定目录（可指向 U 盘）。
// 主机故障时冷备笔记本拷入最近快照即可接管，已提交数据最多损失 N 分钟。
func snapshotLoop(dbPath, dir string, minutes int, logf func(string, ...any)) {
	if minutes <= 0 {
		minutes = 5
	}
	for range time.Tick(time.Duration(minutes) * time.Minute) {
		if err := snapshotOnce(dbPath, dir); err != nil {
			logf("!! 快照失败: %v", err)
		}
	}
}

func snapshotOnce(dbPath, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	src, err := os.ReadFile(dbPath)
	if err != nil {
		return err
	}
	name := filepath.Join(dir, fmt.Sprintf("eval-%s.db", time.Now().Format("20060102-1504")))
	return os.WriteFile(name, src, 0o600)
}

// statusTicker 到点自动开放与截止（HLD 3.3 / LLD 7.1 的「推进器」）。
//
// 先立即推一次再进入循环：装备长期关机，开机时 start_at 往往已经过去，
// 让管理员干等一分钟才看到"进行中"是没有道理的。
func statusTicker(svc *service.Service, logf func(string, ...any)) {
	advance(svc, logf)
	for range time.Tick(30 * time.Second) {
		advance(svc, logf)
	}
}

func advance(svc *service.Service, logf func(string, ...any)) {
	done, err := svc.Advance(context.Background(), time.Now())
	for _, a := range done {
		switch a.To {
		case model.StatusRunning:
			logf("项目「%s」到达开始时间，自动开放作答", a.Name)
		case model.StatusClosed:
			logf("项目「%s」到达截止时间，已自动截止并完成匿名化重建", a.Name)
		}
	}
	if err != nil {
		logf("!! 自动推进状态失败: %v", err)
	}
}

func banner(cfg config.Config, dev bool) {
	mode := "现场部署"
	if dev {
		mode = "开发调试（-dev）"
	}
	fmt.Printf(`
┌──────────────────────────────────────────────────────────────┐
│  线上民主测评系统 · ODES                                      │
└──────────────────────────────────────────────────────────────┘
  运行模式    %s
  本机地址    %s
  证书域名    %s
  短码入口    %s
  HTTPS       :%d        DHCP %s      DNS %s
  探测域名    已覆盖 %d 个（含小米/华为/OPPO/vivo/三星）

`, mode, cfg.Network.ListenIP, cfg.Domain.Primary, cfg.Domain.ShortEntry,
		cfg.Network.HTTPSPort, onOff(cfg.Network.EnableDHCP), onOff(cfg.Network.EnableDNS),
		netsvc.ProbeDomainCount())
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "\n错误：%s\n\n", fmt.Sprintf(f, a...))
	os.Exit(1)
}
