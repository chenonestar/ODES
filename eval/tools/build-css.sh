#!/bin/sh
# 构建管理端与作答端的 CSS，产物写入 web/dist/ 并由 go:embed 打进二进制。
#
# 只有**修改样式或模板**的人需要跑这个脚本；仓库里已提交构建产物，
# 拿到代码直接 go run 即可，不需要任何工具链。
#
# 全程零 Node、零 npm、零联网：
#   · Tailwind 用官方 standalone CLI（自包含二进制，仓库内 xz 压缩提交）
#   · daisyUI 5 以纯 CSS 分发，vendored 在 web/vendor/
set -e
cd "$(dirname "$0")/.."

CLI=tools/.tailwindcss
case "$(uname -s)" in
  Linux)  PACK=tools/tailwindcss-linux-x64.xz ;;
  Darwin) PACK=tools/tailwindcss-macos-arm64.xz ;;
  *)      PACK=tools/tailwindcss-windows-x64.exe.xz ;;
esac

if [ ! -x "$CLI" ]; then
  if [ ! -f "$PACK" ]; then
    echo "缺少 $PACK。" >&2
    echo "该平台的 Tailwind CLI 尚未入库，请见 tools/README.md 的取用说明。" >&2
    exit 1
  fi
  echo "解压 $PACK …"
  xz -dc "$PACK" > "$CLI"
  chmod +x "$CLI"
fi

echo "构建管理端 CSS（Tailwind + 完整 daisyUI）…"
"$CLI" -i web/src/admin.css -o web/dist/admin.css --minify

echo "构建作答端 CSS（仅 Tailwind，受 20KB 预算约束）…"
"$CLI" -i web/src/eval.css -o web/dist/eval.css --minify

EVAL_SIZE=$(wc -c < web/dist/eval.css)
echo
echo "  管理端 web/dist/admin.css  $(wc -c < web/dist/admin.css) 字节"
echo "  作答端 web/dist/eval.css   ${EVAL_SIZE} 字节"
if [ "$EVAL_SIZE" -gt 20480 ]; then
  echo >&2
  echo "作答端 CSS 超出 HLD 9.3 的 20KB 预算（${EVAL_SIZE} 字节）。" >&2
  echo "这份 CSS 要下发到 200 部手机，请精简后重试。" >&2
  exit 1
fi
echo "构建完成。"
