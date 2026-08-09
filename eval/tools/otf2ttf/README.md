# otf2ttf — 把 CFF 轮廓的中文字库转成 gopdf 能用的 glyf 轮廓

## 为什么需要这一步

gopdf 通过 `glyf` + `loca` 两张表取字形，**不支持 CFF 轮廓**。
而思源宋体（Source Han Serif）与 Noto Serif CJK —— 也就是 ADR-009 指定的
那一款 —— 官方只发布 CFF 轮廓的 `.otf` / `.ttc`，直接放进
`web/fonts/report.ttf` 会报「Unrecognized file (font) format」。

本脚本做一次性转换。**只在办公室机器上跑一次**，产物拷进
`eval/web/fonts/report.ttf` 即可；考察笔记本上不需要 Python，
也不需要任何字体工具。

## 许可

思源宋体 / Noto Serif CJK 以 **SIL OFL 1.1** 发布，明确允许修改与格式转换；
衍生版须继续以 OFL 分发、且不得单独售卖字体本身。把字体嵌入 PDF 文档
不会让文档受 OFL 约束 —— 这正是 OFL 相对其他自由字体许可的优势，
也是这里推荐它而不是 Arphic 系字体的原因。

## 用法

```bash
pip install fonttools
python3 otf2ttf.py <源文件> <ttc 内序号> <输出.ttf>
```

例（Debian/Ubuntu 的 fonts-noto-cjk 包）：

```bash
python3 otf2ttf.py /usr/share/fonts/opentype/noto/NotoSerifCJK-Regular.ttc 2 report.ttf
cp report.ttf ../../web/fonts/report.ttf
go run ../checkfont ../../web/fonts/report.ttf
```

`.ttc` 里 SC 通常是序号 2，脚本会打印实际取到的字体名，照着核对。
源文件若已是单体 `.otf`，序号填 0。

耗时约 2 分钟，产出约 30MB。

## 转换过程中三个必须处理的点

这三处任何一处漏掉，产出的文件在系统里预览完全正常，但 gopdf 用不了：

1. **必须建 `loca` 表。** 源 OTF 里没有这张表（CFF 不用它），而 glyf
   轮廓的字形偏移全靠它定位。缺了它 gopdf 报「table not found」。
2. **`post` 表要用 3.0 而不是 2.0。** CJK 字体有六万多个字形，
   format 2.0 的索引是 uint16，装不下。
3. **每个字形要重算包围盒。** `maxp` / `head` 的重算会读 `xMin` 等字段，
   而刚画出来的字形还没有这些值。
