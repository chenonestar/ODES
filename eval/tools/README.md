# tools

## Tailwind CSS standalone CLI

官方 standalone CLI，**自包含可执行文件，运行不需要 Node 或 npm**。
仓库内以 xz 压缩提交，`build-css.sh` 首次运行时自动解压到 `tools/.tailwindcss`。

为什么压缩：原始二进制 106.6 MiB，超过 GitHub **单文件 100 MiB 硬上限**，
直接提交会被拒绝 push。xz -9 后 27.1 MiB，可以正常入库。

| 文件 | 平台 | 版本 |
|---|---|---|
| `tailwindcss-linux-x64.xz` | Linux x64 | 4.3.3 |

需要其它平台时，从
`https://github.com/tailwindlabs/tailwindcss/releases` 下载对应的
`tailwindcss-macos-arm64` / `tailwindcss-windows-x64.exe`，
`xz -9` 压缩后按上表命名放入本目录，并更新 `web/vendor/VERSIONS.txt`。

## 什么时候需要跑构建

只有改动**样式或模板**时才需要。`web/dist/` 下的构建产物已提交进仓库，
拿到代码直接 `go run ./cmd/eval -dev -seed` 即可，不需要任何工具链。
