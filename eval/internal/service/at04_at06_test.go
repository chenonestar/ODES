package service

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"odes/internal/anon"
	"odes/internal/model"
)

// ── AT-04 日志洁净 / AT-06 擦除无残留（LLD 11.1）────────────────────
//
// 这两条都是"跑完整流程再扫盘"的验证，放在一起共用同一套夹具。

// at04Secrets 是一次完整流程里产生的、绝不能出现在日志里的东西。
type at04Secrets struct {
	tokens   []string // 令牌值：入场凭证，泄露即可代答
	comments []string // 作答内容：泄露即匿名性归零
}

// runFullFlow 跑一遍现场会发生的全部动作，返回途中产生的敏感值。
func runFullFlow(t *testing.T, s *Service) (projectID anon.ID, sec at04Secrets) {
	t.Helper()
	ctx := context.Background()

	pid, toks := fixture(t, s, 6)
	sec.tokens = toks

	// 打印令牌单、试填、提交、截止——现场的完整动作序列
	if _, err := s.PrintSheets(pid); err != nil {
		t.Fatal(err)
	}
	for i, tv := range toks[:4] {
		a := gradeAnswer(t, s, pid, i%4)
		comment := "评语特征串" + string(rune('A'+i)) + "至关重要勿泄露"
		sec.comments = append(sec.comments, comment)
		// 把评语挂到第一项上，确保它真的进了密文
		if len(a.Items) > 0 {
			a.Items[0].Text = comment
		}
		if err := s.Submit(ctx, tv, a); err != nil {
			t.Fatalf("第 %d 份提交失败: %v", i+1, err)
		}
	}
	if err := s.Transition(ctx, pid, model.StatusClosed); err != nil {
		t.Fatal(err)
	}
	return pid, sec
}

// AT-04：跑完整流程后扫描 op_log，不得出现令牌值、IP、MAC、作答内容。
//
// op_log 是**随项目长期保留**的——归档擦除后它仍在，正是最容易被忽略的
// 泄露面：擦得掉作答记录，却把令牌值留在了操作日志里。
func TestAT04_OpLogIsClean(t *testing.T) {
	s := newSvc(t)
	pid, sec := runFullFlow(t, s)

	logs, err := s.DB.OpLogs(pid, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("完整流程后 op_log 为空——扫描没有意义，先确认流程真的跑过")
	}

	var all strings.Builder
	for _, l := range logs {
		all.WriteString(l.Action + " " + l.Result + " " + l.Detail + "\n")
	}
	text := all.String()

	for _, tok := range sec.tokens {
		if strings.Contains(text, tok) {
			t.Errorf("op_log 出现令牌值 %s：令牌是入场凭证，泄露即可被代答", tok)
		}
		// 短码同样不能出现：它和令牌一一对应
		if len(tok) >= 8 && strings.Contains(text, tok[:8]) {
			t.Errorf("op_log 出现令牌前 8 位 %s", tok[:8])
		}
	}
	for _, c := range sec.comments {
		if strings.Contains(text, c) {
			t.Errorf("op_log 出现作答内容 %q", c)
		}
	}
	if m := regexp.MustCompile(
		`\b(\d{1,3}\.){3}\d{1,3}\b`).FindString(text); m != "" {
		t.Errorf("op_log 出现疑似 IP 地址 %s（NFR-ANO-040）", m)
	}
	if m := regexp.MustCompile(
		`(?i)\b([0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b`).FindString(text); m != "" {
		t.Errorf("op_log 出现疑似 MAC 地址 %s（NFR-ANO-040）", m)
	}

	t.Logf("AT-04：%d 条操作日志，未出现令牌值、作答内容、IP 或 MAC", len(logs))
	t.Logf("日志样例：%s", firstLines(text, 3))
}

// AT-06：擦除后二进制扫描数据文件，不得命中作答特征串。
//
// 这是 CON-06「测评结束后本机擦除」唯一的客观验证手段。擦除跑的是
// wipe.Run（VACUUM + 覆写空闲页 + 残留校验），但"跑过了"不等于"擦干净了"
// ——SQLite 的空闲页、WAL 残留、以及被删除记录留在页尾的字节都可能带着
// 明文特征。这条用例直接读文件字节找证据。
func TestAT06_NoResidueAfterWipe(t *testing.T) {
	s := newSvc(t)
	id, sec := runFullFlow(t, s)

	dbPath := s.DB.Path()
	if dbPath == "" {
		t.Skip("拿不到数据库路径")
	}

	// 擦除前必须能命中，否则"擦除后找不到"这个结论毫无意义——
	// 可能只是特征串从一开始就没写进去过。
	//
	// 注意作答内容是加密存储的，明文本就不该出现在文件里；
	// 这里用**令牌值**做哨兵：它以明文存在 token 表，擦除前必然命中。
	before := scanFileFor(t, dbPath, sec.tokens[0])
	if !before {
		t.Fatalf("擦除前在数据文件里都找不到令牌 %s：哨兵失效，本用例的结论不可信",
			sec.tokens[0])
	}

	blob, sum, err := s.BuildArchive(id, "Archive-Pass-9527")
	if err != nil {
		t.Fatal(err)
	}
	_ = blob
	if err := s.ArchiveAndWipe(context.Background(), id, "Archive-Pass-9527",
		strings.ToUpper(sum[:8])); err != nil {
		t.Fatalf("归档擦除失败: %v", err)
	}

	// 擦除后：令牌值、作答明文特征串都不得再命中
	var hits []string
	for _, tok := range sec.tokens {
		if scanFileFor(t, dbPath, tok) {
			hits = append(hits, "令牌 "+tok)
		}
	}
	for _, c := range sec.comments {
		if scanFileFor(t, dbPath, c) {
			hits = append(hits, "作答内容 "+c)
		}
	}
	// 同目录下的 WAL / SHM 也要扫：擦除若只处理主库文件，
	// 痕迹会留在 -wal 里，而那个文件同样会被一起拷走
	dir := filepath.Dir(dbPath)
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		for _, tok := range sec.tokens {
			if scanFileFor(t, p, tok) {
				hits = append(hits, p+" 含令牌 "+tok)
			}
		}
		return nil
	})

	if len(hits) > 0 {
		t.Fatalf("擦除后仍有残留（CON-06 / AT-06）：%v", hits)
	}
	t.Logf("AT-06：擦除前哨兵命中、擦除后 %d 个文件全部无残留", countFilesIn(t, dir))
}

// scanFileFor 在文件字节里找特征串。用字节匹配而不是按行读：
// 残留往往夹在二进制页数据中间，不构成完整的文本行。
func scanFileFor(t *testing.T, path, needle string) bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte(needle))
}

func countFilesIn(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " ｜ ")
}
