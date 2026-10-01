// Package migrations 内嵌版本化 SQL 迁移文件。
// 文件名格式为 NNNN_描述.sql，按版本号顺序执行；已发布的文件只能追加新版本，不能修改。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
