// 令牌单与导出：二维码、xlsx、PDF 缺字体时的提示。
//
// 二维码这部分只有真实浏览器测得了：要验的是渲染尺寸（LLD 4.3 要求
// 边长 ≥8cm、短码 ≥36pt）与打印样式是否真的把管理端外壳藏起来。
const L = require('./lib');
const fs = require('fs');
const path = require('path');

const { ADMIN, EVAL, PASS } = L;
const os = require('os');
const OUT = fs.mkdtempSync(path.join(os.tmpdir(), 'odes-e2e-'));

const check = L.check;

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const pw = L.requirePlaywright();
  const { browser, ctx } = await L.launch(pw);
  const page = await ctx.newPage();
  const jsErrors = [];
  page.on('pageerror', e => jsErrors.push(String(e)));
  page.on('dialog', d => d.accept());

  await page.goto(ADMIN + '/admin/', { waitUntil: 'networkidle' });
  if (await page.locator('input[type=password]').count()) {
    await page.fill('input[type=password]', PASS);
    await page.click('button[type=submit]');
    await page.waitForLoadState('networkidle');
  }
  const href = await page.locator('a[href*="/admin/projects/"]:not([href$="/new"])').first().getAttribute('href');
  const projURL = ADMIN + href;

  // ── 二维码（FR-TKN-032/033）──────────────────────────────
  await page.goto(projURL + '/print', { waitUntil: 'networkidle' });
  const imgs = page.locator('img.qr');
  const n = await imgs.count();
  check('令牌单每张都有二维码', n >= 30, `${n} 个`);

  const srcs = await imgs.evaluateAll(els => els.map(e => e.getAttribute('src')));
  check('二维码为内联 data URI（不发外部请求）',
    srcs.every(s => s && s.startsWith('data:image/png;base64,')));
  check('每张令牌单的二维码互不相同', new Set(srcs).size === srcs.length,
    `${new Set(srcs).size}/${srcs.length} 唯一`);

  // 实际渲染尺寸 ≥8cm（LLD 4.3）。1cm ≈ 37.8px
  const box = await imgs.first().boundingBox();
  check('二维码渲染边长 ≥8cm', box && box.width >= 8 * 37.7,
    box ? `${(box.width / 37.8).toFixed(1)}cm` : '无法测量');

  // 短码字号 ≥36pt（1pt ≈ 1.333px）
  const codeSize = await page.locator('.sheet .code').first()
    .evaluate(el => parseFloat(getComputedStyle(el).fontSize));
  check('短码字号 ≥36pt', codeSize >= 36 * 1.333 - 1,
    `${(codeSize / 1.333).toFixed(1)}pt`);

  // 打印时管理端外壳必须隐藏
  await page.emulateMedia({ media: 'print' });
  const navVisible = await page.locator('.no-print').first().isVisible().catch(() => false);
  check('打印时隐藏管理端外壳', !navVisible);
  await page.emulateMedia({ media: 'screen' });

  // 二维码真的能扫出来：解出内容应是本项目的作答 URL
  const decoded = await page.evaluate(async (src) => {
    const img = new Image();
    await new Promise(r => { img.onload = r; img.src = src; });
    const c = document.createElement('canvas');
    c.width = img.width; c.height = img.height;
    c.getContext('2d').drawImage(img, 0, 0);
    if (!('BarcodeDetector' in window)) return 'NO_DETECTOR';
    const det = new BarcodeDetector({ formats: ['qr_code'] });
    const res = await det.detect(c);
    return res.length ? res[0].rawValue : 'NONE';
  }, srcs[0]);
  if (decoded === 'NO_DETECTOR') {
    console.log('SKIP  浏览器无 BarcodeDetector，跳过实际解码');
  } else {
    check('二维码可被解码且指向作答页', /\/e\/[A-Z0-9]{20,}$/.test(decoded), decoded);
  }

  // ── xlsx 导出 ─────────────────────────────────────────────
  const dl = async (url, name) => {
    const r = await page.request.get(url, { ignoreHTTPSErrors: true });
    if (!r.ok()) return { ok: false, status: r.status() };
    const b = await r.body();
    fs.writeFileSync(`${OUT}/${name}`, b);
    return { ok: true, size: b.length, zip: b[0] === 0x50 && b[1] === 0x4b, ct: r.headers()['content-type'] };
  };

  const tk = await dl(projURL + '/export?kind=tokens', 'token-list.xlsx');
  check('令牌清单 xlsx 可下载且是合法 zip 容器', tk.ok && tk.zip, JSON.stringify(tk));
  check('令牌清单 MIME 正确', (tk.ct || '').includes('spreadsheetml'), tk.ct);

  // ── PDF 报告：字体已随仓库提交，这里要出的是一份真 PDF ────
  //
  // 这两条以前断言的是"缺字体时给出可照做的提示"。字体入库后那个前提
  // 不成立了——继续留着就是一条永远为假的断言，而它会掩盖真正的回归。
  const pdf = await page.request.get(projURL + '/export?kind=pdf', { ignoreHTTPSErrors: true });
  check('PDF 报告可导出', pdf.ok(), 'status=' + pdf.status());
  const pdfBody = await pdf.body();
  check('产出的是合法 PDF',
    pdfBody.slice(0, 5).toString() === '%PDF-' && pdfBody.includes(Buffer.from('%%EOF')),
    pdfBody.slice(0, 8).toString());
  // /FontFile2 是内嵌 TrueType 字体的标志。缺了它，换台没装该字体的机器
  // 打开就是一片方框——而这恰恰是 ADR-009 要求内嵌完整字库的原因。
  check('中文字库已内嵌（/FontFile2）',
    pdfBody.includes(Buffer.from('/FontFile2')),
    `${(pdfBody.length / 1024).toFixed(0)} KB`);
  check('体积符合内嵌字库的量级', pdfBody.length > 50 * 1024,
    `${(pdfBody.length / 1024).toFixed(0)} KB`);

  check('管理端无 JS 报错', jsErrors.length === 0, jsErrors.join(' / '));

  await browser.close();
  process.exit(L.summary('二维码 / xlsx / PDF 提示') === 0 ? 0 : 1);
})().catch(e => {
  console.error('套件异常终止:', e);
  process.exit(1);
});
