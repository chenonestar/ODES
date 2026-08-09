# 浏览器端到端测试

对应 LLD 11.2 的 UI-01 / UI-02，以及一批"Go 测试测不到"的路径。

## 为什么必须有这一层

作答端是 Alpine.js CSP 构建的单页，管理端有拖拽排序与打印样式——
这些行为只在真实浏览器里成立。开发过程中这一层实测逮出过五个 Go 测试
完全够不着的缺陷：

- 状态机死锁：「手动截止」按钮在 published 下渲染，点了只得到一页错误，
  项目再也截不了止，统计/归档/擦除全部走不到
- `draggable=""` 是非法值（HTML 里 draggable 是枚举属性不是布尔属性），
  表格行根本拖不动，但页面上的拖拽把手看起来一切正常
- 复制含选择题的项目必然失败（选项主键冲突）
- 表单校验失败时重定向，题干、题型、选项、勾选的对象全部丢失
- 算术验证答错会无限弹窗，且没有一处告诉用户"上次答错了"

## 跑法

```bash
cd eval
go build -o /tmp/odes ./cmd/eval
/tmp/odes -dev -seed -data /tmp/odes-data -password 'Test-Passw0rd' &
node test/e2e/run.js
```

`run.js` 会依次跑完四个套件，任一失败即以非零码退出。

需要 Node 18+ 与 Playwright：

```bash
npm i -g playwright
playwright install chromium
```

**浏览器优先用 Playwright 自带的那份**（`launch` 时不指定 `executablePath`）。
CI 镜像里往往自带 Google Chrome，若优先挑系统的那个，就会拿一个与
Playwright 版本不匹配的浏览器去驱动，症状是启动即失败或行为诡异。
自带的没装时才回退到系统路径，也可用 `CHROMIUM_PATH` 指定。

**模块解析**：全局装的 playwright，Node 默认查不到——因为它不搜全局
`node_modules`。`lib.js` 按「局部 → `npm root -g` → `NODE_PATH` → 常见路径」
依次尝试，全找不到时会把找过的位置逐条列出来。

## 环境变量

| 变量 | 用途 |
|---|---|
| `ODES_ADMIN` / `ODES_EVAL` | 管理端 / 作答端地址，`run.js` 自动注入 |
| `ODES_PASSWORD` | 管理员口令，默认 `Test-Passw0rd` |
| `ODES_BIN` | 用现成的可执行文件，跳过编译 |
| `CHROMIUM_PATH` | 指定浏览器（仅在自带的不可用时生效） |

## 与 Go 测试的分工

Go 测试守数据与不变量（匿名性、口径、事务、架构）；这一层守**人在浏览器里
真的能把流程走完**。两者不重叠：Go 测试全绿而作答页白屏，是完全可能的。
