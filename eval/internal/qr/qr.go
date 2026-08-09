// Package qr 生成令牌单上的二维码（FR-TKN-032 / FR-TKN-033）。
//
// FR-TKN-033 要求**本地算法、不调用任何外部服务**。这条不是洁癖：
// 调用外部二维码服务意味着把每一个令牌值发给第三方，而令牌值是打开
// 作答页的凭证——等于把整场测评的入场券交出去，且访问日志本身就是
// 一份"谁在什么时候生成了哪些令牌"的旁证。现场也全程离线，外部服务
// 根本不可达。
//
// 编码是纯计算，没有任何 I/O。
package qr

import (
	"bytes"
	"encoding/base64"
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// 纠错级别取 Medium（约 15%）。
//
// 不取更高级别是因为二维码是**印在纸上、由几百部手机在会场光线下扫**的：
// 纠错级别越高模块越密，同样 8cm 边长下每个模块越小，反而更难扫。
// Medium 足以覆盖折痕与轻微污损，是 URL 场景的通行选择。
const level = qrcode.Medium

// printPx 是生成分辨率。令牌单要求边长 ≥8cm（LLD 4.3），
// 8cm 在 300dpi 下约 945 像素，取 1024 留出余量。
const printPx = 1024

// PNG 返回二维码的 PNG 字节。
func PNG(content string, px int) ([]byte, error) {
	if content == "" {
		return nil, fmt.Errorf("二维码内容为空")
	}
	if px <= 0 {
		px = printPx
	}
	return qrcode.Encode(content, level, px)
}

// DataURI 返回可直接放进 <img src> 的 data URI。
//
// 用内联 data URI 而不是另开一个 /qr?token=... 端点：令牌单页面一打开
// 就会发起几十上百个请求，每个 URL 里都带着令牌值，这些值会留在浏览器
// 历史与访问日志里。内联则整页自包含，另存为单文件也不会散架。
func DataURI(content string) (string, error) {
	b, err := PNG(content, printPx)
	if err != nil {
		return "", err
	}
	var sb bytes.Buffer
	sb.WriteString("data:image/png;base64,")
	sb.WriteString(base64.StdEncoding.EncodeToString(b))
	return sb.String(), nil
}
