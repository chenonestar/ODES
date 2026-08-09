package netsvc

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

func newDHCP() *DHCPServer {
	return &DHCPServer{
		ServerIP:  net.ParseIP("192.168.66.1"),
		Mask:      net.IPv4Mask(255, 255, 255, 0),
		PoolStart: net.ParseIP("192.168.66.50"),
		PoolEnd:   net.ParseIP("192.168.66.250"),
		LeaseTime: 2 * time.Hour,
		leases:    map[string]lease{},
	}
}

// buildDiscover 构造一个客户端 DISCOVER/REQUEST 报文。
func buildDHCPReq(msgType byte, mac net.HardwareAddr, requested net.IP) []byte {
	b := make([]byte, dhcpHeaderLen+4)
	b[0] = opRequest
	b[1] = 1
	b[2] = 6
	copy(b[4:8], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	copy(b[28:34], mac)
	binary.BigEndian.PutUint32(b[236:240], dhcpMagic)
	b = appendOpt(b, optMessageType, []byte{msgType})
	if requested != nil {
		b = appendOpt(b, optRequestedIP, requested.To4())
	}
	b = append(b, optEnd)
	return b
}

func TestDHCPDiscoverThenRequest(t *testing.T) {
	s := newDHCP()
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:01")

	offer := s.handle(buildDHCPReq(dhcpDiscover, mac, nil))
	if offer == nil {
		t.Fatal("DISCOVER 未产生 OFFER")
	}
	opts := parseOptions(offer[240:])
	if opts[optMessageType][0] != dhcpOffer {
		t.Fatalf("期望 OFFER，得到 %d", opts[optMessageType][0])
	}
	yiaddr := net.IP(offer[16:20])
	if !s.inPool(yiaddr) {
		t.Fatalf("分配的地址 %s 不在池内", yiaddr)
	}
	// 网关与 DNS 必须都指向本机，否则手机会用运营商 DNS
	if !net.IP(opts[optRouter]).Equal(s.ServerIP) {
		t.Errorf("网关应为 %s，得到 %s", s.ServerIP, net.IP(opts[optRouter]))
	}
	if !net.IP(opts[optDNS]).Equal(s.ServerIP) {
		t.Errorf("DNS 应为 %s，得到 %s", s.ServerIP, net.IP(opts[optDNS]))
	}
	if binary.BigEndian.Uint32(opts[optLeaseTime]) != 7200 {
		t.Errorf("租期应为 7200 秒")
	}
	if len(offer) < 300 {
		t.Errorf("报文长度 %d 小于最小 BOOTP 长度", len(offer))
	}

	ack := s.handle(buildDHCPReq(dhcpRequest, mac, yiaddr))
	if parseOptions(ack[240:])[optMessageType][0] != dhcpAck {
		t.Fatal("REQUEST 未产生 ACK")
	}
	if !net.IP(ack[16:20]).Equal(yiaddr) {
		t.Fatal("ACK 的地址与 OFFER 不一致")
	}
	if s.LeaseCount() != 1 {
		t.Fatalf("租约数应为 1，得到 %d", s.LeaseCount())
	}
}

func TestDHCPStableAddressAndNoCollision(t *testing.T) {
	s := newDHCP()
	seen := map[string]bool{}
	for i := 0; i < 200; i++ { // 单批上限 200 人
		mac := net.HardwareAddr{0xAA, 0, 0, 0, byte(i >> 8), byte(i)}
		ack := s.handle(buildDHCPReq(dhcpRequest, mac, nil))
		if ack == nil {
			t.Fatalf("第 %d 台未获地址", i+1)
		}
		ip := net.IP(ack[16:20]).String()
		if seen[ip] {
			t.Fatalf("地址冲突: %s", ip)
		}
		seen[ip] = true
	}
	if s.LeaseCount() != 200 {
		t.Fatalf("应有 200 条租约，得到 %d", s.LeaseCount())
	}
	// 同一 MAC 再次请求须拿到同一地址（避免刷新页面后地址漂移）
	mac := net.HardwareAddr{0xAA, 0, 0, 0, 0, 7}
	a := net.IP(s.handle(buildDHCPReq(dhcpRequest, mac, nil))[16:20]).String()
	b := net.IP(s.handle(buildDHCPReq(dhcpRequest, mac, nil))[16:20]).String()
	if a != b {
		t.Fatalf("同一终端两次请求地址不一致: %s vs %s", a, b)
	}
}

func TestDHCPPoolExhausted(t *testing.T) {
	s := newDHCP()
	s.PoolStart = net.ParseIP("192.168.66.50")
	s.PoolEnd = net.ParseIP("192.168.66.51") // 只有 2 个
	for i := 0; i < 2; i++ {
		mac := net.HardwareAddr{0xBB, 0, 0, 0, 0, byte(i)}
		if s.handle(buildDHCPReq(dhcpDiscover, mac, nil)) == nil {
			t.Fatalf("第 %d 台不应被拒绝", i+1)
		}
	}
	mac := net.HardwareAddr{0xBB, 0, 0, 0, 0, 9}
	if s.handle(buildDHCPReq(dhcpDiscover, mac, nil)) != nil {
		t.Fatal("池已满时应拒绝 DISCOVER 而非分配重复地址")
	}
}

// ── DNS ───────────────────────────────────────────────────────────────

func buildDNSQuery(name string, qtype uint16) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:2], 0x1234)
	b[2] = 0x01 // RD
	binary.BigEndian.PutUint16(b[4:6], 1)
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, qtype)
	b = binary.BigEndian.AppendUint16(b, dnsClassIN)
	return b
}

