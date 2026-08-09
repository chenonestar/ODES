package netsvc

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

// ── DHCPv4 最小实现（RFC 2131），仅依赖标准库 ──────────────────────────
//
// 设计约束（来自需求 FR-NET-010 / 012、NFR-ANO-040）：
//   · 租约表**只在内存**，进程退出即消失，绝不落盘。
//     租约表含 MAC ↔ IP 映射，属可关联信息，持久化会破坏匿名性。
//   · 不记录 hostname（选项 12）。手机的主机名常含机主姓名拼音，
//     一旦写入日志即构成身份线索。尖刺阶段曾把它打印到控制台观察，
//     移入正式项目时已删除：本包任何路径都不得读取或输出选项 12。

const (
	dhcpHeaderLen = 236
	dhcpMagic     = 0x63825363

	opRequest = 1
	opReply   = 2

	// 选项码
	optSubnetMask   = 1
	optRouter       = 3
	optDNS          = 6
	optRequestedIP  = 50
	optLeaseTime    = 51
	optMessageType  = 53
	optServerID     = 54
	optParamReqList = 55
	optEnd          = 255

	// 报文类型
	dhcpDiscover = 1
	dhcpOffer    = 2
	dhcpRequest  = 3
	dhcpDecline  = 4
	dhcpAck      = 5
	dhcpNak      = 6
	dhcpRelease  = 7
	dhcpInform   = 8
)

type lease struct {
	ip      net.IP
	expires time.Time
}

// DHCPServer 为测评网络分配地址。
type DHCPServer struct {
	ServerIP  net.IP        // 192.168.66.1
	Mask      net.IPMask    // 255.255.255.0
	PoolStart net.IP        // 192.168.66.50
	PoolEnd   net.IP        // 192.168.66.250
	LeaseTime time.Duration // 2h
	Log       func(format string, a ...any)

	mu     sync.Mutex
	leases map[string]lease // key: MAC 字符串，仅内存
	conn   net.PacketConn
}

func (s *DHCPServer) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log(f, a...)
	}
}

// LeaseCount 返回当前有效租约数，供管理端显示"实时接入终端数"（FR-NET-041）。
func (s *DHCPServer) LeaseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	now := time.Now()
	for _, l := range s.leases {
		if l.expires.After(now) {
			n++
		}
	}
	return n
}

func (s *DHCPServer) ListenAndServe() error {
	if s.leases == nil {
		s.leases = make(map[string]lease)
	}
	if s.LeaseTime == 0 {
		s.LeaseTime = 2 * time.Hour
	}
	c, err := net.ListenPacket("udp4", ":67")
	if err != nil {
		return fmt.Errorf("绑定 UDP/67 失败（多为权限不足或已被占用）: %w", err)
	}
	s.conn = c
	defer c.Close()

	buf := make([]byte, 1500)
	for {
		n, _, err := c.ReadFrom(buf)
		if err != nil {
			return err
		}
		if n < dhcpHeaderLen+4 {
			continue
		}
		reply := s.handle(buf[:n])
		if reply == nil {
			continue
		}
		// 客户端此刻尚无 IP，统一广播到 255.255.255.255:68。
		// 这是不引入 raw socket / ARP 注入的通行做法，兼容性足够。
		dst := &net.UDPAddr{IP: net.IPv4bcast, Port: 68}
		if _, err := c.WriteTo(reply, dst); err != nil {
			s.logf("DHCP 回包失败: %v", err)
		}
	}
}

func (s *DHCPServer) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func (s *DHCPServer) handle(pkt []byte) []byte {
	if pkt[0] != opRequest {
		return nil
	}
	if binary.BigEndian.Uint32(pkt[236:240]) != dhcpMagic {
		return nil
	}
	opts := parseOptions(pkt[240:])

	mt := byte(0)
	if v, ok := opts[optMessageType]; ok && len(v) == 1 {
		mt = v[0]
	}
	hlen := int(pkt[2])
	if hlen == 0 || hlen > 16 {
		hlen = 6
	}
	mac := net.HardwareAddr(pkt[28 : 28+hlen])
	xid := pkt[4:8]

	switch mt {
	case dhcpDiscover:
		ip := s.allocate(mac, opts[optRequestedIP])
		if ip == nil {
			s.logf("DHCP 地址池已耗尽，拒绝 %s", mac)
			return nil
		}
		s.logf("DHCP DISCOVER %s → OFFER %s", mac, ip)
		return s.build(dhcpOffer, xid, pkt[10:12], mac, hlen, ip)

	case dhcpRequest:
		want := opts[optRequestedIP]
		if want == nil && !isZero(pkt[12:16]) {
			want = pkt[12:16] // ciaddr：续租
		}
		ip := s.allocate(mac, want)
		if ip == nil {
			s.logf("DHCP REQUEST %s → NAK", mac)
			return s.build(dhcpNak, xid, pkt[10:12], mac, hlen, net.IPv4zero)
		}
		s.logf("DHCP REQUEST %s → ACK %s  （当前接入 %d 台）", mac, ip, s.LeaseCount())
		return s.build(dhcpAck, xid, pkt[10:12], mac, hlen, ip)

	case dhcpRelease, dhcpDecline:
		s.mu.Lock()
		delete(s.leases, mac.String())
		s.mu.Unlock()
		return nil
	}
	return nil
}

