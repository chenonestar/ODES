# 报告字体

正式统计报告（PDF）需要一份**完整覆盖的中文字库**，按 LLD 13.1 / ADR-009
不做子集化——理由见 13.3：干部姓名里有生僻字，子集化的字库会在最不该
出错的地方印出方框。

字体文件**不入库**（约 20MB，且授权与来源应由使用单位自行确认），
请放到本目录下并命名为 `report.ttf`：

```
eval/web/fonts/report.ttf
```

缺字体时：PDF 导出会给出明确提示并拒绝出报告，HTML / xlsx / CSV
三个出口不受影响，照常可用。

## 格式要求：必须是 glyf 轮廓的 TrueType

gopdf 通过 `glyf` + `loca` 两张表取字形（见其 `pdf_dictionary_obj.go`），
**不支持 CFF 轮廓**。而 Adobe 思源宋体、Google Noto Serif CJK 发布的
`.otf` 都是 CFF 轮廓，直接放进来会加载失败。

放进来之前先自查：

```
go run ./tools/checkfont web/fonts/report.ttf
```

它会报告这个文件是 glyf 还是 CFF、以及常用汉字的覆盖情况。
