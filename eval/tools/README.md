# tools

## Tailwind CSS standalone CLI

官方 standalone CLI，**自包含可执行文件，运行不需要 Node 或 npm**。

**二进制不入库。** `fetch-tailwind.sh` 锁定版本 `v4.3.3`，按当前平台取回对应
二进制并逐字节校验 SHA-256（校验和取自官方发布的 `sha256sums.txt`）。
`build-css.sh` 在找不到 CLI 时会自动调用它。取回的文件落在 `tools/.tailwindcss`，
已加入 `.gitignore`。

为什么不入库：单个二进制约 100 MB（超过 GitHub 单文件 100 MiB 硬上限，
压缩后仍有 27 MB），而它**只有改样式的人才需要**——仓库里已提交构建产物
`web/dist/*.css`，拿到代码直接 `go run` 即可。

支持的平台（校验和均已锁定，无需逐平台决定入库）：

| 平台 | 资源名 |
|---|---|
| Linux x64 / arm64（含 musl） | `tailwindcss-linux-*` |
| macOS arm64 / x64 | `tailwindcss-macos-*` |
| Windows x64 | `tailwindcss-windows-x64.exe` |

升级版本时：改 `fetch-tailwind.sh` 里的 `VERSION`，并用新版本的
`sha256sums.txt` 整块替换 `sum_for()`，然后重跑 `build-css.sh` 并提交新产物。

### 与 CON-02 全程离线的关系

现场离线约束的是**运行**，不是构建。SRS 2.1 明确区分了两个时点：
"办公室（出发前）可以且应当联网——用于更新证书、更新程序、维护装备；
考察现场全程离线"。本脚本只在办公室的开发机上跑，产物随二进制走。

## 什么时候需要跑构建

只有改动**样式或模板**时才需要。`web/dist/` 下的构建产物已提交进仓库，
拿到代码直接 `go run ./cmd/eval -dev -seed` 即可，不需要任何工具链，也不需要联网。
