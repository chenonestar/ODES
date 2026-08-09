package httpd

import (
	"net"
	"testing"
)

// S-1 回归：作答端与管理端在同一端口上必须能共存。
//
// 背景：LLD 8.1/8.2 规定作答端 :8443、管理端 127.0.0.1:8443。若作答端绑
// 0.0.0.0，两者在同一端口上重叠，操作系统拒绝共存——两种绑定顺序都会
// EADDRINUSE。改绑具体网卡地址后才成立。本测试锁住这个前提。
func TestEvalAndAdminCanShareOnePort(t *testing.T) {
	const port = "19733"

	// 用两个回环地址模拟「具体网卡地址 + 127.0.0.1」。
	// 生产上是 192.168.66.1 与 127.0.0.1，同样是两个互不重叠的具体地址。
	evalLn, err := net.Listen("tcp", "127.0.0.2:"+port)
	if err != nil {
		t.Skipf("本机不支持 127.0.0.2，跳过: %v", err)
	}
	defer evalLn.Close()

	adminLn, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("管理端未能与作答端共用端口 %s：%v\n"+
			"这说明作答端又绑回了通配地址（0.0.0.0），LLD 8.2 的端口方案会失效", port, err)
	}
	defer adminLn.Close()
}

// 对照：证明上面那条测试确实能发现问题——绑通配地址时必然冲突。
func TestWildcardBindConflictsWithLoopback(t *testing.T) {
	const port = "19734"
	wild, err := net.Listen("tcp", ":"+port)
	if err != nil {
		t.Skip(err)
	}
	defer wild.Close()

	lo, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err == nil {
		lo.Close()
		t.Skip("本平台允许通配与回环共存，该平台上 S-1 不成立")
	}
	t.Logf("通配地址与回环在同端口冲突（预期）：%v", err)
}

func TestIsLoopback(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"127.0.0.1", true}, {"localhost", true}, {"127.0.0.2", true},
		{"192.168.66.1", false}, {"0.0.0.0", false}, {"", false},
	} {
		if got := isLoopback(c.in); got != c.want {
			t.Errorf("isLoopback(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}
