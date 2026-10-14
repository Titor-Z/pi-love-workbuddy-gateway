// Package config 负责解析启动参数并产出运行期配置。
package config

import (
	"flag"
	"fmt"
	"io"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
)

// Config 是网关的运行期配置。
type Config struct {
	// ListenAddr 是 API 监听地址，形如 127.0.0.1:8788。
	// 只提供 /v1/* 与 /health，pi 的 baseUrl 指向这里。
	ListenAddr string
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
	return cfg, nil
}
