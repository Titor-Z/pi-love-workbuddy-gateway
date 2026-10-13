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
	// ListenAddr 是本地监听地址，形如 127.0.0.1:8788。
	ListenAddr string
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
	// PiOutput 仅在 `models` 子命令下有意义：为真时输出 pi 的 models.json，
	// 否则只打印人看的表格。
	PiOutput bool
}

// Parse 从命令行参数解析配置。args 不含程序名本身。
func Parse(args []string, errOut io.Writer) (*Config, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet("pi-love-workbuddy-gateway", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.StringVar(&cfg.ListenAddr, "addr", "127.0.0.1:8788", "本地监听地址")
	fs.StringVar(&cfg.APIKey, "api-key", "", "本地访问密钥，留空表示不校验")
	fs.StringVar(&cfg.AuthFile, "auth-file", "", "WorkBuddy 登录态文件路径，留空自动探测")
	fs.StringVar(&cfg.AtRestSecret, "at-rest-secret", "", "at-rest 根密钥（44 字符 base64），留空自动获取")
	fs.StringVar(&cfg.LogPath, "log", appdir.LogPath(), "请求日志路径，留空只写标准输出")
	fs.StringVar(&cfg.Promotions, "promotions", "", "促销配置文件路径，留空用默认位置")
	fs.BoolVar(&cfg.PiOutput, "pi", false, "（仅 models 子命令）输出 pi 的 models.json")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if cfg.ListenAddr == "" {
		return nil, fmt.Errorf("监听地址不能为空")
	}
	return cfg, nil
}
