// Package arch 存放架构约束的自动化检查。
//
// 这些检查对应 LLD A.1「CI 强制检查」，跑在 CI 里，任一失败即构建失败。
// 把它们写成测试而不是脚本，是为了本地 go test 就能跑到，不必等推上去。
package arch

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// layer 是包所处的层次，数字越小越底层。
//
// 依赖只允许从高层指向低层（HLD 第 4 章）：
//
//	admin / evalui / httpd → service → store / crypto / stats / anon
//
// 反向依赖一律禁止。这不是洁癖：一旦 store 反过来引用了 service，
// "唯一可以开启事务的层"这条约束就没了着落，而提交事务的正确性
// （ADR-004）正建立在它之上。
var layer = map[string]int{
	"anon":   0,
	"crypto": 0,
	"model":  0,
	"config": 0,
	"netsvc": 0,
	"wipe":   1,
	"store":  1,
	"stats":  1,
	"export": 2,
	"service": 3,
	"admin":  4,
	"evalui": 4,
	"httpd":  4,
	"arch":   9, // 本包，只在测试里存在
}

func TestDependencyDirection(t *testing.T) {
	root := internalDir(t)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pkg := e.Name()
		lvl, known := layer[pkg]
		if !known {
			t.Errorf("包 internal/%s 未在 layer 表中登记；新增包时请一并登记其层次", pkg)
			continue
		}
		for _, imp := range importsOf(t, filepath.Join(root, pkg)) {
			dep, ok := internalPkgOf(imp)
			if !ok {
				continue
			}
			depLvl, ok := layer[dep]
			if !ok {
				continue
			}
			if depLvl > lvl {
				t.Errorf("依赖方向违规：internal/%s（第 %d 层）导入了 internal/%s（第 %d 层）\n"+
					"依赖只允许从高层指向低层：admin/evalui/httpd → service → store/crypto/stats/anon",
					pkg, lvl, dep, depLvl)
			}
		}
	}
}

// TestShuffleBeforeOutput 检查"对外输出记录集合"的包确实调用了 anon.Shuffle。
//
// NFR-ANO-030 要求统计页、评语列表、原始数据导出中的记录顺序均为每次读取时
// 重新随机排列。HLD 5.4 把这条落成一个可检查点：凡是对外输出记录集合的
// 代码路径，必须能追溯到一次 Shuffle 调用。
//
// 这是个粗检查——它保证不了每条路径都调了，但能挡住"整个包忘了调"这种
// 级别的回归，而那正是最可能发生的。
func TestShuffleBeforeOutput(t *testing.T) {
	root := internalDir(t)
	for _, pkg := range []string{"stats", "export", "service"} {
		if !usesShuffle(t, filepath.Join(root, pkg)) {
			t.Errorf("internal/%s 没有任何 anon.Shuffle 调用：\n"+
				"该包会对外输出记录集合，NFR-ANO-030 要求输出前重排顺序", pkg)
		}
	}
}

// TestAnonZoneModelHasNoForbiddenFields 在**编译期类型**上守住匿名隔离区。
//
// 数据库侧的同一组约束由 store.Open 的启动断言与 AT-02 负责；这里守的是
// Go 结构体：字段一旦加上，即便还没建表也已经在代码里成立了。
func TestAnonZoneModelHasNoForbiddenFields(t *testing.T) {
	src := filepath.Join(internalDir(t), "model", "model.go")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)

	for _, spec := range []struct {
		typeName  string
		forbidden []string
	}{
		{"type Token struct", []string{"UsedAt", "CreatedAt", "Seq", "RosterID", "IP ", "UserAgent"}},
		{"type AnswerRecord struct", []string{"TokenID", "RosterID", "SubmittedAt", "Seq", "IP ", "IsPaperEntry"}},
	} {
		i := strings.Index(text, spec.typeName)
		if i < 0 {
			t.Fatalf("未找到 %s", spec.typeName)
		}
		j := strings.Index(text[i:], "\n}")
		if j < 0 {
			t.Fatalf("%s 结构体解析失败", spec.typeName)
		}
		body := text[i : i+j]
		for _, f := range spec.forbidden {
			if strings.Contains(body, f) {
				t.Errorf("%s 中出现被禁止的字段 %q：\n"+
					"匿名隔离区的两个结构体不得有时间、序号或指向对方的字段（LLD 2.2）",
					spec.typeName, strings.TrimSpace(f))
			}
		}
	}
}

// ── 工具 ────────────────────────────────────────────────────────────

func internalDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// 本测试位于 internal/arch，上一级即 internal
	return filepath.Dir(wd)
}

func importsOf(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", dir, err)
	}
	var out []string
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, imp := range f.Imports {
				out = append(out, strings.Trim(imp.Path.Value, `"`))
			}
		}
	}
	return out
}

func internalPkgOf(importPath string) (string, bool) {
	const prefix = "odes/internal/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(importPath, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest, true
}

func usesShuffle(t *testing.T, dir string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(b), "anon.Shuffle") {
			found = true
		}
		return nil
	})
	return found
}
