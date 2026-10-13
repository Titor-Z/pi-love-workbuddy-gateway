// Package webui 承载前端构建产物。
//
// 前端源码在仓库的 web/ 目录，构建输出到本目录下的 dist——
// 因为 go:embed 不能引用上级目录，嵌入的资源必须由拥有它的那个包自己声明。
// 构建产物会提交进仓库，这样 `go build` 不依赖 Node 也能直接出二进制。
package webui

import "embed"

// Dist 是前端构建产物。
//
// 用 all: 前缀是为了把 dist 下的所有文件都收进来，
// 包括以 _ 或 . 开头的那些（默认会被 embed 跳过）。
//
//go:embed all:dist
var Dist embed.FS