// allocate 分配或续租地址。已有租约优先复用，保证同一终端地址稳定。
func (s *DHCPServer) allocate(mac net.HardwareAddr, requested []byte) net.IP {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	key := mac.String()

	if l, ok := s.leases[key]; ok {
		l.expires = now.Add(s.LeaseTime)
		s.leases[key] = l
		return l.ip
	}

	used := make(map[string]bool, len(s.leases))
	for k, l := range s.leases {
		if l.expires.After(now) {
			used[l.ip.String()] = true
		} else {
			delete(s.leases, k)
		}
	}

	// 优先满足客户端请求的地址（减少切换网络后的地址变动）
	if len(requested) == 4 {
		ip := net.IP(requested).To4()
		if s.inPool(ip) && !used[ip.String()] {
			s.leases[key] = lease{ip: ip, expires: now.Add(s.LeaseTime)}
			return ip
		}
	}

	start := ip2u32(s.PoolStart)
	end := ip2u32(s.PoolEnd)
	for v := start; v <= end; v++ {
		ip := u322ip(v)
		if used[ip.String()] {
			continue
		}
		s.leases[key] = lease{ip: ip, expires: now.Add(s.LeaseTime)}
		return ip
	}
	return nil
}

func (s *DHCPServer) inPool(ip net.IP) bool {
	if ip == nil {
		return false
	}
	v := ip2u32(ip)
	return v >= ip2u32(s.PoolStart) && v <= ip2u32(s.PoolEnd)
}

func (s *DHCPServer) build(msgType byte, xid, flags []byte, mac net.HardwareAddr, hlen int, yiaddr net.IP) []byte {
	b := make([]byte, dhcpHeaderLen+4, 400)
	b[0] = opReply
	b[1] = 1 // ethernet
	b[2] = byte(hlen)
	copy(b[4:8], xid)
	copy(b[10:12], flags)
	copy(b[16:20], yiaddr.To4())
	copy(b[20:24], s.ServerIP.To4()) // siaddr
	copy(b[28:28+hlen], mac)
	binary.BigEndian.PutUint32(b[236:240], dhcpMagic)

	b = appendOpt(b, optMessageType, []byte{msgType})
	b = appendOpt(b, optServerID, s.ServerIP.To4())
	if msgType != dhcpNak {
		lt := make([]byte, 4)
		binary.BigEndian.PutUint32(lt, uint32(s.LeaseTime/time.Second))
		b = appendOpt(b, optLeaseTime, lt)
		b = appendOpt(b, optSubnetMask, []byte(s.Mask))
		b = appendOpt(b, optRouter, s.ServerIP.To4())
		// 关键：DNS 必须指向本机。测评网络不通外网，只有本机内置 DNS
		// 才能解析证书域名与各厂商探测域名；指向别处则解析必然失败。
		b = appendOpt(b, optDNS, s.ServerIP.To4())
	}
	b = append(b, optEnd)
	for len(b) < 300 { // 补齐到最小 BOOTP 长度，兼容老旧客户端
		b = append(b, 0)
	}
	return b
}

// ── 工具 ──────────────────────────────────────────────────────────────

func parseOptions(b []byte) map[byte][]byte {
	out := make(map[byte][]byte)
	for i := 0; i < len(b); {
		code := b[i]
		if code == optEnd {
			break
		}
		if code == 0 { // pad
			i++
			continue
		}
		if i+1 >= len(b) {
			break
		}
		l := int(b[i+1])
		if i+2+l > len(b) {
			break
		}
		out[code] = b[i+2 : i+2+l]
		i += 2 + l
	}
	return out
}

func appendOpt(b []byte, code byte, val []byte) []byte {
	b = append(b, code, byte(len(val)))
	return append(b, val...)
}

func ip2u32(ip net.IP) uint32 { return binary.BigEndian.Uint32(ip.To4()) }

func u322ip(v uint32) net.IP {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return net.IP(b)
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
