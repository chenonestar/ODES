# spike-netcheck —— 技术尖刺 ① 验证程序

验证「单个 Go 可执行文件同时承担 DHCP + DNS + 连通性探测应答 + HTTPS」这一方案
在 Windows / macOS / Linux 上是否可靠。这是需求书 `FR-NET-010 ~ 050` 的可行性前提，
也是 HLD 的阻塞项：若结论为否，"零配置"需从主路径降级为备选路径，
现场部署规程与产品叙事都要改写。

仅依赖标准库，无第三方模块。`internal/netsvc` 可直接演进为正式项目的同名包。

## 构建

```bash
go build -o netcheck .                                   # 本机
GOOS=windows GOARCH=amd64 go build -o netcheck.exe .     # 交叉编译到 Windows
GOOS=darwin  GOARCH=arm64 go build -o netcheck-mac .
```

## 运行

先把笔记本有线网卡设为静态 `192.168.66.1 / 255.255.255.0`，然后：

```bash
# Linux / macOS 需 root（绑定 53 / 67 / 80）
sudo ./netcheck

# Windows：以管理员身份运行，并在防火墙提示中放行
netcheck.exe

# 使用真实证书（正式验证必须用这种方式）
./netcheck -domain eval.xxx.gov.cn -cert cert/fullchain.pem -key cert/privkey.pem
```

常用参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `-ip` | `192.168.66.1` | 本机在测评网络中的静态地址 |
| `-domain` | `eval.example.gov.cn` | 证书域名，对应 `config.toml` 的 `domain.primary` |
| `-short-entry` | `e.example.gov.cn` | 短码入口域名，对应 `domain.short_entry` |
| `-pool-start` / `-pool-end` | `.50` / `.250` | DHCP 地址池，201 个，覆盖单批 200 人 |
| `-https-port` | `8443` | HTTPS 端口 |
| `-no-dhcp` | 关 | 关闭内置 DHCP，用于验证「由外置路由分配地址」的降级路径 |
| `-no-dns` | 关 | 关闭内置 DNS |

## 验证步骤

1. 按上文启动程序，确认自检行显示网卡地址正常、无 `!!` 行；
2. 把一台 AP 接入笔记本所在交换机（AP 模式、关 DHCP）；
3. 手机连接该 AP 的 WiFi；
4. 观察控制台是否出现 `DHCP ... → ACK 192.168.66.x`；
5. **重点观察手机是否提示"无法上网"、Android 是否自动切回移动数据**；
6. 手机浏览器打开 `https://{-domain 指定的域名}:8443/`；
7. `Ctrl+C` 结束，查看验证小结。

**必须覆盖的机型**：iOS、原生 Android，以及至少两台国产品牌（小米 / 华为 / OPPO / vivo）。
国产 ROM 各有自己的探测域名，只覆盖 gstatic 是不够的 —— 见 `internal/netsvc/probes.go`。

## 通过标准

- [ ] 三平台上 53 / 67 / 80 / 8443 均能绑定，且明确记录**是否需要管理员权限**
- [ ] 手机自动取得 `192.168.66.50–250` 的地址
- [ ] 手机全程不提示"无法上网"，Android 不切回移动数据
- [ ] 验证小结中 Android / Apple / Windows 三类探测计数均 > 0
- [ ] 用真实证书时手机浏览器无任何证书告警
- [ ] 证书自检 CK-01 ~ CK-05 全部通过
- [ ] **在装有企业杀软的机器上运行不被拦截或报毒**

最后一项风险最高：一个未签名的 exe 同时监听 53 和 67、还对
`connectivitycheck.gstatic.com` 返回自己的地址 —— 这个行为特征与
DNS 劫持类恶意软件高度相似。若确实报毒，处置路径是代码签名证书
（数千元/年）或加入白名单，两条都需要时间，必须早发现。

## 已完成的自动化验证

```
go test ./...
```

- DHCP：DISCOVER→OFFER→REQUEST→ACK 全流程；选项 1/3/6/51/53/54 正确；
  200 台连续分配无冲突；同一 MAC 地址稳定；地址池耗尽时拒绝而非重复分配
- DNS：证书域名 / 短码入口 / 各厂商探测域名解析正确；
  `dns.msftncsi.com` 返回 Windows 期望的真实值 `131.107.255.255`；
  未知域名 NXDOMAIN 且不转发；AAAA 查询返回 NOERROR + 空
- HTTP：Android 204 / Apple Success / Windows Connect Test 三类应答；
  非探测请求 301 跳转 HTTPS

## 证书自检（CK-01 ~ CK-05）

启动时对证书执行五项检查，对应 LLD 第 12.4 节。**其中 CK-02 最重要**：

```
[证书] ✓  CK-01 证书文件可读、可解析   cert/fullchain.pem
[证书] !! CK-02 SAN 覆盖配置域名      证书未包含域名 eval.xxx.gov.cn
                                    （证书实际覆盖 [wrong.gov.cn]），
                                     手机将出现安全告警
[证书] ✓  CK-03 在有效期内            至 2027-08-08
[证书] ✓  CK-04 剩余有效期 ≥21 天      剩余 364 天
[证书] ✓  CK-05 私钥与证书匹配
```

证书本身有效、也没过期，但签发时用的是另一个主机名 —— 现场每部手机
照样红屏。这种故障没有任何外部症状，只能靠启动自检在办公室拦住。

## 关于证书方案的说明

程序**只装载证书，不申请证书**。证书由信息化岗位用外部工具
（certbot / acme.sh / 商业 CA）为**考察部门自有域名**签发后放入程序目录。

曾设想借助 `nip.io` 一类公共通配 DNS 服务，把私有 IP 包装成
`192-168-66-1.nip.io` 形式的公网域名后向 Let's Encrypt 申请证书。
**该路径不成立**：

- HTTP-01 / TLS-ALPN-01 要求 CA 从公网访问该域名，而它解析到私有 IP；
- DNS-01 要求在该域名下创建 TXT 记录，而 nip.io 是第三方服务，
  只提供通配解析，不开放记录写入。

三种验证方式全部堵死，因此**自有域名是硬性前提而非可选优化**。
内置 DNS 相应地也删除了 `*.nip.io` 通配兜底 —— 未在配置中列出的域名
一律 NXDOMAIN，避免"随便什么域名都能解析到本机"掩盖配置错误。

## 关于匿名性的两处代码约束

1. **DHCP 租约表只在内存**（`dhcp.go`）。租约表含 MAC ↔ IP 映射，
   属可关联信息，持久化会破坏 `NFR-ANO-040`。
2. **不得记录 hostname**。手机主机名常含机主姓名拼音。
   本程序在控制台打印它仅供尖刺阶段观察，代码中已标注
   `正式版不得记录`，移植到正式项目时必须删除。
