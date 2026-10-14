// Package config 负责解析启动参数并产出运行期配置。
//
// 命令行刻意只留三个参数：`-d`、`-addr`，以及 `--help`。
//
// 其余一切都在控制台里完成。理由：这个工具的用法是「启动一次，然后一直用
// 面板」。把开关堆在命令行上的后果是每次换机器都要翻 README，
// 而面板就在那里，看得见、点得到、还能说明每一项的后果。
package config

import (
	"flag"
	"fmt"
	"io"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
)

// 运行模式。
//
// gateway 是默认模式：本机持有 WorkBuddy 凭证，直连后端，
// 并开放 relay 端口给局域网里的客户端连入。
//
// client 是子电脑用的模式：本机**不持有凭证**，把请求转发给网关，
// 凭证由网关同步过来、加密存在本机，仅在切到网关模式时才被使用。
//
// 两者的关键差别不是「谁连谁」，而是**谁持有凭证**：
// 全网只有网关一个 refreshToken，所以多设备同时请求不会互相把对方踢下线。
const (
	ModeGateway = "gateway"
	ModeClient  = "client"
)

// 本机 API 与控制台的默认地址。
//
// 声明成变量而不是常量，只为让测试能在同一台机器上同时起
// 网关与客户端（两者默认都要绑 8788）。生产默认值就是下面这两个，
// 不通过任何参数暴露——pi 连的必须始终是 127.0.0.1:8788。
var (
	defaultListenAddr  = "127.0.0.1:8788"
	defaultConsoleAddr = "127.0.0.1:8789"
)

// Config 是网关的运行期配置。
//
// 命令行只决定两件事：跑哪种模式、API 端口绑在哪。
// 其它参数（转发开关、凭证同步开关、并发上限、设备名）都收在面板里，
// 因为它们需要在界面上说明后果，而不是藏在 man page 里。
type Config struct {
	// Mode 是运行模式：gateway（默认）或 client。
	Mode string
	// ListenAddr 是本机 API 的监听地址，形如 127.0.0.1:8788。
	// **两种模式都一样**：客户端自己也是一个完整网关，pi 配的就是它。
	ListenAddr string
	// GatewayAddress 只对 client 模式有意义：要连的那台网关，形如 192.168.1.5:8790。
	// 它来自 -addr，与 ListenAddr 是两件事——混在一起会让客户端
	// 试图监听网关的地址，把网关自己顶掉。
	GatewayAddress string
	// ConsoleAddr 是 Web 控制台的监听地址，只听 127.0.0.1。
	// 与 API 分属两个端口：控制台含余额、请求日志与停止/重启按钮，
	// 不能跟着「可能被改成 0.0.0.0 的那个端口」一起暴露到局域网。
	ConsoleAddr string
	// Daemon 为真时脱离终端在后台运行。
	// **默认开启**：本项目带 Web 面板，主要用法是启动一次后长期在后台跑，
	// 之后全部操作面板完成；想在终端里盯着日志时用 `-d=false`。
	Daemon bool
}

// Parse 从命令行参数解析配置。args 不含程序名本身。
func Parse(args []string, errOut io.Writer) (*Config, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet("pi-love-workbuddy-gateway", flag.ContinueOnError)
	fs.SetOutput(errOut)

	fs.StringVar(&cfg.Mode, "mode", ModeGateway,
		"运行模式：gateway（默认，本机持有凭证）或 client（转发给网关，本机不持有凭证）")
	fs.StringVar(&cfg.ListenAddr, "addr", "",
		"gateway 模式：本机 API 端口，默认 127.0.0.1:8788；"+
			"client 模式：要连的网关地址，形如 192.168.1.5:8790")
	fs.StringVar(&cfg.ConsoleAddr, "console-addr", defaultConsoleAddr, "Web 控制台监听地址，只绑本机")
	fs.BoolVar(&cfg.Daemon, "d", true,
		"后台运行（脱离终端）；在终端里看日志请用 -d=false")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("无法识别的参数：%v（本程序只有 -mode / -addr / -d 三个参数，其余都在控制台里）", fs.Args())
	}

	// 客户端模式的 -addr 是「要连的网关地址」，语义与网关模式的本地端口不同，
	// 所以默认值也由模式决定。
	// -addr 的含义由模式决定，但本机 API 端口固定：
	// 客户端也是完整网关，pi 连的始终是本机的 8788。
	if cfg.Mode == ModeClient {
		cfg.GatewayAddress = cfg.ListenAddr
		cfg.ListenAddr = defaultListenAddr
		if cfg.GatewayAddress == "" {
			return nil, fmt.Errorf("客户端模式必须用 -addr 指定网关地址，形如 -addr 192.168.1.5:8790")
		}
	} else if cfg.ListenAddr == "" {
		cfg.ListenAddr = defaultListenAddr
	}
	if cfg.ConsoleAddr == "" {
		return nil, fmt.Errorf("控制台监听地址不能为空")
	}
	if cfg.Mode != ModeGateway && cfg.Mode != ModeClient {
		return nil, fmt.Errorf("未知运行模式 %q，只支持 %s 与 %s", cfg.Mode, ModeGateway, ModeClient)
	}
	return cfg, nil
}

// RelayAddr 是 relay 端口（网关模式）。固定 0.0.0.0:8790：
// 它必须能被局域网里的客户端连到，而它有自己的三重准入
// （PSK + TLS 指纹 + 设备吊销），不需要用户去选地址。
func RelayAddr() string { return "0.0.0.0:8790" }

// MaxConcurrent 是打到后端的并发上限。
//
// 多设备共用一个 WorkBuddy 账号时并发会翻倍，而后端本来就有限流
// （实测 HTTP 429）。在这里排队比让后端随机拒绝好解释得多。
// 面板可改，这里是默认值。
const MaxConcurrent = 4

// PromotionsPath 是促销配置文件的默认路径。
func PromotionsPath() string { return appdir.PromotionsPath() }
