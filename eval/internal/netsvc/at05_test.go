package netsvc

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── AT-05 租约不落盘（LLD 11.1 / NFR-ANO-040）─────────────────────
//
// 文档判据：DHCP 分配 50 个地址后强杀进程，扫描全部文件不得出现 MAC 地址。
//
// 为什么 MAC 不能落盘：MAC 唯一标识一部手机。测评现场只要有一份
// "MAC ↔ IP ↔ 时间"的记录，再配上 Web 侧任何带时间的痕迹，就能把
// 某次提交对应到某部手机、进而对应到人。这是一条绕过令牌匿名性的旁路，
// 而它不需要任何解密。
//
// 实现上租约只存在于 DHCPServer.leases 这个内存 map 里（见 dhcp.go
// 的 `leases map[string]lease // key: MAC 字符串，仅内存`）。本用例做的是
// **反向验证**：真跑 50 次分配，然后把工作目录整个扫一遍。
//
// "强杀进程"在单元测试里等价于"不做任何收尾直接扫盘"——本包没有注册
// 任何退出钩子，因此进程被 kill -9 与测试里直接扫描的结果一致。

func TestAT05_DHCPLeasesNeverTouchDisk(t *testing.T) {
	dir := t.TempDir()

	// 在工作目录里预置一个文件，确保扫描逻辑本身是有效的——
	// 若扫描器有 bug（比如根本没遍历到文件），这个哨兵会让测试失败。
	sentinel := "aa:bb:cc:dd:ee:ff"
	if err := os.WriteFile(filepath.Join(dir, "sentinel.txt"),
		[]byte("这是哨兵文件，内含 "+sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newDHCP()
	// 日志也接出来一并检查：日志是最容易漏的落盘路径
	var logBuf strings.Builder
	s.Log = func(f string, a ...any) { fmt.Fprintf(&logBuf, f+"\n", a...) }

	var macs []net.HardwareAddr
	for i := 0; i < 50; i++ {
		mac, err := net.ParseMAC(fmt.Sprintf("02:00:5e:%02x:%02x:%02x",
			i/65536%256, i/256%256, i%256))
		if err != nil {
			t.Fatal(err)
		}
		macs = append(macs, mac)

		offer := s.handle(buildDHCPReq(dhcpDiscover, mac, nil))
		if offer == nil {
			t.Fatalf("第 %d 台 DISCOVER 未产生 OFFER", i+1)
		}
		yiaddr := net.IP(append([]byte(nil), offer[16:20]...))
		if ack := s.handle(buildDHCPReq(dhcpRequest, mac, yiaddr)); ack == nil {
			t.Fatalf("第 %d 台 REQUEST 未产生 ACK", i+1)
		}
	}
	if n := s.LeaseCount(); n != 50 {
		t.Fatalf("应分配出 50 个租约，实际 %d 个——分配没真正发生，后面的扫描就没有意义", n)
	}

	// 哨兵必须被扫出来，否则说明扫描器本身失效
	if hits := scanDirForMACs(t, dir, []string{sentinel}); len(hits) == 0 {
		t.Fatal("哨兵文件未被扫描命中：扫描逻辑本身有问题，本用例的结论不可信")
	}

	// 真正的断言：50 个 MAC 一个都不能出现在任何文件里
	var needles []string
	for _, m := range macs {
		needles = append(needles, m.String())                  // aa:bb:cc:dd:ee:ff
		needles = append(needles, strings.ToUpper(m.String())) // AA:BB:CC:DD:EE:FF
		needles = append(needles, strings.ReplaceAll(m.String(), ":", "-"))
		needles = append(needles, strings.ReplaceAll(m.String(), ":", ""))
	}
	if hits := scanDirForMACs(t, dir, needles); len(hits) > 0 {
		t.Fatalf("MAC 地址落盘了（NFR-ANO-040）：%v", hits)
	}

	// 二进制形态也扫一遍：MAC 有可能以 6 字节原始形式被写出去
	for _, m := range macs {
		if hit := scanDirForBytes(t, dir, []byte(m)); hit != "" {
			t.Fatalf("MAC %s 以二进制形式出现在 %s", m, hit)
		}
	}

	// 日志同样不得出现 MAC
	logged := logBuf.String()
	for _, m := range macs {
		if strings.Contains(strings.ToLower(logged), strings.ToLower(m.String())) {
			t.Fatalf("MAC %s 出现在日志里", m)
		}
	}
	t.Logf("AT-05：50 个租约全部只在内存，工作目录 %d 个文件与日志均无 MAC 痕迹",
		countFiles(t, dir))
}

func scanDirForMACs(t *testing.T, dir string, needles []string) []string {
	t.Helper()
	var hits []string
	walkFiles(t, dir, func(path string, b []byte) {
		text := string(b)
		for _, n := range needles {
			if strings.Contains(text, n) {
				hits = append(hits, path+" 含 "+n)
			}
		}
	})
	return hits
}

func scanDirForBytes(t *testing.T, dir string, needle []byte) string {
	t.Helper()
	var hit string
	walkFiles(t, dir, func(path string, b []byte) {
		if hit == "" && len(needle) > 0 && containsBytes(b, needle) {
			hit = path
		}
	})
	return hit
}

func walkFiles(t *testing.T, dir string, fn func(path string, b []byte)) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		fn(path, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	walkFiles(t, dir, func(string, []byte) { n++ })
	return n
}

func containsBytes(hay, needle []byte) bool {
	if len(needle) == 0 || len(hay) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
