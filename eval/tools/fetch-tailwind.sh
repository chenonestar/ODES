#!/bin/sh
# 取回 Tailwind CSS standalone CLI（自包含二进制，运行不需要 Node/npm）。
#
# 二进制**不入库**：它有 100 MB 量级，且只有改样式的人才需要。仓库里锁的是
# 版本号与官方 SHA-256，取回后逐字节校验，因此结果是可复现的。
#
# 与 CON-02 的关系：现场全程离线约束的是**运行**，不是构建。SRS 2.1 明确
# "办公室（出发前）可以且应当联网——用于更新证书、更新程序、维护装备"。
# 本脚本只在办公室的开发机上跑。
set -e
cd "$(dirname "$0")"

VERSION=v4.3.3
BASE="https://github.com/tailwindlabs/tailwindcss/releases/download/${VERSION}"
OUT=.tailwindcss

# 官方 sha256sums.txt（${VERSION}）。升级版本时整块替换。
sum_for() {
  case "$1" in
    tailwindcss-linux-x64)        echo dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a ;;
    tailwindcss-linux-arm64)      echo 55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195 ;;
    tailwindcss-linux-x64-musl)   echo a04d34ceacc8f52cbe8920ad846cdeb61d3d0021dba32db0d1f77c9d9fad7a6c ;;
    tailwindcss-linux-arm64-musl) echo 71ea4be79c9de9827545682df3e040053fb535d37c71ed2cfdedf9385a0868e0 ;;
    tailwindcss-macos-arm64)      echo cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d ;;
    tailwindcss-macos-x64)        echo 7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1 ;;
    tailwindcss-windows-x64.exe)  echo e0e260ce048014e9268f6237ff18f8ccf02cef521cbd0ae04e82c2cdf7aa3955 ;;
    *) echo "" ;;
  esac
}

case "$(uname -s)" in
  Linux)
    case "$(uname -m)" in
      aarch64|arm64) ASSET=tailwindcss-linux-arm64 ;;
      *)             ASSET=tailwindcss-linux-x64 ;;
    esac
    # musl 系（Alpine 等）要用 -musl 变体，否则动态链接对不上
    if [ -f /etc/alpine-release ] || (ldd --version 2>&1 | grep -qi musl); then
      ASSET="${ASSET}-musl"
    fi
    ;;
  Darwin)
    case "$(uname -m)" in
      arm64) ASSET=tailwindcss-macos-arm64 ;;
      *)     ASSET=tailwindcss-macos-x64 ;;
    esac
    ;;
  MINGW*|MSYS*|CYGWIN*)
    ASSET=tailwindcss-windows-x64.exe
    OUT=.tailwindcss.exe
    ;;
  *)
    echo "无法识别的平台 $(uname -s)。请手动下载 ${BASE}/ 下对应的二进制，" >&2
    echo "改名为 tools/${OUT} 并加上可执行权限。" >&2
    exit 1
    ;;
esac

WANT=$(sum_for "$ASSET")
if [ -z "$WANT" ]; then
  echo "脚本里没有 $ASSET 的校验和，请补充后重试。" >&2
  exit 1
fi

# 已有且校验通过就直接复用，不重复下载
if [ -f "$OUT" ] && command -v sha256sum >/dev/null 2>&1; then
  if [ "$(sha256sum "$OUT" | cut -d' ' -f1)" = "$WANT" ]; then
    exit 0
  fi
fi

echo "下载 Tailwind CLI ${VERSION} (${ASSET})，约 100MB，仅首次需要…"
if ! curl -fsSL --retry 3 -o "$OUT.tmp" "${BASE}/${ASSET}"; then
  echo >&2
  echo "下载失败。本脚本需要联网（只在办公室开发机上跑，现场不需要）。" >&2
  echo "也可手动下载 ${BASE}/${ASSET}，改名为 tools/${OUT}。" >&2
  rm -f "$OUT.tmp"
  exit 1
fi

# 校验：锁版本而不校验等于没锁——下载源被替换时必须能发现
if command -v sha256sum >/dev/null 2>&1; then GOT=$(sha256sum "$OUT.tmp" | cut -d' ' -f1)
elif command -v shasum   >/dev/null 2>&1; then GOT=$(shasum -a 256 "$OUT.tmp" | cut -d' ' -f1)
else
  echo "找不到 sha256sum / shasum，无法校验完整性，拒绝使用。" >&2
  rm -f "$OUT.tmp"; exit 1
fi
if [ "$GOT" != "$WANT" ]; then
  echo "SHA-256 校验失败，已丢弃下载内容。" >&2
  echo "  期望 $WANT" >&2
  echo "  实际 $GOT" >&2
  rm -f "$OUT.tmp"; exit 1
fi

mv "$OUT.tmp" "$OUT"
chmod +x "$OUT"
echo "校验通过：$ASSET"
