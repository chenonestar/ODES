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

需要 Node 18+ 与 Playwright（`npm i -g playwright`）。浏览器用系统里已有的
Chromium，通过 `CHROMIUM_PATH` 指定，未指定时按常见路径探测。

## 与 Go 测试的分工

Go 测试守数据与不变量（匿名性、口径、事务、架构）；这一层守**人在浏览器里
真的能把流程走完**。两者不重叠：Go 测试全绿而作答页白屏，是完全可能的。
