package qr

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

func TestPNGDecodesAndIsSquare(t *testing.T) {
	b, err := PNG("https://eval.example.gov.cn:8443/e/ABCDEFGHJKMNPQRSTUVWXYZ23456789AB", 1024)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("生成的不是合法 PNG: %v", err)
	}
	r := img.Bounds()
	if r.Dx() != r.Dy() {
		t.Fatalf("二维码应为正方形，得到 %dx%d", r.Dx(), r.Dy())
	}
	if r.Dx() < 945 {
		// 8cm @300dpi ≈ 945px，低于此值印出来会糊
		t.Fatalf("打印分辨率不足：%dpx，8cm@300dpi 需要 ≥945px", r.Dx())
	}
}

func TestDataURIIsInlineAndSelfContained(t *testing.T) {
	u, err := DataURI("https://eval.example.gov.cn:8443/e/TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "data:image/png;base64,") {
		t.Fatalf("应为内联 data URI，得到 %.40s", u)
	}
	// 令牌值绝不能出现在 URI 里——内联的意义正是不把令牌带进任何
	// 会被记录的地方（浏览器历史、访问日志）
	if strings.Contains(u, "TOKEN") {
		t.Fatal("data URI 中不应出现令牌明文")
	}
}

// 令牌单一张一个码：内容不同必须产出不同的码，否则整批单子都指向同一人。
func TestDifferentTokensDifferentCodes(t *testing.T) {
	a, err := PNG("https://x/e/AAAA", 256)
	if err != nil {
		t.Fatal(err)
	}
	b, err := PNG("https://x/e/BBBB", 256)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("不同令牌生成了相同的二维码")
	}
}

func TestEmptyContentRejected(t *testing.T) {
	if _, err := PNG("", 256); err == nil {
		t.Fatal("空内容应报错，而不是产出一个扫不出东西的码")
	}
}
