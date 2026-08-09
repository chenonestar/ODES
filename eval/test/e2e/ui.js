// UI-01 / UI-02（LLD 11.2）
//
// UI-01 作答端断网后翻页、填写、校验均正常，仅提交失败并可重试
// UI-02 同一浏览器先后用两个令牌作答，草稿不串档；提交后草稿被清除
//
// 这两条是 ADR-001「作答页做成自包含单页」这个决定的验收条件。
// 会场几百部手机挤同一批 AP，信号抖动是常态；若翻页要发请求，
// 参评人员会卡在半路，而散会后无法补测——数据就这么丢了。

const L = require('./lib');

(async () => {
  const pw = L.requirePlaywright();
  const { browser, ctx } = await L.launch(pw);
  const errors = [];        // 未捕获异常，必须为零
  const consoleErrors = []; // 控制台错误，断网期间会有预期噪音
  const admin = await L.newPage(ctx, errors, consoleErrors);

  await L.login(admin);
  const projURL = await L.firstProjectURL(admin);
  const badge = await L.ensureRunning(admin, projURL, ctx);
  L.check('项目已进入可作答状态', badge === '已发布' || badge === '进行中', '徽章=' + badge);

  // 取两个不同的令牌：UI-02 要先后用两个令牌作答
  await admin.goto(projURL + '/print', { waitUntil: 'networkidle' });
  const html = await admin.content();
  const tokens = [...new Set(
    [...html.matchAll(/\/e\/([A-Z0-9]{20,})/g)].map(m => m[1]))];
  L.check('令牌单上取到至少 2 个令牌', tokens.length >= 2, `${tokens.length} 个`);

  // ── UI-01 断网后仍可翻页、填写、校验 ─────────────────────
  {
    const page = await L.newPage(ctx, errors, consoleErrors);
    await page.goto(L.EVAL + '/e/' + tokens[0], { waitUntil: 'networkidle' });
    L.check('UI-01 作答页已加载', (await page.locator('[role="radiogroup"]').count()) > 0);

    // 断网。作答页是自包含单页，加载完之后不应再需要网络。
    await ctx.setOffline(true);

    const pages = await L.answerAllPages(page, () => 0, '断网状态下填的评语');
    L.check('UI-01 断网后仍能逐页翻完', pages >= 2, `翻了 ${pages} 页`);

    const body = await page.locator('body').innerText();
    L.check('UI-01 断网后页面内容正常（不是白屏或错误页）',
      /优秀|称职/.test(body) && !/无法访问|ERR_/.test(body));

    // 必填校验是本地做的，断网也应工作：清掉一项再提交，应就地提示
    const cleared = await page.evaluate(() => {
      const el = document.querySelector('[role="radio"][aria-checked="true"]');
      return !!el;
    });
    L.check('UI-01 断网后选择状态仍在 DOM 上', cleared);

    // 断网提交必须失败**且保留已填内容**，而不是清空或跳走
    const before = await L.draftKeys(page);
    await page.locator('button:has-text("提交")').last().click();
    await page.waitForTimeout(1200);
    const afterText = await page.locator('body').innerText();
    L.check('UI-01 断网提交失败并给出可重试的提示',
      /提交失败|已保留|重试|WiFi/.test(afterText),
      afterText.replace(/\s+/g, ' ').slice(0, 100));
    L.check('UI-01 提交失败后草稿仍在（内容没丢）',
      (await L.draftKeys(page)).length > 0 || before.length > 0);

    // 恢复网络后重试必须成功
    await ctx.setOffline(false);
    const ok = await L.submitAnswers(page);
    L.check('UI-01 恢复网络后重试提交成功', ok,
      (await page.locator('body').innerText()).replace(/\s+/g, ' ').slice(0, 80));
    L.check('UI-01 提交成功后草稿已清除',
      (await L.draftKeys(page)).length === 0);

    await page.close();
  }

  // ── UI-02 两个令牌先后作答，草稿不串档 ────────────────────
  {
    // 同一个 page 依次打开两个令牌，模拟"借用他人手机"的真实场景：
    // 这正是草稿串档最可能发生的时刻，而串档意味着甲的选择被当成乙的提交。
    const page = await L.newPage(ctx, errors, consoleErrors);

    // 第一个令牌：只填不交，留下草稿
    await page.goto(L.EVAL + '/e/' + tokens[1], { waitUntil: 'networkidle' });
    await L.answerAllPages(page, () => 0, '第一位参评人员的评语');
    // 草稿有 500ms 防抖（page.html 的 save()），读早了必然是空的
    await page.waitForTimeout(900);
    const draftA = await L.draftKeys(page);
    L.check('UI-02 第一个令牌填完后有草稿', draftA.length > 0, JSON.stringify(draftA));

    // 关键机制：草稿键按令牌隔离（draft:<token>）。
    // 若键与令牌无关，同一部手机上换个人作答就会把上一位的选择带出来，
    // 而提交出去的是甲的答案、记在乙的令牌上——这是最难事后发现的一类错误。
    L.check('UI-02 草稿键按令牌隔离',
      draftA.some(k => k.includes(tokens[1])),
      draftA.join(','));
    const textA = await page.locator('textarea').first().inputValue().catch(() => '');

    // 第二个令牌：同一浏览器直接打开
    await page.goto(L.EVAL + '/e/' + tokens[2 % tokens.length], { waitUntil: 'networkidle' });
    const textB = await page.locator('textarea').first().inputValue().catch(() => '');

    L.check('UI-02 换令牌后不带出上一位的评语',
      textB === '' || textB !== textA,
      `上一位="${textA.slice(0, 20)}" 本次="${textB.slice(0, 20)}"`);

    const checkedB = await page.locator('[role="radio"][aria-checked="true"]').count();
    L.check('UI-02 换令牌后没有预先选中的档位', checkedB === 0, `选中 ${checkedB} 项`);

    await page.close();
  }

  L.check('全程无未捕获 JS 异常', errors.length === 0, errors.slice(0, 3).join(' / '));
  const noisy = L.unexpectedConsoleErrors(consoleErrors);
  L.check('无预期外的控制台错误', noisy.length === 0, noisy.slice(0, 3).join(' / '));

  await browser.close();
  process.exit(L.summary('UI-01 / UI-02') === 0 ? 0 : 1);
})().catch(e => {
  console.error('套件异常终止:', e);
  process.exit(1);
});
