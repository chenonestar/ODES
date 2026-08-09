// Package migrations 把 goose 迁移脚本 embed 进二进制（HLD 技术选型表）。
//
// 迁移脚本随二进制走、启动时自动执行，是"单文件拷贝即用"的一部分：
// 现场不存在"先跑一遍迁移工具"这个步骤。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
