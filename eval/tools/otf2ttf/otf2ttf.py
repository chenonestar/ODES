# 把 CFF 轮廓的 OTF 转成 glyf 轮廓的 TTF。
# 依据：思源宋体 / Noto Serif CJK 以 SIL OFL 1.1 发布，OFL 明确允许修改与
# 格式转换（衍生版须以 OFL 继续分发，且不得单独售卖字体本身）。
import sys
from fontTools.ttLib import TTFont, TTCollection, newTable
from fontTools.pens.cu2quPen import Cu2QuPen
from fontTools.pens.ttGlyphPen import TTGlyphPen

MAX_ERR = 1.0  # 单位为 em 的千分之一，1.0 对正文字号肉眼不可分辨

def otf_to_ttf(font, max_err=MAX_ERR):
    glyphOrder = font.getGlyphOrder()
    glyphSet = font.getGlyphSet()
    glyf = newTable("glyf")
    glyf.glyphOrder = glyphOrder
    glyf.glyphs = {}
    for name in glyphOrder:
        pen = TTGlyphPen(glyphSet)
        glyphSet[name].draw(Cu2QuPen(pen, max_err, reverse_direction=True))
        glyf[name] = pen.glyph()
    font["glyf"] = glyf
    # 必须重算每个字形的包围盒：maxp/head 的重算会读 xMin 等字段，
    # 而 TTGlyphPen 产出的字形此时还没有这些值。
    for name in glyphOrder:
        glyf[name].recalcBounds(glyf)

    # loca 必须显式建出来。源 OTF 里没有这张表（CFF 不用它），
    # 而 glyf 轮廓的字形偏移全靠它定位——缺了它 gopdf 直接报
    # "table not found"，而字体在系统预览里看起来完全正常。
    font["loca"] = newTable("loca")

    # maxp / head / post 要改成 TrueType 的形态，否则渲染端会按 CFF 去找轮廓
    maxp = newTable("maxp")
    maxp.tableVersion = 0x00010000
    maxp.maxZones = 1
    maxp.maxTwilightPoints = 0
    maxp.maxStorage = 0
    maxp.maxFunctionDefs = 0
    maxp.maxInstructionDefs = 0
    maxp.maxStackElements = 0
    maxp.maxSizeOfInstructions = 0
    maxp.maxComponentElements = max(
        (len(g.components) if g.isComposite() else 0) for g in glyf.glyphs.values())
    font["maxp"] = maxp
    font["maxp"].compile(font)
    glyf.compile(font)

    font["head"].indexToLocFormat = 1
    if "post" in font:
        # 用 post 3.0（不存字形名）。CJK 字体有六万多个字形，
        # format 2.0 的索引是 uint16，装不下——这是转换 CJK 字体时
        # 必然会撞上的一处，与字体本身无关。
        font["post"].formatType = 3.0
        font["post"].extraNames = []
        font["post"].mapping = {}
        font["post"].glyphOrder = None
    for t in ("CFF ", "CFF2", "VORG"):
        if t in font:
            del font[t]
    font.sfntVersion = "\x00\x01\x00\x00"
    return font

src, idx, dst = sys.argv[1], int(sys.argv[2]), sys.argv[3]
if src.lower().endswith((".ttc", ".otc")):
    f = TTCollection(src)[idx]
else:
    f = TTFont(src)
print("源：", f["name"].getDebugName(4))
otf_to_ttf(f)
f.save(dst)
print("已写出", dst)
