// 端到端测试的公共部分：浏览器启动、登录、断言与作答流程。
//
// 刻意不引测试框架（jest/mocha 之类）：这一层只有四个套件，一个
// check() 函数就够了，而多一个框架就多一份要装、要跟着升级的东西
// （LLD 12.7.4「能用原生工具时不叠兼容层」）。

const fs = require('fs');

const ADMIN = process.env.ODES_ADMIN || 'https://127.0.0.1:8444';
const EVAL = process.env.ODES_EVAL || 'https://127.0.0.1:8443';
const PASS = process.env.ODES_PASSWORD || 'Test-Passw0rd';

// Chromium 路径：CI 与开发机上位置不同，按常见路径探测。
function chromiumPath() {
  if (process.env.CHROMIUM_PATH) return process.env.CHROMIUM_PATH;
  const candidates = [
    '/opt/pw-browsers/chromium-1194/chrome-linux/chrome',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
    '/usr/bin/google-chrome',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  ];
  for (const p of candidates) {
    try { if (fs.existsSync(p)) return p; } catch (e) { /* 忽略 */ }
  }
  return undefined; // 交给 Playwright 自带的那份
}

function requirePlaywright() {
  for (const p of ['playwright', '/opt/node22/lib/node_modules/playwright']) {
    try { return require(p); } catch (e) { /* 换下一个 */ }
  }
  console.error('未找到 playwright：npm i -g playwright');
  process.exit(2);
}

// ── 断言 ────────────────────────────────────────────────────────────

let failures = 0;
let total = 0;

function check(name, ok, extra) {
  total++;
  if (!ok) failures++;
  const mark = ok ? 'PASS' : 'FAIL';
  console.log(`  ${mark}  ${name}${extra ? '  — ' + String(extra).slice(0, 160) : ''}`);
}

function skip(name, why) {
  console.log(`  SKIP  ${name}${why ? '  — ' + why : ''}`);
}

function summary(suite) {
  console.log(`\n[${suite}] ${total - failures}/${total} 通过`);
  return failures;
}

// ── 浏览器 ──────────────────────────────────────────────────────────

async function launch(playwright) {
  const browser = await playwright.chromium.launch({
    executablePath: chromiumPath(),
    args: ['--no-sandbox'],
  });
  // 自签名证书：开发与 CI 下都是自签的，必须忽略，否则第一步就走不下去
  const ctx = await browser.newContext({ ignoreHTTPSErrors: true, acceptDownloads: true });
  return { browser, ctx };
}

// newPage 建页并挂三个通用钩子：
//   · 未捕获异常单独收集——作答页出一个未捕获异常，整页就不再响应，
//     这是唯一"必须为零"的一类
//   · 控制台错误另收一份。断网测试必然产生 ERR_INTERNET_DISCONNECTED
//     这类资源加载错误，把它和未捕获异常混为一谈，要么逼着断网用例
//     假绿，要么逼着人给整个断言开后门
//   · 自动接受确认框——FR-ANS-017 的"提交后无法修改"确认等
function newPage(ctx, errors, consoleErrors) {
  return ctx.newPage().then(p => {
    p.on('pageerror', e => errors.push(String(e)));
    p.on('console', m => {
      if (m.type() === 'error' && consoleErrors) consoleErrors.push(m.text());
    });
    p.on('dialog', d => d.accept());
    return p;
  });
}

// 断网期间预期内的控制台噪音，不计入失败。
const expectedOfflineNoise = /ERR_INTERNET_DISCONNECTED|ERR_NETWORK|Failed to load resource|Failed to fetch/i;

function unexpectedConsoleErrors(list) {
  return (list || []).filter(m => !expectedOfflineNoise.test(m));
}

async function login(page) {
  await page.goto(ADMIN + '/admin/', { waitUntil: 'networkidle' });
  if (await page.locator('input[type=password]').count()) {
    await page.fill('input[type=password]', PASS);
    await page.click('button[type=submit]');
    await page.waitForLoadState('networkidle');
  }
}

