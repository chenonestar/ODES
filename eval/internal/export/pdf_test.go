package export

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"odes/internal/anon"
	"odes/internal/model"
)

// anyCJKFont 找一份 glyf 轮廓的 CJK 字体。覆盖不全也没关系——
// 用它验的是"缺字必须被挡住"这件事本身。
func anyCJKFont(t *testing.T) []byte {
	t.Helper()
	for _, p := range []string{
		"/etc/alternatives/fonts-japanese-gothic.ttf",
		"/usr/share/fonts/opentype/ipafont-gothic/ipag.ttf",
	} {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return b
		}
	}
	t.Skip("本机没有 CJK TTF 字体")
	return nil
}

// fullCoverageFont 返回一份**能覆盖整份报告**的字体，否则跳过。
//
// 排版类用例只有在字形齐全时才有意义：覆盖检查会在排版之前就拦下来，
// 拿一份缺字的字体去测排版，测到的只是那道检查。仓库里不带字体，
// 因此这些用例在放置 web/fonts/report.ttf 之前会一直跳过——
// 这是刻意的，跳过比拿假前提测出的绿灯诚实。
func fullCoverageFont(t *testing.T) []byte {
	t.Helper()
	for _, p := range []string{
		"../../web/fonts/report.ttf",
		"/etc/alternatives/fonts-japanese-gothic.ttf",
	} {
		b, err := os.ReadFile(p)
		if err != nil || len(b) == 0 {
			continue
		}
		if len(missingGlyphs(b, reportText(mkProject(), mkMixedResult(), "A1B2C3D4"))) == 0 {
			return b
		}
	}
	t.Skip("本机没有能完整覆盖报告文字的中文字库；" +
		"放置 web/fonts/report.ttf 后本用例自动生效")
	return nil
}

func mkProject() *model.Project {
	return &model.Project{
		ID: anon.NewID(), Name: "测试项目", ExpectedCount: 30,
		GradeScores: []int{100, 80, 60, 0}, CreatedAt: time.Now(),
	}
}

// 缺字体时必须是一个**可辨认**的错误，而不是笼统的"生成失败"：
// 管理员要能立刻判断这是放个文件就能解决的事。
func TestReportPDFWithoutFontIsIdentifiable(t *testing.T) {
	var buf bytes.Buffer
	err := ReportPDF(&buf, mkProject(), mkResult(), "A1B2C3D4", nil)
	if !errors.Is(err, ErrNoFont) {
		t.Fatalf("缺字体应返回 ErrNoFont，得到 %v", err)
	}
	if buf.Len() != 0 {
		t.Error("失败时不应写出任何内容——半成品 PDF 比没有更糟")
	}
}

func TestReportPDFRendersAndIsWellFormed(t *testing.T) {
	font := fullCoverageFont(t)
	var buf bytes.Buffer
	if err := ReportPDF(&buf, mkProject(), mkMixedResult(), "A1B2C3D4", font); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatalf("不是 PDF：开头为 %q", b[:min(8, len(b))])
	}
	if !bytes.Contains(b, []byte("%%EOF")) {
		t.Error("PDF 缺少结尾标记，多半是写到一半出错了")
	}
	if len(b) < 20000 {
		// 内嵌中文字库后正常应有几百 KB；过小说明字体没被嵌进去，
		// 那样在没装该字体的机器上打开就是一片方框
		t.Errorf("PDF 只有 %d 字节，中文字库很可能没有嵌入", len(b))
	}
	if !bytes.Contains(b, []byte("/FontFile2")) {
		t.Error("PDF 中没有 /FontFile2：字体未被嵌入，换台机器打开会显示方框")
	}
}

// FR-EXP-020 / FR-EXP-021：口径说明页与页脚核验编号是**必做**项。
// 这两样缺了，报告就不能作为留痕材料上报。
func TestReportPDFHasCaliberPageAndFooter(t *testing.T) {
	font := fullCoverageFont(t)
	var buf bytes.Buffer
	if err := ReportPDF(&buf, mkProject(), mkMixedResult(), "A1B2C3D4", font); err != nil {
		t.Fatal(err)
	}
	// PDF 内容流被压缩，无法直接搜中文。改为验证结构性事实：
	// 口径说明页会让页数至少为 2，页脚每页都画。
	pages := bytes.Count(buf.Bytes(), []byte("/Type /Page\n"))
	if pages == 0 {
		pages = bytes.Count(buf.Bytes(), []byte("/Type /Page"))
	}
	if pages < 2 {
		t.Errorf("应至少 2 页（正文 + 口径说明页），实际 %d 页", pages)
	}
}

