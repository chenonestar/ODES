package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func xlsxWith(t *testing.T, col []string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	for i, v := range col {
		if err := f.SetCellStr("Sheet1", cellName(i+1), v); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func cellName(row int) string {
	c, err := excelize.CoordinatesToCellName(1, row)
	if err != nil {
		panic(err)
	}
	return c
}

func TestParseRosterXLSX(t *testing.T) {
	b := xlsxWith(t, []string{"姓名", "张三", "李四", "", "王五"})
	got, err := ParseRoster("名单.xlsx", b)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"张三", "李四", "王五"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("得到 %v，应为 %v（表头与空行都要跳过）", got, want)
	}
}

func TestParseRosterCSVVariants(t *testing.T) {
	cases := []struct {
		name string
		file string
		data []byte
		want []string
	}{
		{"带 BOM 的 CSV", "a.csv",
			append([]byte{0xEF, 0xBB, 0xBF}, []byte("姓名\n张三\n李四\n")...),
			[]string{"张三", "李四"}},
		{"多列只取第一列", "b.csv",
			[]byte("张三,局长,组织部\n李四,副局长,组织部\n"),
			[]string{"张三", "李四"}},
		{"纯文本一行一个", "c.txt",
			[]byte("张三\n李四\n王五\n"),
			[]string{"张三", "李四", "王五"}},
		{"重复行去重", "d.csv",
			[]byte("张三\n张三\n李四\n"),
			[]string{"张三", "李四"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseRoster(c.file, c.data)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("得到 %v，应为 %v", got, c.want)
			}
		})
	}
}

// BOM 不去掉会让第一个人的名字前多出一个不可见字符，
// 之后无论怎么核对都对不上——而且肉眼完全看不出来。
func TestParseRosterStripsBOM(t *testing.T) {
	got, err := ParseRoster("a.csv", append([]byte{0xEF, 0xBB, 0xBF}, []byte("张三\n")...))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "张三" {
		t.Fatalf("首条为 %q（%v），BOM 未被去除", got[0], []byte(got[0]))
	}
}

func TestParseRosterRejectsNonUTF8(t *testing.T) {
	// GBK 编码的「张三」
	gbk := []byte{0xD5, 0xC5, 0xC8, 0xFD, '\n'}
	_, err := ParseRoster("a.csv", gbk)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("非 UTF-8 应给出可照做的提示，得到 %v", err)
	}
}

func TestParseRosterEmpty(t *testing.T) {
	if _, err := ParseRoster("a.csv", []byte("姓名\n\n\n")); !errors.Is(err, ErrRosterEmpty) {
		t.Fatalf("整表无有效行应返回 ErrRosterEmpty，得到 %v", err)
	}
}

// 导入是整表替换。增量合并会让"导错了再导一次"变成两份名单叠加，
// 而名单条数是用来核对应参加人数的——它是全部比率的分母。
func TestImportRosterReplacesInsteadOfAppending(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	id := newDraft(t, s)

	if err := s.ImportRoster(ctx, id, []string{"张三", "李四", "王五"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.DB.CountRoster(id); n != 3 {
		t.Fatalf("首次导入应为 3 条，得到 %d", n)
	}
	if err := s.ImportRoster(ctx, id, []string{"赵六", "钱七"}); err != nil {
		t.Fatal(err)
	}
	n, _ := s.DB.CountRoster(id)
	if n != 2 {
		t.Fatalf("再次导入应整表替换为 2 条，得到 %d（叠加会让分母静默出错）", n)
	}
	got, err := s.DB.Roster(s.Vault, id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "赵六,钱七" && strings.Join(got, ",") != "钱七,赵六" {
		t.Fatalf("名单内容应为第二次导入的那份，得到 %v", got)
	}
}

// FR-TKN-012：名单可在分发后删除，不影响系统运行与统计。
func TestClearRosterDoesNotAffectTokensOrStats(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	id, toks := fixture(t, s, 3)
	_ = s.DB.InsertRoster(s.Vault, id, "张三")

	if err := s.Submit(ctx, toks[0], gradeAnswer(t, s, id, 0)); err != nil {
		t.Fatal(err)
	}
	before, err := s.Stats(id)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.ClearRoster(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.DB.CountRoster(id); n != 0 {
		t.Fatalf("名单应已删除，仍有 %d 条", n)
	}
	total, _, _ := s.DB.CountTokens(id)
	if total != 3 {
		t.Fatalf("删除名单不应影响令牌，得到 %d 个", total)
	}
	s.invalidateStats(id)
	after, err := s.Stats(id)
	if err != nil {
		t.Fatalf("删除名单后统计应照常可算：%v", err)
	}
	if after.Submitted != before.Submitted || len(after.Cells) != len(before.Cells) {
		t.Fatal("删除名单改变了统计结果")
	}
}

// FR-TKN-011 的硬约束：名单表与令牌表之间不存在任何字段关联。
// 这条一旦被破坏（比如"顺手加个 token_id 方便核对"），谁拿到哪个令牌
// 就成了库里的明账，整套匿名性当场作废。
func TestRosterHasNoLinkToToken(t *testing.T) {
	s := newSvc(t)
	rows, err := s.DB.Query(`SELECT name FROM pragma_table_info('roster')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	for _, c := range cols {
		if strings.Contains(c, "token") || strings.Contains(c, "answer") {
			t.Errorf("roster 表出现了指向令牌/作答的列 %q：\n"+
				"名单与令牌之间不得存在任何字段关联（FR-TKN-011）", c)
		}
	}
	if len(cols) != 3 {
		t.Errorf("roster 应只有 id / project_id / label_enc 三列，实际为 %v", cols)
	}
}
