package main

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

// ── 证书装载与启动校验 ────────────────────────────────────────────────
//
// 对应 LLD 第 12.4 节 CK-01 ~ CK-05。程序**只装载不申请**：证书由信息化
// 岗位用外部工具（certbot / acme.sh / 商业 CA）签发后放入程序目录。
//
// 其中 CK-02（SAN 是否覆盖配置的域名）是最容易被忽略、后果最严重的一项：
// 证书本身有效、也没过期，但签发时用的是另一个主机名 —— 现场每部手机
// 照样红屏。必须在办公室就拦住。

type certCheck struct {
	Code string
	Name string
	OK   bool
	Msg  string
}

// inspectCert 执行 CK-01 ~ CK-05，返回逐项结果。
func inspectCert(certFile, keyFile, domain string) (tls.Certificate, []certCheck) {
	var out []certCheck
	add := func(code, name string, ok bool, msg string) {
		out = append(out, certCheck{code, name, ok, msg})
	}

	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		add("CK-01", "证书文件可读、可解析", false, fmt.Sprintf("%v", err))
		return tls.Certificate{}, out
	}
	add("CK-01", "证书文件可读、可解析", true, certFile)

	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		add("CK-02", "SAN 覆盖配置域名", false, "证书解析失败")
		return pair, out
	}
	pair.Leaf = leaf

	// CK-02：最关键的一条
	if err := leaf.VerifyHostname(domain); err != nil {
		add("CK-02", "SAN 覆盖配置域名", false,
			fmt.Sprintf("证书未包含域名 %s（证书实际覆盖 %v），手机将出现安全告警",
				domain, sanList(leaf)))
	} else {
		add("CK-02", "SAN 覆盖配置域名", true, domain)
	}

	now := time.Now()
	switch {
	case now.Before(leaf.NotBefore):
		add("CK-03", "在有效期内", false,
			fmt.Sprintf("证书尚未生效，生效时间 %s", leaf.NotBefore.Format("2006-01-02")))
	case now.After(leaf.NotAfter):
		add("CK-03", "在有效期内", false,
			fmt.Sprintf("证书已于 %s 过期", leaf.NotAfter.Format("2006-01-02")))
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

	// CK-05：LoadX509KeyPair 已校验配对，此处显式记录以对齐 LLD 检查表
	add("CK-05", "私钥与证书匹配", true, "")
	return pair, out
}

func sanList(c *x509.Certificate) []string {
	names := append([]string{}, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		names = append(names, ip.String())
	}
	if len(names) == 0 && c.Subject.CommonName != "" {
		names = append(names, c.Subject.CommonName+"(CN)")
	}
	return names
}

// loadTLS 装载证书。未提供证书文件时退回自签名，仅供开发与连通性验证。
func loadTLS(certFile, keyFile, domain string) (*tls.Config, bool, []certCheck, error) {
	if certFile != "" && keyFile != "" {
		pair, checks := inspectCert(certFile, keyFile, domain)
		if len(pair.Certificate) == 0 {
			return nil, false, checks, fmt.Errorf("证书装载失败，见上方 CK-01")
		}
		return &tls.Config{
			Certificates: []tls.Certificate{pair},
			MinVersion:   tls.VersionTLS12,
		}, false, checks, nil
	}
	cert, err := selfSignedCert(domain)
	if err != nil {
		return nil, true, nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, true, nil, nil
}

// selfSignedCert 仅用于开发与"链路是否通"的初步验证。
//
// 手机上会出现全屏安全告警，这是预期行为。正式测评必须使用由公共 CA 为
// **考察部门自有域名**签发的真实证书：数百部手机逐一点击"继续访问"不具
// 可行性，且会严重损害参评人员对系统的信任（NFR-SEC-010）。
//
// 注意：曾设想用 nip.io 一类公共通配 DNS 服务把私有 IP 包装成公网域名后
// 向 Let's Encrypt 申请证书，该路径不成立 —— HTTP-01 / TLS-ALPN-01 要求
// CA 从公网访问（解析到私有 IP，够不着），DNS-01 要求在该域名下写 TXT
// 记录（第三方服务，无写入权限）。三种验证方式全部堵死，故自有域名是
// 硬性前提而非可选优化。
func selfSignedCert(domain string) (tls.Certificate, error) {
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
		Subject:               pkix.Name{CommonName: domain, Organization: []string{"spike-netcheck"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, 30),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{domain},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: &tmpl}, nil
}