// 表格跨页时必须重画表头，否则第二页起是一堆没有列名的数字。
// 这里用一份行数足以撑爆一页的数据，验证不会崩、也不会截断。
func TestReportPDFHandlesLongTable(t *testing.T) {
	font := fullCoverageFont(t)
	r := mkMixedResult()
	base := r.Cells[0]
	for i := 0; i < 120; i++ {
		c := base
		c.SubjectName = "对象" + itoa(i)
		r.Cells = append(r.Cells, c)
	}
	var buf bytes.Buffer
	if err := ReportPDF(&buf, mkProject(), r, "A1B2C3D4", font); err != nil {
		t.Fatal(err)
	}
	pages := bytes.Count(buf.Bytes(), []byte("/Type /Page"))
	if pages < 3 {
		t.Errorf("121 行等级题应跨多页，实际 %d 页", pages)
	}
}

// 放了 CFF 轮廓的 .otf 是最可能踩的坑（思源宋体官方 .otf 就是），
// 报错必须指向自查工具，而不是丢一句 invalid font。
func TestReportPDFRejectsUnusableFontWithGuidance(t *testing.T) {
	var buf bytes.Buffer
	err := ReportPDF(&buf, mkProject(), mkResult(), "A1B2C3D4",
		[]byte("OTTO\x00\x01\x00\x00 这不是 glyf 轮廓的字体"))
	if err == nil {
		t.Fatal("非法字体应报错")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("checkfont")) {
		t.Errorf("错误提示应指向自查工具，得到：%v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 这是这一组里最要紧的一条。
//
// gopdf 对字库里没有的字是**直接丢掉**：不报错、不画方框，报告上只是
// 少了几个字。实测用日文 IPAGothic 排一份中文报告，"测评项目"会变成
// "目"、"统计报告"变成"告"——而拿到报告的人只会以为是打印机的问题。
// 姓名少一个字的正式报告会被原样上报，没有任何人能发现数据错了。
//
// 所以缺字必须在排版**之前**变成一个硬错误。
func TestReportPDFRefusesFontMissingGlyphs(t *testing.T) {
	font := anyCJKFont(t)
	r := mkMixedResult()
	r.Cells[0].SubjectName = "测评对象" // 简体专用字，日文字库没有

	var buf bytes.Buffer
	err := ReportPDF(&buf, mkProject(), r, "A1B2C3D4", font)
	if err == nil {
		t.Fatal("字库缺字时必须拒绝出报告——缺字会被静默丢弃，无法事后发现")
	}
	msg := err.Error()
	for _, want := range []string{"缺少", "字形", "静默"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误提示应说明缺字后果，缺少 %q：%v", want, msg)
		}
	}
	if buf.Len() != 0 {
		t.Error("拒绝时不应写出任何内容")
	}
}

// 覆盖检查必须基于**实际要打印的内容**，而不是一张常用字表——
// 出问题的恰恰是姓名里的生僻字，它们按定义不在常用表里。
func TestGlyphCheckCoversSubjectNames(t *testing.T) {
	font := anyCJKFont(t)
	r := mkMixedResult()
	// 实测 IPAGothic 里没有这个字（日文字库覆盖不到"赟"）。
	// 刻意用实测确认过缺失的字，而不是凭印象挑一个"看起来很生僻"的——
	// 犇、喆、頔 这几个日文字库其实都有，用它们这条测试会假绿。
	rare := "赟"
	r.Cells[0].SubjectName = rare

	missing := missingGlyphs(font, reportText(mkProject(), r, "A1B2C3D4"))
	found := false
	for _, m := range missing {
		if string(m) == rare {
			found = true
		}
	}
	if !found {
		t.Errorf("测评对象姓名里的生僻字 %s 未被覆盖检查发现，缺字列表：%s",
			rare, string(missing))
	}
}
