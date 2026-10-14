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

// LogFileName 是默认日志文件名（人类读的文本日志）。
const LogFileName = "gateway.log"

// JournalFileName 是默认事件文件名（面板读的 JSONL）。
const JournalFileName = "gateway.jsonl"

// ClientJournalFileName 是客户端模式的默认事件文件名。
//
// 与网关模式刻意不用同一个文件：同一台机器上两种模式可能同时跑（先切出去试
// 一试），跨进程就没有 Journal 那把进程内互斥锁了，写同一个文件会交错成坏行。
// 换文件名比引文件锁简单得多，也让「切换模式」这件事天然可解释。
const ClientJournalFileName = "client.jsonl"

// DeviceFileName 是本设备身份文件。设备 ID 与运行模式无关：
// 同一台机器切网关 / 客户端是同一个身份，否则网关的设备表会分裂出两个条目。
const DeviceFileName = "device.json"

// PSKFileName 是网关持有的预共享密钥。0600：它是客户端接入的唯一凭据。
const PSKFileName = "psk.txt"

// DevicesFileName 是网关侧的设备注册表。
const DevicesFileName = "devices.json"

// ClientFileName 是客户端侧的配对配置（网关地址、PSK、指纹）。
// 它间接持有另一台机器上账号的访问权限，权限要求与 PSK 文件相同。
const ClientFileName = "client.json"

// RelayCertFileName / RelayKeyFileName 是 relay 端口的自签证书与私钥。
const RelayCertFileName = "relay-cert.pem"
const RelayKeyFileName = "relay-key.pem"

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

// JournalPath 返回网关模式下事件文件（JSONL）的默认路径。
func JournalPath() string { return join(Dir(), JournalFileName) }

// ClientJournalPath 返回客户端模式下事件文件的默认路径。
// 与网关模式分开，两者互不干扰，也无需跨进程加锁。
func ClientJournalPath() string { return join(Dir(), ClientJournalFileName) }

// DevicePath 返回本设备身份文件的路径。
func DevicePath() string { return join(Dir(), DeviceFileName) }

// PSKPath 返回预共享密钥文件的路径。
func PSKPath() string { return join(Dir(), PSKFileName) }

// DevicesPath 返回设备注册表文件的路径。
func DevicesPath() string { return join(Dir(), DevicesFileName) }

// ClientPath 返回客户端配对配置文件的路径。
func ClientPath() string { return join(Dir(), ClientFileName) }

// RelayCertPath 返回 relay 证书文件的路径。
func RelayCertPath() string { return join(Dir(), RelayCertFileName) }

// RelayKeyPath 返回 relay 私钥文件的路径。
func RelayKeyPath() string { return join(Dir(), RelayKeyFileName) }

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
