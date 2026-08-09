// Package config 读取 config.toml（LLD 12.2）。
//
// 为什么配置化（LLD 12.1）：域名、网段、证书路径原先散落在代码常量里，
// 使「域名归属」这件事成了编码阻塞项。改为配置驱动后，"证书从哪来"
// 被移到程序之外——程序只负责装载与校验，不负责申请。
//
// 这里手写了一个 TOML 子集解析器（约 80 行），只支持本配置实际用到的
// 语法：节、键值、字符串、整数、布尔、注释。引入完整 TOML 库不值得——
// 多一个依赖，而需求只有这么多。
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Network  Network
	Domain   Domain
	TLS      TLS
	Session  Session
	Snapshot Snapshot
}

type Network struct {
	ListenIP   string
	PoolStart  string
	PoolEnd    string
	HTTPSPort  int
	EnableDHCP bool
	EnableDNS  bool
}

type Domain struct {
	Primary    string
	Alternate  string
	ShortEntry string
}

type TLS struct {
	Mode string // file | selfsigned
	Cert string
	Key  string
}

type Session struct {
	TimeoutMin int
}

type Snapshot struct {
	Dir         string
	IntervalMin int
}

// Default 是文件不存在时的内置默认值。
func Default() Config {
	return Config{
		Network: Network{
			ListenIP: "192.168.66.1", PoolStart: "192.168.66.50",
			PoolEnd: "192.168.66.250", HTTPSPort: 8443,
			EnableDHCP: true, EnableDNS: true,
		},
		Domain: Domain{
			Primary: "eval.example.gov.cn", ShortEntry: "e.example.gov.cn",
		},
		TLS:      TLS{Mode: "selfsigned", Cert: "cert/fullchain.pem", Key: "cert/privkey.pem"},
		Session:  Session{TimeoutMin: 30},
		Snapshot: Snapshot{Dir: "snapshot", IntervalMin: 5},
	}
}

// DevDefault 是开发机默认值：不碰 53/67 端口、只绑回环、用自签名证书。
//
// 开发调试与现场部署的差别只在这几项上，其余行为完全一致——
// 不做"开发模式跳过校验"之类的特殊处理，否则调试通过不代表现场能用。
func DevDefault() Config {
	c := Default()
	c.Network.ListenIP = "127.0.0.1"
	c.Network.EnableDHCP = false
	c.Network.EnableDNS = false
	c.Domain.Primary = "localhost"
	c.Domain.ShortEntry = "localhost"
	c.TLS.Mode = "selfsigned"
	return c
}

// Load 读取配置文件。文件不存在时返回默认值并说明。
func Load(path string) (Config, bool, error) {
	c := Default()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	defer f.Close()

	section := ""
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if i := strings.Index(s, "#"); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			section = strings.ToLower(strings.Trim(s, "[]"))
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return c, true, fmt.Errorf("%s 第 %d 行无法解析: %s", path, line, s)
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if err := c.set(section, k, v); err != nil {
			return c, true, fmt.Errorf("%s 第 %d 行: %w", path, line, err)
		}
	}
	return c, true, sc.Err()
}

func (c *Config) set(section, k, v string) error {
	atoi := func() (int, error) { return strconv.Atoi(v) }
	switch section + "." + k {
	case "network.listen_ip":
		c.Network.ListenIP = v
	case "network.pool_start":
		c.Network.PoolStart = v
	case "network.pool_end":
		c.Network.PoolEnd = v
	case "network.https_port":
		n, err := atoi()
		if err != nil {
			return err
		}
		c.Network.HTTPSPort = n
	case "network.enable_dhcp":
		c.Network.EnableDHCP = v == "true"
	case "network.enable_dns":
		c.Network.EnableDNS = v == "true"
	case "domain.primary":
		c.Domain.Primary = v
	case "domain.alternate":
		c.Domain.Alternate = v
	case "domain.short_entry":
		c.Domain.ShortEntry = v
	case "tls.mode":
		c.TLS.Mode = v
	case "tls.cert":
		c.TLS.Cert = v
	case "tls.key":
		c.TLS.Key = v
	case "session.timeout_min":
		n, err := atoi()
		if err != nil {
			return err
		}
		c.Session.TimeoutMin = n
	case "snapshot.dir":
		c.Snapshot.Dir = v
	case "snapshot.interval_min":
		n, err := atoi()
		if err != nil {
			return err
		}
		c.Snapshot.IntervalMin = n
	default:
		// 未知键不报错：配置文件可能来自更新的版本。
	}
	return nil
}
