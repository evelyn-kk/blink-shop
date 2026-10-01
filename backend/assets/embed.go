// Package assets 内嵌随程序发布的静态资源，经 GET /api/v1/assets/{path...} 公开读取。
// catalog/ 下是演示商品图和商家 logo，由 go run ./cmd/gen-sample-images 生成。
package assets

import "embed"

//go:embed catalog
var FS embed.FS
