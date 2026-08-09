package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// ── 证书装载与启动校验（LLD 12.4，CK-01 ~ CK-05）────────────────────
//
// 程序**只装载不申请**：证书由信息化岗位用外部工具（win-acme / certbot /
// 商业 CA）为考察部门自有域名签发后放入程序目录。
//
// 否决"内置 ACME 客户端"的理由不是技术不可行——DNS-01 只需出站访问，
// 办公室联网时笔记本完全能跑通。真正的理由是 DNS API 密钥的价值远高于
// 证书本身（可为该域名下任意主机签发证书、劫持解析），而考察笔记本要被
// 带出、置于会场、可能离开视线（CON-08 / ADR-010）。

type Check struct {
	Code, Name, Msg string
	OK              bool
}

func (c Check) String() string {
	mark := "✓"
	if !c.OK {
		mark = "!!"
	}
	return fmt.Sprintf("%s %s %s　%s", mark, c.Code, c.Name, c.Msg)
}

// LoadTLS 装载证书并执行 CK-01~05。
// 第二个返回值表示是否为自签名（自签名时手机会告警，禁止用于正式测评）。
func LoadTLS(t TLS, domain, alternate string) (*tls.Config, bool, []Check, error) {
	if t.Mode == "selfsigned" || t.Cert == "" || t.Key == "" {
		cert, err := selfSigned(domain)
		if err != nil {
			return nil, true, nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert},
			MinVersion: tls.VersionTLS12}, true, nil, nil
	}

	pair, checks := inspect(t.Cert, t.Key, domain, alternate)
	if len(pair.Certificate) == 0 {
		return nil, false, checks, fmt.Errorf("证书装载失败，见 CK-01")
	}
	return &tls.Config{Certificates: []tls.Certificate{pair},
		MinVersion: tls.VersionTLS12}, false, checks, nil
}

func inspect(certFile, keyFile, domain, alternate string) (tls.Certificate, []Check) {
	var out []Check
	add := func(code, name string, ok bool, msg string) {
		out = append(out, Check{Code: code, Name: name, OK: ok, Msg: msg})
	}

	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		add("CK-01", "证书文件可读、可解析", false, err.Error())
		return tls.Certificate{}, out
	}
	add("CK-01", "证书文件可读、可解析", true, certFile)

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		add("CK-02", "SAN 覆盖配置域名", false, "证书解析失败")
		return pair, out
	}
	pair.Leaf = leaf

	// CK-02 是最容易被忽略、后果最严重的一项：证书本身有效、也没过期，
	// 但签发时用的是另一个主机名——现场每部手机照样红屏，且这种故障
	// 没有任何外部症状，只能靠启动自检在办公室拦住。
	names := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	if err := leaf.VerifyHostname(domain); err != nil {
		add("CK-02", "SAN 覆盖配置域名", false,
			fmt.Sprintf("证书未包含域名 %s（实际覆盖 %v），手机将出现安全告警", domain, names))
	} else if alternate != "" {
		if err := leaf.VerifyHostname(alternate); err != nil {
			add("CK-02", "SAN 覆盖配置域名", false,
				fmt.Sprintf("主用域名 %s 已覆盖，但备用域名 %s 未覆盖", domain, alternate))
		} else {
			add("CK-02", "SAN 覆盖配置域名", true, domain+" + "+alternate)
		}
	} else {
		add("CK-02", "SAN 覆盖配置域名", true, domain)
	}

	now := time.Now()
	switch {
	case now.Before(leaf.NotBefore):
		add("CK-03", "在有效期内", false,
			"证书尚未生效，生效时间 "+leaf.NotBefore.Format("2006-01-02"))
	case now.After(leaf.NotAfter):
		add("CK-03", "在有效期内", false,
			"证书已于 "+leaf.NotAfter.Format("2006-01-02")+" 过期")
	default:
		add("CK-03", "在有效期内", true, "至 "+leaf.NotAfter.Format("2006-01-02"))
	}

	days := int(time.Until(leaf.NotAfter).Hours() / 24)
	if days < 21 {
		add("CK-04", "剩余有效期 ≥21 天", false,
			fmt.Sprintf("将于 %d 天后过期，请在出发前重新签发并替换证书文件", days))
	} else {
		add("CK-04", "剩余有效期 ≥21 天", true, fmt.Sprintf("剩余 %d 天", days))
	}

	// LoadX509KeyPair 已校验配对，此处显式记录以对齐 LLD 检查表
	add("CK-05", "私钥与证书匹配", true, "")
	return pair, out
}

// selfSigned 仅用于开发与连通性验证。
//
// 手机上会出现全屏安全告警，这是预期行为。正式测评必须使用由公共 CA 为
// 考察部门自有域名签发的真实证书：数百部手机逐一点击"继续访问"不具
// 可行性，且会严重损害参评人员对系统的信任（NFR-SEC-010）。
//
// 曾设想借助 nip.io 一类公共通配 DNS 服务把私有 IP 包装成公网域名后向
// Let's Encrypt 申请证书，该路径不成立：HTTP-01 / TLS-ALPN-01 要求 CA
// 从公网访问（解析到私有 IP，够不着），DNS-01 要求在该域名下写 TXT 记录
// （第三方服务，无写入权限）。三种验证方式全部堵死，故自有域名是硬性
// 前提而非可选优化（ADR-008）。
func selfSigned(domain string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domain, Organization: []string{"ODES 开发自签名"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, 30),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{domain, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.66.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: &tmpl}, nil
}
