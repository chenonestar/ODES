// checkfont 检查一份字体能否用于 PDF 报告。
//
// 存在的理由很实际：gopdf 只认 glyf 轮廓，而中文字库最常见的发布格式
// （思源宋体、Noto Serif CJK 的 .otf）是 CFF 轮廓。两者都叫"字体文件"、
// 都能在系统里正常预览，但后者放进来会在导报告时才失败——而那通常是
// 散会后要交材料的时候。宁可在放文件的当下就报出来。
package main

import (
	"encoding/binary"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: checkfont <字体文件>")
		os.Exit(2)
	}
	b, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取失败:", err)
		os.Exit(1)
	}
	tables, err := sfntTables(b)
	if err != nil {
		fmt.Fprintln(os.Stderr, "不是可识别的字体文件:", err)
		os.Exit(1)
	}

	fmt.Printf("文件      %s（%.1f MB）\n", os.Args[1], float64(len(b))/(1<<20))
	fmt.Printf("表        %d 张\n", len(tables))

	_, hasGlyf := tables["glyf"]
	_, hasLoca := tables["loca"]
	_, hasCFF := tables["CFF "]
	_, hasCFF2 := tables["CFF2"]

	switch {
	case hasGlyf && hasLoca:
		fmt.Println("轮廓      glyf（TrueType）　✓ gopdf 可用")
	case hasCFF || hasCFF2:
		fmt.Println("轮廓      CFF（PostScript）　✗ gopdf 不支持")
		fmt.Println()
		fmt.Println("思源宋体与 Noto Serif CJK 的 .otf 都是这一类。需要换成")
		fmt.Println("glyf 轮廓的 .ttf 版本，或改用支持 CFF 的 PDF 库。")
		os.Exit(1)
	default:
		fmt.Println("轮廓      未知　✗ 既没有 glyf/loca，也没有 CFF")
		os.Exit(1)
	}
	fmt.Println()
	fmt.Println("格式检查通过。字形覆盖请用实际数据导一次报告确认——")
	fmt.Println("尤其是测评对象姓名里的生僻字。")
}

func sfntTables(b []byte) (map[string][2]uint32, error) {
	if len(b) < 12 {
		return nil, fmt.Errorf("文件过短")
	}
	tag := binary.BigEndian.Uint32(b[0:4])
	// ttcf: 字体集合，取第一个子字体
	if tag == 0x74746366 {
		if len(b) < 16 {
			return nil, fmt.Errorf("ttc 头部不完整")
		}
		off := binary.BigEndian.Uint32(b[12:16])
		if int(off)+12 > len(b) {
			return nil, fmt.Errorf("ttc 子字体偏移越界")
		}
		b = b[off:]
	}
	n := int(binary.BigEndian.Uint16(b[4:6]))
	if 12+n*16 > len(b) {
		return nil, fmt.Errorf("表目录越界")
	}
	out := map[string][2]uint32{}
	for i := 0; i < n; i++ {
		e := b[12+i*16 : 12+(i+1)*16]
		out[string(e[0:4])] = [2]uint32{
			binary.BigEndian.Uint32(e[8:12]), binary.BigEndian.Uint32(e[12:16]),
		}
	}
	return out, nil
}
