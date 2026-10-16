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

// PromotionsFileName 是旧版促销配置的文件名。v0.9.0 起由覆盖表取代，
// 只作为一次性迁移的来源。
const PromotionsFileName = "promotions.json"

// ModelOverridesFileName 是模型覆盖表的文件名：别名、免费截止、备注、
// 隐藏、思考档位。这些字段后端都不提供，只能人工维护。
const ModelOverridesFileName = "model-overrides.json"

// LogFileName 是默认日志文件名（人类读的文本日志）。
const LogFileName = "gateway.log"

// JournalDirName 是事件分片目录名。
//
// 事件按天分片存放（<prefix>-YYYY-MM-DD.jsonl，prefix 为 gateway / client），
// 而不是全部追加进一个文件。理由：单文件会随时间无限膨胀，而「保留 N 天」
// 在分片下就是删掉几个文件（O(1)），不需要重写整个文件；读取时也只需打开
// 最近几个分片，不必把历史全量解析一遍。
const JournalDirName = "journal"

// JournalFileName 是 v0.8.0 及以前的事件文件名（面板读的 JSONL）。
// 现在只作为一次性迁移的来源，不再往里写。
const JournalFileName = "gateway.jsonl"

// ClientJournalFileName 是客户端模式旧版的事件文件名，同样只作迁移来源。
//
// 与网关模式刻意不用同一个文件：同一台机器上两种模式可能同时跑（先切出去试
// 一试），跨进程就没有 Journal 那把进程内互斥锁了，写同一个文件会交错成坏行。
// 换文件名比引文件锁简单得多，也让「切换模式」这件事天然可解释。
const ClientJournalFileName = "client.jsonl"

// SettingsFileName 是面板可改的运行偏好（日志条数、保留天数、签到设置）。
const SettingsFileName = "settings.json"

// CheckinFileName 记录签到结论，只用于展示，不作判断依据。
const CheckinFileName = "checkin.json"

// JournalPrefixGateway / JournalPrefixClient 是两种模式的事件分片文件名前缀。
// 分开是为了让同一台机器上两种模式各自的日志互不干扰。
const (
	JournalPrefixGateway = "gateway"
	JournalPrefixClient  = "client"
)

// CredentialEpochFileName 是网关侧的同步计数器。
//
// 单独一个文件，不放进 devices.json（那张表是观测记录，会被删了重来）
// 也不放进 relay.json（那是面板改的偏好）。删掉它 = 用户主动重置。
const CredentialEpochFileName = "credential-epoch"

// ClientCredentialFileName 是客户端本地的凭证副本。
// 权限 0600：它间接持有账号访问权。
const ClientCredentialFileName = "credentials.json"

// ClientAtRestFileName 是客户端自己的 at-rest 密钥。
// 与网关那套完全无关——它只加密本机的凭证副本。
const ClientAtRestFileName = "at-rest-secret"

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

// RelayPreferenceFileName 记录「是否开放转发服务」这一项偏好。
// 由面板改写、启动时读取。默认不存在 = 不开启：多设备共享是可选功能。
const RelayPreferenceFileName = "relay.json"

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

// PromotionsPath 返回旧版促销配置的默认路径，仅用于一次性迁移。
func PromotionsPath() string { return join(Dir(), PromotionsFileName) }

// ModelOverridesPath 返回模型覆盖表的默认路径。
func ModelOverridesPath() string { return join(Dir(), ModelOverridesFileName) }

// ModelOverridesBaseFileName 是「上次同步时网关那一份」的副本。
// 三方合并要拿它当共同祖先——少了它就只能比时间戳，而跨机器的时钟不可靠。
const ModelOverridesBaseFileName = "model-overrides.base.json"

// ModelOverridesBasePath 返回同步基准的默认路径。
func ModelOverridesBasePath() string { return join(Dir(), ModelOverridesBaseFileName) }

// LogPath 返回日志文件的默认路径。
func LogPath() string { return join(Dir(), LogFileName) }

// JournalDir 返回事件分片目录。
func JournalDir() string { return join(Dir(), JournalDirName) }

// SettingsPath 返回运行偏好文件的默认路径。
func SettingsPath() string { return join(Dir(), SettingsFileName) }

// CheckinPath 返回签到记录的存放路径。
func CheckinPath() string { return join(Dir(), CheckinFileName) }

// JournalPath 返回旧版网关模式单文件事件的路径，仅用于一次性迁移。
func JournalPath() string { return join(Dir(), JournalFileName) }

// ClientJournalPath 返回旧版客户端模式单文件事件的路径，同样只作迁移来源。
func ClientJournalPath() string { return join(Dir(), ClientJournalFileName) }

// CredentialEpochPath 返回同步计数器文件路径。
func CredentialEpochPath() string { return join(Dir(), CredentialEpochFileName) }

// ClientCredentialPath 返回客户端凭证副本路径。
func ClientCredentialPath() string { return join(Dir(), ClientCredentialFileName) }

// LocalAuthFileName 是客户端切到网关模式后写的本地登录态文件名。
const LocalAuthFileName = "workbuddy-desktop.info"

// LocalAuthPath 返回客户端切到网关模式后的登录态路径。
// 与 WorkBuddy 客户端同名，credential.Locate 能直接探测到它。
func LocalAuthPath() string { return join(Dir(), LocalAuthFileName) }

// ClientAtRestPath 返回客户端自己的 at-rest 密钥路径。
func ClientAtRestPath() string { return join(Dir(), ClientAtRestFileName) }

// DevicePath 返回本设备身份文件的路径。
func DevicePath() string { return join(Dir(), DeviceFileName) }

// PSKPath 返回预共享密钥文件的路径。
func PSKPath() string { return join(Dir(), PSKFileName) }

// DevicesPath 返回设备注册表文件的路径。
func DevicesPath() string { return join(Dir(), DevicesFileName) }

// ClientPath 返回客户端配对配置文件的路径。
func ClientPath() string { return join(Dir(), ClientFileName) }

// RelayPreferencePath 返回转发服务偏好文件的路径。
func RelayPreferencePath() string { return join(Dir(), RelayPreferenceFileName) }

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
