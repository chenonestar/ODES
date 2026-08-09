package netsvc

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// ── DNS 最小权威应答（RFC 1035），仅依赖标准库 ────────────────────────
//
// 职责（FR-NET-020 / 021 / 030）：
//   1. 把**配置文件中指定的证书域名**解析到本机，使离线环境下 HTTPS 可用；
//      该域名为考察部门自有域名，公网 DNS 上也有一条指向同一私有 IP 的
//      A 记录 —— 那条记录只为证书签发流程而存在，现场并不依赖它；
//   2. 把短码入口短域名解析到本机；
//   3. 把各系统的连通性探测域名解析到本机，由 HTTP 侧给出期望应答；
//   4. dns.msftncsi.com 返回 Windows 期望的真实地址，不能指向本机；
//   5. 其余一律 NXDOMAIN，**不做上游转发**——测评网络本就不通外网，
//      转发只会引入超时等待，拖慢页面加载。

const (
	dnsTypeA    = 1
	dnsTypeAAAA = 28
	dnsClassIN  = 1
	dnsTTL      = 60
)

type DNSServer struct {
	ServerIP net.IP
	// Static 为固定解析表，来自配置：证书域名、短码入口域名
	Static map[string]net.IP
	Log    func(format string, a ...any)

	conn net.PacketConn
}

func (s *DNSServer) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log(f, a...)
	}
}

func (s *DNSServer) ListenAndServe() error {
	c, err := net.ListenPacket("udp4", ":53")
	if err != nil {
		return fmt.Errorf("绑定 UDP/53 失败（多为权限不足，或被本机 DNS 服务占用）: %w", err)
	}
	s.conn = c
	defer c.Close()

	buf := make([]byte, 512)
	for {
		n, addr, err := c.ReadFrom(buf)
		if err != nil {
			return err
		}
		req := make([]byte, n)
		copy(req, buf[:n])
		go func() {
			if resp := s.handle(req); resp != nil {
				c.WriteTo(resp, addr)
			}
		}()
	}
}

func (s *DNSServer) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func (s *DNSServer) handle(req []byte) []byte {
	if len(req) < 12 {
		return nil
	}
	qdcount := binary.BigEndian.Uint16(req[4:6])
	if qdcount != 1 {
		return nil
	}
	name, off, ok := parseName(req, 12)
	if !ok || off+4 > len(req) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(req[off : off+2])
	qend := off + 4

	ip, kind := s.resolve(name)

	resp := make([]byte, 0, len(req)+16)
	resp = append(resp, req[:qend]...)

	// flags: QR=1 AA=1，RD 沿用请求
	flags := uint16(0x8400)
	if req[2]&0x01 != 0 {
		flags |= 0x0100 // RD
	}

	answers := uint16(0)
	switch {
	case ip == nil:
		flags |= 3 // NXDOMAIN
	case qtype == dnsTypeAAAA:
		// 我们持有该域名但无 AAAA 记录：返回 NOERROR + 0 条记录。
		// 若此处回 NXDOMAIN，部分客户端会认为整个域名不存在，
		// 连带放弃 A 查询。
	case qtype == dnsTypeA:
		answers = 1
	default:
		// 其它类型（如 HTTPS/SVCB 记录，iOS 会查）同样 NOERROR + 空
	}

	binary.BigEndian.PutUint16(resp[2:4], flags)
	binary.BigEndian.PutUint16(resp[6:8], answers)
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)

	if answers == 1 {
		resp = append(resp, 0xC0, 0x0C) // 指针指向问题段的名字
		resp = binary.BigEndian.AppendUint16(resp, dnsTypeA)
		resp = binary.BigEndian.AppendUint16(resp, dnsClassIN)
		resp = binary.BigEndian.AppendUint32(resp, dnsTTL)
		resp = binary.BigEndian.AppendUint16(resp, 4)
		resp = append(resp, ip.To4()...)
	}

	s.logf("DNS  %-42s → %s", name, describe(ip, kind))
	return resp
}

type resolveKind string

const (
	kindStatic resolveKind = "证书/入口域名"
	kindProbe  resolveKind = "连通性探测 → 本机"
	kindNcsi   resolveKind = "Windows NCSI（返回真实期望值）"
	kindMiss   resolveKind = "NXDOMAIN（不转发）"
)

func (s *DNSServer) resolve(name string) (net.IP, resolveKind) {
	n := strings.ToLower(strings.TrimSuffix(name, "."))

	if ip, ok := s.Static[n]; ok {
		return ip, kindStatic
	}
	if n == msNcsiDNSName {
		return net.ParseIP(msNcsiDNSAddr), kindNcsi
	}
	if LookupProbe(n) != ProbeNone {
		return s.ServerIP, kindProbe
	}
	return nil, kindMiss
}

func describe(ip net.IP, k resolveKind) string {
	if ip == nil {
		return string(k)
	}
	return fmt.Sprintf("%-15s  %s", ip, k)
}

func parseName(msg []byte, off int) (string, int, bool) {
	var sb strings.Builder
	for {
		if off >= len(msg) {
			return "", 0, false
		}
		l := int(msg[off])
		if l == 0 {
			return sb.String(), off + 1, true
		}
		if l&0xC0 != 0 { // 请求中不应出现压缩指针
			return "", 0, false
		}
		off++
		if off+l > len(msg) {
			return "", 0, false
		}
		sb.Write(msg[off : off+l])
		sb.WriteByte('.')
		off += l
	}
}
