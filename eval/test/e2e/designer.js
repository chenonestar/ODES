// 项目 CRUD、测评表设计器、拖拽排序、名单导入。
//
// 拖拽必须在真实浏览器里测：draggable 是 HTML 的枚举属性而非布尔属性，
// 渲染成 draggable="" 时浏览器按 auto 处理、行根本拖不动，而页面上的
// 拖拽把手看起来一切正常——这个缺陷就是本套件逮出来的。
const L = require('./lib');
const fs = require('fs');
const path = require('path');

const { ADMIN, PASS } = L;
const os = require('os');
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'odes-e2e-'));

const check = L.check;
const dt = off => {
  const d = new Date(Date.now() + off);
  const p = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
};

(async () => {
  const pw = L.requirePlaywright();
  const { browser, ctx } = await L.launch(pw);
  const page = await ctx.newPage();
  const jsErrors = [];
  page.on('pageerror', e => jsErrors.push(String(e)));
  page.on('console', m => { if (m.type() === 'error') jsErrors.push('console: ' + m.text()); });
  page.on('dialog', d => d.accept());

  await page.goto(ADMIN + '/admin/', { waitUntil: 'networkidle' });
  if (await page.locator('input[type=password]').count()) {
    await page.fill('input[type=password]', PASS);
    await page.click('button[type=submit]');
    await page.waitForLoadState('networkidle');
  }

  // ── 新建项目（FR-PRJ-020）────────────────────────────────
  await page.click('a[href="/admin/projects/new"]');
  await page.waitForLoadState('networkidle');
  await page.fill('input[name=name]', '2027 年度中层干部民主测评');
  await page.fill('input[name=start_at]', dt(3600e3));
  await page.fill('input[name=end_at]', dt(2 * 3600e3));
  await page.fill('input[name=result_open_at]', dt(3 * 3600e3));
  await page.fill('input[name=expected_count]', '12');
  await page.click('button:has-text("保存")');
  await page.waitForLoadState('networkidle');
  check('新建项目后进入设计器', /测评表设计/.test(await page.locator('h1').innerText()),
    await page.locator('h1').innerText());
  const designURL = page.url();
  const projURL = designURL.replace('/design', '');

  // 时间校验必须挡住非法输入
  await page.goto(projURL + '/edit', { waitUntil: 'networkidle' });
  await page.fill('input[name=end_at]', dt(-3600e3)); // 截止早于开始
  await page.click('button:has-text("保存")');
  await page.waitForLoadState('networkidle');
  check('非法时间被拒绝并回显原因',
    /开始|截止|时间/.test(await page.locator('body').innerText()),
    (await page.locator('[role=alert]').first().innerText().catch(() => '无提示')).slice(0, 60));

  // ── 测评对象（FR-FRM-030）───────────────────────────────
  await page.goto(designURL, { waitUntil: 'networkidle' });
  for (const [n, duty] of [['张三', '局长'], ['李四', '副局长'], ['王五', '科长']]) {
    await page.fill('input[placeholder=姓名]', n);
    await page.fill('input[placeholder=职务]', duty);
    await page.click('button:has-text("添加对象")');
    await page.waitForLoadState('networkidle');
  }
  const subjRows = await page.locator('table[data-reorder*=subjects] tbody tr[data-id]').count();
  check('添加 3 位测评对象', subjRows === 3, `${subjRows} 位`);

  // ── 题目（FR-FRM-010~031）───────────────────────────────
  for (const title of ['政治品德', '履职能力', '工作实绩']) {
    await page.click('a:has-text("向本组添加题目")');
    await page.waitForLoadState('networkidle');
    await page.fill('input[name=title]', title);
    await page.click('button:has-text("保存")');
    await page.waitForLoadState('networkidle');
  }
  const qRows = await page.locator('table[data-reorder*="/groups/"] tbody tr[data-id]').count();
  check('添加 3 道题目', qRows === 3, `${qRows} 道`);

  // FR-FRM-034：展开后作答项 = 题目数 × 关联对象数
  const stats = await page.locator('.stat').allInnerTexts();
  const items = stats.find(s => s.includes('展开后作答项'));
  check('实时显示展开后作答项 = 3×3 = 9', /\b9\b/.test(items || ''),
    (items || '').replace(/\n/g, ' '));

  // 选项数校验
  await page.click('a:has-text("向本组添加题目")');
  await page.waitForLoadState('networkidle');
  await page.fill('input[name=title]', '最突出方面');
  await page.selectOption('select[name=kind]', 'single');
  await page.fill('input[name=options]', '担当作为');   // 只有 1 个，应被拒
  await page.click('button:has-text("保存")');
  await page.waitForLoadState('networkidle');
  check('单选题选项过少被拒', /2~10/.test(await page.locator('body').innerText()),
    (await page.locator('[role=alert]').first().innerText().catch(() => '')).slice(0, 50));

  // 全角逗号也要能用（中文输入法下的常态）
  await page.fill('input[name=options]', '担当作为，廉洁自律，群众口碑');
  await page.click('button:has-text("保存")');
  await page.waitForLoadState('networkidle');
  check('全角逗号分隔的选项被接受',
    (await page.locator('body').innerText()).includes('最突出方面'));

  // ── 拖拽排序（FR-FRM-022）───────────────────────────────
  const before = await page.locator('table[data-reorder*="/groups/"] tbody tr[data-id] td:nth-child(2)')
    .allInnerTexts();
  // 必须用 dragTo：Playwright 的 mouse.down/move/up 不会派发 HTML5 的
  // dragstart/dragover/dragend，用它测原生拖放永远是"没反应"。
  const rows = page.locator('table[data-reorder*="/groups/"] tbody tr[data-id]');
  await rows.nth(3).dragTo(rows.nth(0));
  await page.waitForTimeout(800);
  await page.reload({ waitUntil: 'networkidle' });
  const after = await page.locator('table[data-reorder*="/groups/"] tbody tr[data-id] td:nth-child(2)')
    .allInnerTexts();
  check('拖拽后顺序已持久化（刷新仍生效）',
    before.join('|') !== after.join('|'),
    `前: ${before.map(s => s.split('\n')[0]).join(',')} → 后: ${after.map(s => s.split('\n')[0]).join(',')}`);

  // ── 名单导入（FR-TKN-010）───────────────────────────────
  fs.writeFileSync(TMP + '/roster.csv', '﻿姓名\n甲一\n乙二\n丙三\n丙三\n');
  await page.goto(projURL + '/roster', { waitUntil: 'networkidle' });
  await page.setInputFiles('input[type=file]', TMP + '/roster.csv');
  await page.click('button:has-text("上传并预览")');
  await page.waitForLoadState('networkidle');
  const prev = await page.locator('body').innerText();
  check('上传后进入预览而非直接入库', /导入预览/.test(prev));
  check('表头与重复行已剔除（3 条）', /共解析出\s*3\s*条/.test(prev.replace(/\s+/g, ' ')),
    (prev.match(/共解析出[^。]*/) || [''])[0].slice(0, 60));
  check('与应参加人数不一致时给出提示', /不一致/.test(prev));

  await page.click('button:has-text("确认写入")');
  await page.waitForLoadState('networkidle');
  const afterImport = await page.locator('body').innerText();
  check('确认后写入成功', /当前名单（3 条）/.test(afterImport.replace(/\s+/g, '')) ||
    /当前名单.*3.*条/.test(afterImport));

  // 再导一次 2 条，必须整表替换而不是叠加
  fs.writeFileSync(TMP + '/roster2.csv', '丁四\n戊五\n');
  await page.setInputFiles('input[type=file]', TMP + '/roster2.csv');
  await page.click('button:has-text("上传并预览")');
  await page.waitForLoadState('networkidle');
  await page.click('button:has-text("确认写入")');
  await page.waitForLoadState('networkidle');
  const t2 = (await page.locator('body').innerText()).replace(/\s+/g, '');
  check('再次导入整表替换（2 条而非 5 条）', /当前名单（2条）/.test(t2),
    (t2.match(/当前名单（\d+条）/) || [''])[0]);

  // ── 复制为新项目（FR-PRJ-022）───────────────────────────
  await page.goto(projURL, { waitUntil: 'networkidle' });
  await Promise.all([
    page.waitForURL(/\/design$/, { timeout: 15000 }),
    page.click('button:has-text("复制为新项目")'),
  ]);
  await page.waitForLoadState('networkidle');
  const copyText = (await page.locator('body').innerText()).replace(/\s+/g, ' ');
  check('复制后进入新项目设计器', /测评表设计/.test(copyText));
  check('副本结构已复制（3 位对象）', /测评对象 3 /.test(copyText),
    (copyText.match(/测评对象[^|]{0,14}/) || [''])[0]);
  check('副本名单为空（不复制名单）', /名单条数 0 /.test(copyText),
    (copyText.match(/名单条数[^|]{0,14}/) || [''])[0]);

  check('管理端无 JS 报错', jsErrors.length === 0, jsErrors.slice(0, 3).join(' / '));

  await browser.close();
  process.exit(L.summary('项目 CRUD / 测评表设计器 / 名单导入') === 0 ? 0 : 1);
})().catch(e => {
  console.error('套件异常终止:', e);
  process.exit(1);
});
