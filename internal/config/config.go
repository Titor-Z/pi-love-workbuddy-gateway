// Package config 负责解析启动参数并产出运行期配置。
package config

import (
	"flag"
	"fmt"
	"io"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
)

// 运行模式。gateway 持有凭证直连后端；client 把请求转发给局域网里的网关。
//
// 两者的关键差别不是「谁连谁」，而是**谁持有凭证**：
// 全网只有网关一个 refreshToken，所以多设备同时请求不会互相把对方踢下线。
// 这是整个双模式设计真正的收益所在。
const (
	ModeGateway = "gateway"
	ModeClient  = "client"
)

// Config 是网关的运行期配置。
type Config struct {
	// ListenAddr 是 API 监听地址，形如 127.0.0.1:8788。
	// 只提供 /v1/* 与 /health，pi 的 baseUrl 指向这里。
	ListenAddr string
	// MaxConcurrent 是打到后端去的并发上限。
	// 多设备共用一个 WorkBuddy 账号时并发会翻倍，而后端本来就有限流
	// （实测 HTTP 429）。在这里排队，比让后端随机拒绝更好解释。
	// 0 表示不限。
	MaxConcurrent int

	// Mode 是运行模式：gateway 持有 WorkBuddy 凭证并直连后端；
	// client 把请求转发给局域网里的网关，本机**不持有凭证**。
	Mode string
	// GatewayURL 是 client 模式下要连的网关地址，形如 https://192.168.1.5:8790。
	GatewayURL string
	// PSK 覆盖 Supervisor 生成的预共享密钥；留空则用持久化在配置目录里的那份。
	PSK string
	// RelayAddr 是 gateway 模式下供客户端连接的地址，形如 0.0.0.0:8790。
	RelayAddr string
	// RelayEnabled 控制是否开放 relay 端口。默认关：多设备共享不是人人都需要，
	// 默认开会让每台网关多一个对外监听口。
	RelayEnabled bool
	// DeviceName 覆盖默认的设备名（主机名），便于在网关那侧认得出这台机器。
	DeviceName string
	// Probe 为真时只去取网关证书的指纹，打印后退出。
	// 这一步的语义是「让用户看清这把钥匙」，不建立信任。
	Probe bool
	// Trust 是要确认为可信的网关证书指纹，通常与 -gateway 一起用一次。
	Trust string

	// ConsoleAddr 是 Web 控制台的监听地址，形如 127.0.0.1:8789。
	// v0.3.0 起控制台不再与 API 共用端口：控制台含余额、请求日志与停止/重启，
	// 必须与「允许对外暴露」的那个端口分开。
	ConsoleAddr string
	// APIKey 非空时，本地客户端必须携带同一个密钥才能访问。
	APIKey string
	// AuthFile 是 WorkBuddy 登录态文件路径；留空则由 authdir.Locate 自动探测。
	AuthFile string
	// AtRestSecret 是解密登录态用的 at-rest 根密钥（44 字符 base64）；
	// 留空时依次尝试环境变量与 WorkBuddy 原生层。
	AtRestSecret string
	// LogPath 是请求日志文件路径；默认为配置目录下的 gateway.log，
	// 显式传空串则只写标准输出。
	LogPath string
	// Promotions 是本地促销配置文件路径，用于补上后端不提供的免费截止日期；
	// 留空时回退到默认位置。
	Promotions string
	// JournalPath 是结构化事件文件（JSONL）的路径，供 Web 面板统计；
	// 与 -log 的文本日志职责不同，两者可以同时存在。
	JournalPath string
	// PiOutput 仅在 `models` 子命令下有意义：为真时输出 pi 的 models.json，
	// 否则只打印人看的表格。
	PiOutput bool
	// Daemon 为真时脱离终端在后台运行，由父进程派生一个子进程后立即退出。
	// **默认开启**：本项目带 Web 面板，主要用法是双击一下就长期在后台跑，
	// 之后全部操作面板完成；想在终端里盯着日志时用 `serve` 子命令或 `-d=false`。
	Daemon bool
}

