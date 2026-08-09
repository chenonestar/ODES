// Command buildcss 构建管理端与作答端的 CSS。
//
//	go run ./tools/buildcss
//
// 只有**修改样式或模板**的人需要跑；仓库里已提交构建产物 web/dist/*.css，
// 拿到代码直接 go run ./cmd/eval 即可，不需要工具链、不需要联网。
//
// 为什么用 Go 而不是 shell 脚本：Windows 上跑 .sh 要装 Git Bash，还要处理
// MINGW 与 Windows 的路径转换；补一份 .ps1 又会让同一套逻辑有两份实现、
// 早晚漂移。而任何要改样式的人机器上一定有 Go——这是个 Go 项目。
// 这也正是 LLD 12.7.4 否掉 acme.sh 时讲的那条原则：能用原生工具时不叠兼容层。
//
// 全程零 Node、零 npm：
//   - Tailwind 用官方 standalone CLI（自包含二进制，锁版本 + 校验 SHA-256
//     后按需取回，不入库）
//   - daisyUI 5 以纯 CSS 分发，vendored 在 web/vendor/
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// tailwindVersion 与 checksums 一同构成可复现的锁定。
// 升级时两者必须同步替换——校验和取自官方发布的 sha256sums.txt。
const tailwindVersion = "v4.3.3"

const releaseBase = "https://github.com/tailwindlabs/tailwindcss/releases/download/"

// checksums 来自 ${releaseBase}${tailwindVersion}/sha256sums.txt。
//
// 锁了版本却不校验等于没锁：下载源被替换时必须能发现。
var checksums = map[string]string{
	"tailwindcss-linux-x64":        "dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a",
	"tailwindcss-linux-x64-musl":   "a04d34ceacc8f52cbe8920ad846cdeb61d3d0021dba32db0d1f77c9d9fad7a6c",
	"tailwindcss-linux-arm64":      "55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195",
	"tailwindcss-linux-arm64-musl": "71ea4be79c9de9827545682df3e040053fb535d37c71ed2cfdedf9385a0868e0",
	"tailwindcss-macos-arm64":      "cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d",
	"tailwindcss-macos-x64":        "7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1",
	"tailwindcss-windows-x64.exe":  "e0e260ce048014e9268f6237ff18f8ccf02cef521cbd0ae04e82c2cdf7aa3955",
}

// evalCSSBudget 是作答端 CSS 的体积上限（HLD 9.3）。
//
// 这份 CSS 要下发到 200 部手机，超预算直接构建失败——把约束放在构建里，
// 比写在文档里等人自觉遵守可靠。
const evalCSSBudget = 20 * 1024

func main() {
	root, err := moduleRoot()
	if err != nil {
		fatal("%v", err)
	}
	if err := os.Chdir(root); err != nil {
		fatal("切换到模块根目录失败: %v", err)
	}

	cli, err := ensureCLI()
	if err != nil {
		fatal("%v", err)
	}

	fmt.Println("构建管理端 CSS（Tailwind + 完整 daisyUI）…")
	if err := run(cli, "-i", "web/src/admin.css", "-o", "web/dist/admin.css", "--minify"); err != nil {
		fatal("构建管理端 CSS 失败: %v", err)
	}

	fmt.Println("构建作答端 CSS（仅 Tailwind，受体积预算约束）…")
	if err := run(cli, "-i", "web/src/eval.css", "-o", "web/dist/eval.css", "--minify"); err != nil {
		fatal("构建作答端 CSS 失败: %v", err)
	}

	adminSize := mustSize("web/dist/admin.css")
	evalSize := mustSize("web/dist/eval.css")
	fmt.Println()
	fmt.Printf("  管理端 web/dist/admin.css  %d 字节\n", adminSize)
	fmt.Printf("  作答端 web/dist/eval.css   %d 字节（预算 %d）\n", evalSize, evalCSSBudget)

	if evalSize > evalCSSBudget {
		fatal("作答端 CSS 超出 HLD 9.3 的 %d 字节预算（当前 %d）。\n"+
			"这份 CSS 要下发到 200 部手机，请精简后重试。", evalCSSBudget, evalSize)
	}
	fmt.Println("构建完成。")
}

// ensureCLI 返回可用的 Tailwind CLI 路径，必要时下载并校验。
func ensureCLI() (string, error) {
	asset, err := assetName()
	if err != nil {
		return "", err
	}
	want, ok := checksums[asset]
	if !ok {
		return "", fmt.Errorf("checksums 中没有 %s 的记录，请补充后重试", asset)
	}

	path := filepath.Join("tools", ".tailwindcss")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}

	// 已有且校验通过就复用，不重复下载
	if sum, err := fileSHA256(path); err == nil && sum == want {
		return path, nil
	}

	fmt.Printf("下载 Tailwind CLI %s（%s，约 100MB，仅首次需要）…\n", tailwindVersion, asset)
	url := releaseBase + tailwindVersion + "/" + asset
	tmp := path + ".tmp"
	if err := download(url, tmp); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("下载失败: %w\n\n"+
			"本步骤需要联网——只在办公室的开发机上跑，现场不需要。\n"+
			"也可手动下载 %s，另存为 %s 并加可执行权限。", err, url, path)
	}

	got, err := fileSHA256(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if got != want {
		os.Remove(tmp)
		return "", fmt.Errorf("SHA-256 校验失败，已丢弃下载内容：\n  期望 %s\n  实际 %s", want, got)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return "", err
	}
	fmt.Printf("校验通过：%s\n", asset)
	return path, nil
}

// assetName 按当前平台选择发布资源。
//
// 用 runtime.GOOS/GOARCH 而不是 uname：不依赖外部命令，Windows 上也准确。
func assetName() (string, error) {
	switch runtime.GOOS {
	case "linux":
		arch := "x64"
		if runtime.GOARCH == "arm64" {
			arch = "arm64"
		}
		name := "tailwindcss-linux-" + arch
		if isMusl() {
			// musl 系（Alpine 等）用 glibc 变体会因动态链接对不上而无法运行
			name += "-musl"
		}
		return name, nil
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "tailwindcss-macos-arm64", nil
		}
		return "tailwindcss-macos-x64", nil
	case "windows":
		return "tailwindcss-windows-x64.exe", nil
	}
	return "", fmt.Errorf("暂不支持的平台 %s/%s；请手动下载对应二进制放到 tools/.tailwindcss",
		runtime.GOOS, runtime.GOARCH)
}

func isMusl() bool {
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		return true
	}
	for _, p := range []string{"/lib/ld-musl-x86_64.so.1", "/lib/ld-musl-aarch64.so.1"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func download(url, dst string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func run(bin string, args ...string) error {
	abs, err := filepath.Abs(bin)
	if err != nil {
		return err
	}
	cmd := exec.Command(abs, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// moduleRoot 向上找 go.mod，使脚本从任意子目录调用都能工作。
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 go.mod：请在 eval/ 目录内运行 go run ./tools/buildcss")
		}
		dir = parent
	}
}

func mustSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		fatal("读取 %s 失败: %v", path, err)
	}
	return fi.Size()
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "\n错误：%s\n\n", fmt.Sprintf(format, a...))
	os.Exit(1)
}