// firstProjectURL 取首个真实项目。必须排除 /new——那是"新建项目"的链接，
// 不加这个过滤会一路点到新建表单上，而症状是后面每一条断言都莫名其妙。
async function firstProjectURL(page) {
  const href = await page
    .locator('a[href*="/admin/projects/"]:not([href$="/new"])')
    .first().getAttribute('href');
  if (!href) throw new Error('管理端首页没有项目；请带 -seed 启动');
  return ADMIN + href;
}

// statusBadge 读状态徽章。
//
// 一律读徽章而不是读正文：正文里「手动截止」这类按钮文案会把
// /已截止/ 这样的正则误配成"项目已截止"，而那种假绿曾经让一个
// 状态机死锁一路溜过去。
async function statusBadge(page, projURL) {
  await page.goto(projURL, { waitUntil: 'networkidle' });
  return page.locator('.badge').first().innerText();
}

// ensureRunning 把项目推到可作答状态。
//
// 演示项目初始是草稿，草稿态下正式令牌一律 404——作答端的套件必须先
// 走完"打印令牌单 → 试填 → 发布"这三步前置校验（PC-06 / PC-07），
// 否则第一步就打不开页面，而症状会误导成"作答页坏了"。
async function ensureRunning(page, projURL, ctx) {
  let badge = await statusBadge(page, projURL);
  if (badge === '进行中' || badge === '已发布') return badge;
  if (badge !== '草稿') {
    throw new Error('项目处于「' + badge + '」，本套件需要草稿或进行中的项目');
  }

  await page.goto(projURL + '/print', { waitUntil: 'networkidle' }); // 满足 PC-06

  // 试填一次满足 PC-07
  await page.goto(projURL + '/trial', { waitUntil: 'networkidle' });
  const trial = await page.locator('a[href*="/e/"]').first().getAttribute('href');
  if (trial) {
    const tp = await ctx.newPage();
    tp.on('dialog', d => d.accept());
    await tp.goto(trial.startsWith('http') ? trial : EVAL + trial,
      { waitUntil: 'networkidle' });
    await answerAllPages(tp, () => 0, '试填');
    await submitAnswers(tp);
    await tp.close();
  }

  await page.goto(projURL, { waitUntil: 'networkidle' });
  const pub = page.locator('button:has-text("发布测评")').first();
  if (await pub.count()) {
    await pub.click();
    await page.waitForLoadState('networkidle');
  }
  badge = await statusBadge(page, projURL);
  if (badge !== '已发布' && badge !== '进行中') {
    throw new Error('发布失败，当前状态「' + badge + '」；前置校验可能未通过');
  }
  return badge;
}

// ── 作答流程 ────────────────────────────────────────────────────────

// answerAllPages 逐页作答。pick(i) 决定每组选第几个选项。
async function answerAllPages(page, pick = () => 0, text = '端到端测试评语') {
  let pages = 0;
  for (let p = 0; p < 20; p++) {
    const groups = page.locator('[role="radiogroup"]');
    const gn = await groups.count();
    if (gn === 0) break;
    for (let g = 0; g < gn; g++) {
      const opts = groups.nth(g).locator('[role="radio"]');
      const on = await opts.count();
      if (on) await opts.nth(pick(g) % on).click();
    }
    const tas = page.locator('textarea');
    for (let t = 0; t < await tas.count(); t++) await tas.nth(t).fill(text);
    pages++;
    const next = page.locator('button:has-text("下一")');
    if (await next.count() && await next.first().isEnabled()) {
      await next.first().click();
      await page.waitForTimeout(120);
    } else break;
  }
  return pages;
}

async function submitAnswers(page) {
  const btn = page.locator('button:has-text("提交")').last();
  if (!(await btn.count())) return false;
  await btn.click();
  await page.waitForTimeout(600);
  return /提交成功，感谢参与/.test(await page.locator('body').innerText());
}

// draftKeys 返回作答页在 LocalStorage 里留下的草稿键。
async function draftKeys(page) {
  return page.evaluate(() =>
    Object.keys(localStorage).filter(k => /odes|draft/i.test(k)));
}

module.exports = {
  ADMIN, EVAL, PASS,
  requirePlaywright, launch, newPage, login, firstProjectURL,
  answerAllPages, submitAnswers, draftKeys,
  statusBadge, ensureRunning, unexpectedConsoleErrors,
  check, skip, summary,
};