// Parse 从命令行参数解析配置。args 不含程序名本身。
func Parse(args []string, errOut io.Writer) (*Config, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet("pi-love-workbuddy-gateway", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.StringVar(&cfg.ListenAddr, "addr", "127.0.0.1:8788", "API 监听地址（/v1/* 与 /health）")
	fs.StringVar(&cfg.ConsoleAddr, "console-addr", "127.0.0.1:8789", "Web 控制台监听地址，只绑本机")
	fs.StringVar(&cfg.APIKey, "api-key", "", "本地访问密钥，留空表示不校验")
	fs.StringVar(&cfg.AuthFile, "auth-file", "", "WorkBuddy 登录态文件路径，留空自动探测")
	fs.StringVar(&cfg.AtRestSecret, "at-rest-secret", "", "at-rest 根密钥（44 字符 base64），留空自动获取")
	fs.StringVar(&cfg.LogPath, "log", appdir.LogPath(), "请求日志路径，留空只写标准输出")
	fs.StringVar(&cfg.Promotions, "promotions", "", "促销配置文件路径，留空用默认位置")
	fs.StringVar(&cfg.JournalPath, "journal", appdir.JournalPath(), "结构化事件文件路径，面板读这个；留空则不记录")
	fs.BoolVar(&cfg.PiOutput, "pi", false, "（仅 models 子命令）输出 pi 的 models.json")
	fs.IntVar(&cfg.MaxConcurrent, "max-concurrent", 4, "打到后端的并发上限，超了排队；0 表示不限")
	fs.StringVar(&cfg.Mode, "mode", "gateway", "运行模式：gateway 直连后端（默认），client 转发给网关")
	fs.StringVar(&cfg.GatewayURL, "gateway", "", "客户端模式：网关地址，形如 https://192.168.1.5:8790")
	fs.StringVar(&cfg.PSK, "psk", "", "网关模式：留空用已保存的那份；客户端模式：要填入的网关密钥")
	fs.StringVar(&cfg.RelayAddr, "relay-addr", "0.0.0.0:8790", "转发服务监听地址（网关模式且 -relay 时生效）")
	fs.BoolVar(&cfg.RelayEnabled, "relay", false, "开放转发服务，供局域网里的客户端连接")
	fs.StringVar(&cfg.DeviceName, "device-name", "", "本设备名称，默认取主机名")
	fs.BoolVar(&cfg.Probe, "probe", false, "（客户端模式）取网关证书指纹并打印，退出")
	fs.StringVar(&cfg.Trust, "trust", "", "（客户端模式）确认为可信的网关证书指纹")
	fs.BoolVar(&cfg.Daemon, "d", true, "后台运行（脱离终端）；在终端里看日志请用 serve 子命令或 -d=false")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if cfg.ListenAddr == "" {
		return nil, fmt.Errorf("监听地址不能为空")
	}
	if cfg.ConsoleAddr == "" {
		return nil, fmt.Errorf("控制台监听地址不能为空")
	}
	if cfg.ListenAddr == cfg.ConsoleAddr {
		return nil, fmt.Errorf("控制台地址不能与 API 地址相同（%s）："+
			"控制台含余额、请求日志与停止/重启，必须与 API 端口分开", cfg.ListenAddr)
	}
	switch cfg.Mode {
	case ModeGateway:
		if cfg.RelayEnabled && cfg.RelayAddr == cfg.ListenAddr {
			return nil, fmt.Errorf("转发地址不能与 API 地址相同（%s）", cfg.RelayAddr)
		}
		if cfg.GatewayURL != "" {
			// gateway 模式持有自己的凭证，指定另一个网关是没有意义的组合，
			// 多半是把参数打错了。宁可报错也不要默默忽略。
			return nil, fmt.Errorf("网关模式不需要 -gateway，那个参数说的是客户端要去连谁")
		}
	case ModeClient:
		if cfg.GatewayURL == "" {
			return nil, fmt.Errorf("客户端模式必须用 -gateway 指定网关地址")
		}
		// 客户端不持有凭证，也不提供转发服务；这几个参数在这边没有意义。
		if cfg.RelayEnabled {
			return nil, fmt.Errorf("客户端模式不提供转发服务（-relay 只对网关模式有意义）")
		}
		if cfg.AuthFile != "" || cfg.AtRestSecret != "" {
			return nil, fmt.Errorf("客户端模式不读取 WorkBuddy 登录态（-auth-file / -at-rest-secret 无效）")
		}
	default:
		return nil, fmt.Errorf("未知运行模式 %q，只支持 %s 与 %s", cfg.Mode, ModeGateway, ModeClient)
	}
	return cfg, nil
}
