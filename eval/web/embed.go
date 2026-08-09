// Package web 持有构建产物与 vendored 前端资源，全部 embed 进二进制。
//
// 三条纪律：
//  1. **不引用任何外部地址**。系统现场全程离线（CON-02），HLD 第 2 章的
//     约束表明确排除 CDN、外部字体、外部 API。daisyUI / htmx / Alpine
//     都以文件形式提交在 vendor/ 下。
//  2. **构建产物随仓库提交**。拿到代码直接 go run 即可，不需要任何工具链；
//     只有改样式的人才需要跑 tools/build-css.sh。
//  3. **作答端与管理端分两份 CSS**。作答端要下发到 200 部手机，受 HLD 9.3
//     的 20KB 预算约束；管理端在回环上，可以用完整的 daisyUI。
package web

import _ "embed"

//go:embed dist/admin.css
var AdminCSS string

//go:embed dist/eval.css
var EvalCSS string

//go:embed vendor/htmx.min.js
var HTMX string

//go:embed src/admin-extra.js
var AdminJS string

// Favicon 用 SVG：单文件、可缩放、几百字节，不需要额外工具生成多尺寸 ico。
// 不给 favicon 时浏览器会自己去请求 /favicon.ico 并吃一个 404。
//
//go:embed src/favicon.svg
var Favicon string

// Alpine 用的是 **CSP 构建**：表达式只能是属性名或方法名，不使用
// new Function()，因此作答页不必在 CSP 里开 'unsafe-eval'。
//
//go:embed vendor/alpine-csp.min.js
var Alpine string
