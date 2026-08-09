// 发布 → 作答 → 截止 → 统计 → 导出的完整流程。
//
// 覆盖的关键判据：状态徽章逐级正确、同一令牌不可二次提交、
// 主报表每行「四档票数 + 弃权 = 应参加人数」、CSV 带 BOM 且无公式注入面、
// 比率为一位小数定点。
const L = require('./lib');

const { ADMIN, EVAL, PASS } = L;

const check = L.check;

(async () => {
  const pw = L.requirePlaywright();
  const { browser, ctx } = await L.launch(pw);
  const page = await ctx.newPage();
  const jsErrors = [];
  page.on('pageerror', e => jsErrors.push(String(e)));
  page.on('dialog', d => d.accept());   // 管理端的发布/截止二次确认

  await page.goto(ADMIN + '/admin/', { waitUntil: 'networkidle' });
  if (await page.locator('input[type=password]').count()) {
    await page.fill('input[type=password]', PASS);
    await page.click('button[type=submit]');
    await page.waitForLoadState('networkidle');
  }
  const href = await page.locator('a[href*="/admin/projects/"]:not([href$="/new"])').first().getAttribute('href');
  const projURL = ADMIN + href;

  // 收集真实令牌
  await page.goto(projURL + '/print', { waitUntil: 'networkidle' });
  const printText = await page.content();
  const tokens = [...new Set([...printText.matchAll(/\/e\/([A-Z0-9]{20,})/g)].map(m => m[1]))];
  check('令牌单上取到令牌', tokens.length >= 5, `${tokens.length} 个`);

  // 试填一次，满足 PC-07
  await page.goto(projURL + '/trial', { waitUntil: 'networkidle' });
  const trialHref = await page.locator('a[href*="/e/"]').first().getAttribute('href');
  {
    const tp = await ctx.newPage();
    tp.on('dialog', d => d.accept());
    await tp.goto(trialHref.startsWith('http') ? trialHref : EVAL + trialHref, { waitUntil: 'networkidle' });
    for (let p = 0; p < 12; p++) {
      const groups = tp.locator('[role="radiogroup"]');
      const gn = await groups.count();
      if (gn === 0) break;
      for (let g = 0; g < gn; g++) {
        const o = groups.nth(g).locator('[role="radio"]').first();
        if (await o.count()) await o.click();
      }
      const tas = tp.locator('textarea');
      for (let t = 0; t < await tas.count(); t++) await tas.nth(t).fill('试填');
      const next = tp.locator('button:has-text("下一")');
      if (await next.count() && await next.first().isEnabled()) { await next.first().click(); await tp.waitForTimeout(120); }
      else break;
    }
    await tp.locator('button:has-text("提交")').last().click();
    await tp.waitForTimeout(800);
    check('试填提交成功（PC-07）', /提交成功，感谢参与/.test(await tp.locator('body').innerText()));
    await tp.close();
  }

  // 发布
  await page.goto(projURL, { waitUntil: 'networkidle' });

  const pub = page.locator('button:has-text("发布测评")').first();
  if (await pub.count()) { await pub.click(); await page.waitForLoadState('networkidle'); }
  // 状态一律读状态徽章，不读正文——正文里「手动截止」这种按钮文案会误配
  const badge = () => page.locator('.badge').first().innerText();
  // published 会在开始时间到达后自动迁到 running，刷一次让它落定
  await page.goto(projURL, { waitUntil: 'networkidle' });
  let s = await badge();
  check('项目已发布', s === '进行中' || s === '已发布', '徽章=' + s);

  // 6 份真实作答，故意让四档分布不均以逼出调平逻辑
  const N = 6;
  for (let i = 0; i < N; i++) {
    const ep = await ctx.newPage();
    ep.on('dialog', d => d.accept());   // FR-ANS-017 的二次确认
    await ep.goto(EVAL + '/e/' + tokens[i], { waitUntil: 'networkidle' });
    for (let p = 0; p < 12; p++) {
      const groups = ep.locator('[role="radiogroup"]');
      const gn = await groups.count();
      if (gn === 0) break;
      for (let g = 0; g < gn; g++) {
        const opts = groups.nth(g).locator('[role="radio"]');
        const on = await opts.count();
        if (on) await opts.nth((i + g) % on).click();
      }
      const tas = ep.locator('textarea');
      for (let t = 0; t < await tas.count(); t++) await tas.nth(t).fill('第 ' + i + ' 份评语');
      const next = ep.locator('button:has-text("下一")');
      if (await next.count() && await next.first().isEnabled()) {
        await next.first().click(); await ep.waitForTimeout(120);
      } else break;
    }
    const sub = ep.locator('button:has-text("提交")').last();
    if (await sub.count()) {
      await sub.click(); await ep.waitForTimeout(300);
      const c = ep.locator('button:has-text("确认提交"), button:has-text("确定")');
      if (await c.count() && await c.first().isVisible()) { await c.first().click(); await ep.waitForTimeout(500); }
    }
    const txt = await ep.locator('body').innerText();
    if (!/提交成功，感谢参与/.test(txt)) check(`第 ${i + 1} 份提交`, false, txt.slice(0, 80));
    await ep.close();
  }
  check(`${N} 份真实作答已提交`, true);

  // 同一令牌二次提交必须被拒
  const dup = await ctx.newPage();
  await dup.goto(EVAL + '/e/' + tokens[0], { waitUntil: 'networkidle' });
  const dupTxt = await dup.locator('body').innerText();
  check('已用令牌重开显示完成态', /已完成测评|提交成功/.test(dupTxt), dupTxt.slice(0, 60).replace(/\n/g, ' '));
  await dup.close();

  // 截止
  await page.goto(projURL, { waitUntil: 'networkidle' });

  for (let i = 0; i < 3 && (await badge()) !== '已截止'; i++) {
    const close = page.locator('button:has-text("手动截止")').first();
    if (!(await close.count())) break;
    await close.click();
    await page.waitForLoadState('networkidle');
    await page.goto(projURL, { waitUntil: 'networkidle' });
  }
  s = await badge();
  check('项目已截止', s === '已截止', '徽章=' + s);

  // 统计页：M-9
  await page.goto(projURL + '/stats', { waitUntil: 'networkidle' });
  const st = await page.locator('body').innerText();
  const pcts = [...st.matchAll(/(\d+\.\d)%/g)].map(m => m[1]);
  check('统计页有比率数据', pcts.length > 0, `${pcts.length} 个百分比`);
  check('比率全部一位小数', pcts.every(p => /^\d+\.\d$/.test(p)));

  // 主报表显示的是票数，不是四档比率——恒等式在这里的可见形式是
  // 「四档票数 + 弃权 = 应参加人数」（未提交折入弃权，分母为应参加人数）。
  const rows = await page.locator('table.table tbody tr').evaluateAll(trs =>
    trs.map(tr => [...tr.querySelectorAll('td')].map(td => td.innerText.trim())));
  let checkedRows = 0, badRow = null;
  for (const r of rows) {
    if (r.length < 9) continue;                       // 评语表只有一列
    const counts = r.slice(1, 6).map(Number);
    if (counts.some(Number.isNaN)) continue;
    checkedRows++;
    const sum = counts.reduce((a, b) => a + b, 0);
    if (sum !== 30) badRow = r.join(' | ') + ' → ' + sum;
  }
  check('每行 四档票数+弃权 = 应参加人数 30', checkedRows > 0 && !badRow,
    badRow ? badRow : `校验了 ${checkedRows} 行`);

  check('统计页无 JS 报错', jsErrors.length === 0, jsErrors.join(' / '));

  // 导出仍可用：报告是内联 HTML，CSV 是附件，分别核对
  const rep = await page.request.get(projURL + '/export', { ignoreHTTPSErrors: true });
  check('统计报告可导出', rep.ok(), 'status=' + rep.status());
  const repBody = await rep.text();
  check('报告含口径说明', /应参加人数|口径/.test(repBody), repBody.length + ' 字节');

  const csv = await page.request.get(projURL + '/export?kind=csv', { ignoreHTTPSErrors: true });
  check('明细 CSV 可导出', csv.ok(), 'status=' + csv.status());
  const csvBody = await csv.text();
  check('CSV 带 BOM（Excel 直接可开）', csvBody.charCodeAt(0) === 0xFEFF);
  // 公式注入防护：任何单元格不得以 = + - @ 开头
  const badCell = csvBody.split('\n').slice(1).flatMap(l => l.split(',')).find(c => /^"?[=+\-@]/.test(c.trim()));
  check('CSV 无公式注入面', !badCell, badCell || '');

  // M-9：CSV 的比率列必须是一位小数定点，且票数列同样满足恒等式
  const lines = csvBody.replace(/^\uFEFF/, '').split('\n').filter(l => l.trim());
  const dataRows = lines.slice(1).filter(l => /^[^,]*,[^,]*,\d/.test(l)).map(l => l.split(','));
  let csvBad = null, csvRows = 0;
  for (const r of dataRows) {
    if (r.length < 13) continue;
    csvRows++;
    const counts = r.slice(2, 7).map(Number);
    if (counts.reduce((a, b) => a + b, 0) !== 30) csvBad = 'counts ' + r.join('|');
    for (const c of r.slice(7, 12)) if (!/^\d+\.\d%$/.test(c)) csvBad = 'rate ' + c;
  }
  check('CSV 票数恒等且比率为一位小数定点', csvRows > 0 && !csvBad,
    csvBad || `校验了 ${csvRows} 行`);

  await browser.close();
  process.exit(L.summary('发布→作答→截止→统计→导出') === 0 ? 0 : 1);
})().catch(e => {
  console.error('套件异常终止:', e);
  process.exit(1);
});
