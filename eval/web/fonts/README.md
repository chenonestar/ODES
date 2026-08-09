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

## 三条格式要求

1. **glyf 轮廓的 TrueType**。gopdf 通过 `glyf` + `loca` 两张表取字形，
   不支持 CFF 轮廓。而思源宋体、Noto Serif CJK 官方发布的 `.otf` 都是
   CFF，直接放进来会加载失败。
2. **单体 `.ttf`，不能是 `.ttc` / `.otc`**。gopdf 不解析字体集合，
   实测 `NotoSerifCJK-Regular.ttc`、`uming.ttc` 都报
   「Unrecognized file (font) format」。
3. **完整覆盖，不做子集化**（LLD 13.3）。

放进来之前先自查：

```
go run ./tools/checkfont web/fonts/report.ttf
```

## 选哪一款：实测数据

用 `tools/checkfont` 与实际排版逐个测过的结果：

| 候选 | 轮廓 | GB2312 | CJK 基本区 | 扩展 A | 生僻姓名样本 | gopdf |
|---|---|---|---|---|---|---|
| **Noto Serif CJK SC（转 glyf 后）** | glyf | 6763/6763 | 100% | 100% | 54/54 | ✓ |
| BabelStone Han | glyf | 6763/6763 | 100% | 70.0% | 54/54 | ✓ |
| NotoSerifCJK-Regular.ttc（原版） | CFF | — | — | — | — | ✗ 集合 + CFF |
| AR PL UMing（uming.ttc） | — | — | — | — | — | ✗ 集合 |
| IPAGothic（日文） | glyf | 4146/6768 | 45.6% | 2.5% | 36/54 | ✓ 但缺字 |

**推荐第一行**：它就是 ADR-009 指定的思源宋体（Source Han Serif 与
Noto Serif CJK 是同一套字形，Adobe 与 Google 联合发布），以 SIL OFL 1.1
分发——OFL 明确允许格式转换，且嵌入文档不会让文档受许可约束。
转换方法见 `tools/otf2ttf/`，办公室机器上跑一次即可。

BabelStone Han 是现成的 `.ttf`、免转换，但许可是 Arphic Public License
叠 CC-BY-SA，政务场景建议先过法务；扩展 A 覆盖也低一档。

日文字库（IPAGothic 等）**不能用**：实测排一份中文报告时"测评项目"会
印成"目"、"统计报告"印成"告"——gopdf 缺字是静默丢弃的，不报错也不画方框。
系统为此在排版前加了一道字形覆盖检查，缺字直接拒绝出报告。
