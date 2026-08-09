// 端到端测试调度器。
//
// 每个套件都跑在**自己的服务器实例与自己的数据目录**上。
//
// 为什么不共用一个实例：套件之间会改变项目状态——lifecycle 会把演示项目
// 一路推到"已截止"，而 ui 需要一个可作答的项目。共用实例的话，套件顺序
// 一变结果就变，排查时先要搞清"是真错了还是被上一个套件带歪了"。
// 每次重开一个实例的代价是几秒钟，换来的是每个套件都能单独重跑。

const { spawn, spawnSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const net = require('net');

const SUITES = [
  { file: 'ui.js', name: 'UI-01 / UI-02　断网续填与草稿隔离' },
  { file: 'lifecycle.js', name: '发布 → 作答 → 截止 → 统计 → 导出' },
  { file: 'tokens-export.js', name: '二维码 / xlsx 导出 / PDF 提示' },
  { file: 'designer.js', name: '项目 CRUD / 测评表设计器 / 名单导入' },
];

const ROOT = path.resolve(__dirname, '../..');
const PASSWORD = 'Test-Passw0rd';

function log(...a) { console.log(...a); }

// buildBinary 编译一份用于测试的可执行文件。
// 不用 `go run`：那样每次启动都要重新编译，四个套件就是四次，
// 而且 go run 的子进程树在 kill 时容易留下孤儿进程。
function buildBinary() {
  const out = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'odes-bin-')), 'eval');
  log('编译测试用可执行文件…');
  const r = spawnSync('go', ['build', '-o', out, './cmd/eval'],
    { cwd: ROOT, stdio: 'inherit' });
  if (r.status !== 0) {
    log('编译失败');
    process.exit(1);
  }
  return out;
}

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const p = srv.address().port;
      srv.close(() => resolve(p));
    });
    srv.on('error', reject);
  });
}

// waitReady 轮询管理端直到能连上。
// 不按固定 sleep：机器快慢差几倍，固定等待要么白等要么偶发失败。
async function waitReady(port, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const ok = await new Promise(resolve => {
      const s = net.connect(port, '127.0.0.1');
      s.on('connect', () => { s.destroy(); resolve(true); });
      s.on('error', () => resolve(false));
      setTimeout(() => { s.destroy(); resolve(false); }, 500);
    });
    if (ok) return true;
    await new Promise(r => setTimeout(r, 300));
  }
  return false;
}

async function runSuite(bin, suite) {
  const dataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'odes-data-'));
  const evalPort = await freePort();
  const adminPort = evalPort + 1;

  const logPath = path.join(dataDir, 'server.log');
  const logFd = fs.openSync(logPath, 'a');
  const srv = spawn(bin, [
    '-dev', '-seed', '-data', dataDir,
    '-port', String(evalPort), '-password', PASSWORD,
  ], { cwd: ROOT, stdio: ['ignore', logFd, logFd] });

  let code = 1;
  try {
    if (!await waitReady(adminPort)) {
      log('  服务器未就绪，日志：\n' + fs.readFileSync(logPath, 'utf8').slice(-2000));
      return 1;
    }
    const r = spawnSync(process.execPath, [path.join(__dirname, suite.file)], {
      cwd: ROOT,
      stdio: 'inherit',
      env: {
        ...process.env,
        ODES_ADMIN: `https://127.0.0.1:${adminPort}`,
        ODES_EVAL: `https://127.0.0.1:${evalPort}`,
        ODES_PASSWORD: PASSWORD,
      },
    });
    code = r.status === null ? 1 : r.status;
    if (code !== 0) {
      log('  服务器日志尾部：\n' + fs.readFileSync(logPath, 'utf8').slice(-1200));
    }
  } finally {
    srv.kill('SIGTERM');
    // 给优雅退出一点时间，超时就强杀——留着进程会占住端口
    await new Promise(r => setTimeout(r, 800));
    if (srv.exitCode === null) srv.kill('SIGKILL');
    fs.closeSync(logFd);
  }
  return code;
}

(async () => {
  const only = process.argv[2];
  const bin = process.env.ODES_BIN || buildBinary();

  const results = [];
  for (const suite of SUITES) {
    if (only && !suite.file.includes(only)) continue;
    log(`\n━━ ${suite.name} ━━`);
    const code = await runSuite(bin, suite);
    results.push({ suite, code });
  }

  if (results.length === 0) {
    log(`没有匹配「${only}」的套件`);
    process.exit(2);
  }

  log('\n════ 汇总 ════');
  let failed = 0;
  for (const { suite, code } of results) {
    log(`  ${code === 0 ? '✓' : '✗'}  ${suite.name}`);
    if (code !== 0) failed++;
  }
  log(failed === 0
    ? `\n${results.length} 个套件全部通过`
    : `\n${failed}/${results.length} 个套件未通过`);
  process.exit(failed === 0 ? 0 : 1);
})().catch(e => {
  console.error('调度器异常:', e);
  process.exit(1);
});
