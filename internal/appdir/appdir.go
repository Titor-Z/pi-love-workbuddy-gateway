// Package appdir 统一本项目的配置与数据文件位置。
//
// 遵循 XDG 习惯：优先 $XDG_CONFIG_HOME，否则 ~/.config。
// **刻意不在 macOS 上改用 `~/Library/Application Support`**，
// 因为 Go 的 os.UserConfigDir() 在 macOS 上会返回后者，
// 导致三平台路径不一致、文档要写三份。文档里只写一种路径。
package appdir

import (
	"fmt"
	"os"
	"path/filepath"
)

// DirName 是本项目在用户配置根目录下的目录名。
const DirName = "piloveworkbuddy"

// PromotionsFileName 是促销配置的文件名。
const PromotionsFileName = "promotions.json"

// LogFileName 是默认日志文件名。
const LogFileName = "gateway.log"

// Root 返回用户配置根目录，即 $XDG_CONFIG_HOME 或 ~/.config。
func Root() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return base
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

// Dir 返回本项目的配置目录。
func Dir() string {
	root := Root()
	if root == "" {
		return ""
	}
	return filepath.Join(root, DirName)
}

// PromotionsPath 返回促销配置的默认路径。
func PromotionsPath() string { return join(Dir(), PromotionsFileName) }

// LogPath 返回日志文件的默认路径。
func LogPath() string { return join(Dir(), LogFileName) }

// EnsureDir 确保配置目录存在，返回目录路径。
func EnsureDir() (string, error) {
	dir := Dir()
	if dir == "" {
		return "", fmt.Errorf("无法确定配置目录：既没有 $XDG_CONFIG_HOME，也拿不到用户主目录")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建配置目录 %s 失败：%w", dir, err)
	}
	return dir, nil
}

// join 拼接目录与文件名；目录为空时返回空串，让调用方退回到默认行为。
func join(dir, name string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}
