# tools

## 构建 CSS

```
go run ./tools/buildcss
```

三个平台命令完全一样，不需要 sh、不需要 PowerShell、不需要 Node 或 npm。
**只有改动样式或模板时才需要跑**——`web/dist/` 下的构建产物已提交进仓库并
embed 进二进制，拿到代码直接 `go run ./cmd/eval -dev -seed` 即可，
不需要工具链、不需要联网。

### 为什么是 Go 程序而不是 .sh

Windows 上跑 `.sh` 要装 Git Bash，还要处理 MINGW 与 Windows 的路径转换差异；
补一份 `.ps1` 又会让同一套逻辑有两份实现、早晚漂移。而任何要改样式的人机器上
一定有 Go——这是个 Go 项目。

这正是 LLD 12.7.4 否掉 acme.sh 时讲的那条原则：**能用原生工具时不叠兼容层。**

## Tailwind CSS standalone CLI

官方 standalone CLI，自包含可执行文件，**运行不需要 Node 或 npm**。

**二进制不入库。** `buildcss` 锁定版本 `v4.3.3`，按当前平台取回对应二进制并
校验 SHA-256（校验和取自官方发布的 `sha256sums.txt`）。取回的文件落在
`tools/.tailwindcss`（Windows 上为 `.tailwindcss.exe`），已在 `.gitignore` 中。
已有文件先校验再复用，不重复下载。

不入库的原因：单个二进制约 100 MB，超过 GitHub 单文件 100 MiB 硬上限
（压缩后仍有 27 MB），而它只有改样式的人才需要。

已锁定校验和的平台——无需逐平台决定入库：

| 平台 | 资源名 |
|---|---|
| Linux x64 / arm64（含 musl 变体） | `tailwindcss-linux-*` |
| macOS arm64 / x64 | `tailwindcss-macos-*` |
| Windows x64 | `tailwindcss-windows-x64.exe` |

升级版本：改 `tools/buildcss/main.go` 里的 `tailwindVersion`，用新版本的
`sha256sums.txt` 同步替换 `checksums`，重跑构建并提交新产物。两者必须同步——
锁了版本却不校验等于没锁。

### 与 CON-02 全程离线的关系

现场离线约束的是**运行**，不是构建。SRS 2.1 明确区分两个时点：
"办公室（出发前）可以且应当联网——用于更新证书、更新程序、维护装备；
考察现场全程离线"。取回 CLI 只在办公室的开发机上发生一次。