func newDNS() *DNSServer {
	ip := net.ParseIP("192.168.66.1")
	return &DNSServer{ServerIP: ip, Static: map[string]net.IP{
		"eval.example.gov.cn": ip, "e.example.gov.cn": ip,
	}}
}

func answerIP(resp []byte) net.IP {
	if len(resp) < 4 || binary.BigEndian.Uint16(resp[6:8]) == 0 {
		return nil
	}
	return net.IP(resp[len(resp)-4:])
}

func TestDNSResolvesCertHostAndProbes(t *testing.T) {
	s := newDNS()
	cases := []struct {
		name string
		want string
	}{
		{"eval.example.gov.cn", "192.168.66.1"}, // 配置的证书域名
		{"e.example.gov.cn", "192.168.66.1"},    // 短码入口域名
		{"connectivitycheck.gstatic.com", "192.168.66.1"},
		{"connect.rom.miui.com", "192.168.66.1"},                   // 小米
		{"connectivitycheck.platform.hicloud.com", "192.168.66.1"}, // 华为
		{"wifi.vivo.com.cn", "192.168.66.1"},                       // vivo
		{"captive.apple.com", "192.168.66.1"},                      // iOS
		{"www.msftconnecttest.com", "192.168.66.1"},                // Windows
		{"dns.msftncsi.com", "131.107.255.255"},                    // 必须返回真实期望值
	}
	for _, c := range cases {
		resp := s.handle(buildDNSQuery(c.name, dnsTypeA))
		got := answerIP(resp)
		if got == nil || got.String() != c.want {
			t.Errorf("%s: 期望 %s，得到 %v", c.name, c.want, got)
		}
		if resp[2]&0x80 == 0 {
			t.Errorf("%s: 响应未置 QR 位", c.name)
		}
	}
}

func TestDNSUnknownIsNXDomainNotForwarded(t *testing.T) {
	s := newDNS()
	resp := s.handle(buildDNSQuery("www.baidu.com", dnsTypeA))
	if resp == nil {
		t.Fatal("应有响应")
	}
	if rcode := resp[3] & 0x0F; rcode != 3 {
		t.Fatalf("未知域名应回 NXDOMAIN(3)，得到 rcode=%d", rcode)
	}
	if binary.BigEndian.Uint16(resp[6:8]) != 0 {
		t.Fatal("NXDOMAIN 不应带答案")
	}
}

// AAAA 查询若回 NXDOMAIN，部分客户端会连带放弃 A 查询。
func TestDNSAAAAReturnsEmptyNoError(t *testing.T) {
	s := newDNS()
	resp := s.handle(buildDNSQuery("eval.example.gov.cn", dnsTypeAAAA))
	if rcode := resp[3] & 0x0F; rcode != 0 {
		t.Fatalf("持有的域名查 AAAA 应回 NOERROR，得到 rcode=%d", rcode)
	}
	if binary.BigEndian.Uint16(resp[6:8]) != 0 {
		t.Fatal("AAAA 应返回 0 条记录")
	}
}

func TestProbeLookupIsCaseInsensitive(t *testing.T) {
	if LookupProbe("Captive.Apple.Com.") != ProbeApple {
		t.Fatal("域名匹配应忽略大小写与末尾点")
	}
	if LookupProbe("example.com") != ProbeNone {
		t.Fatal("无关域名不应被识别为探测域名")
	}
}

// 通配兜底已删除：未在配置中列出的域名一律 NXDOMAIN，
// 避免"随便一个域名都能解析到本机"这种会掩盖配置错误的行为。
func TestDNSUnconfiguredDomainNotResolved(t *testing.T) {
	s := newDNS()
	for _, n := range []string{"eval.other.gov.cn", "192-168-66-1.nip.io", "foo.bar"} {
		resp := s.handle(buildDNSQuery(n, dnsTypeA))
		if got := answerIP(resp); got != nil {
			t.Errorf("%s 不应被解析，却得到 %v", n, got)
		}
	}
}
