// Package config 负责解析启动参数并产出运行期配置。
//
// 命令行只有两个子命令和两个参数：
//
//	pi-love-workbuddy-gateway [gateway] -d -addr 0.0.0.0:8788
//	pi-love-workbuddy-gateway client  -d -addr 192.168.1.5:8788
//
// gateway 是默认（不写就是它）：本机持有 WorkBuddy 凭证，直连后端，
// 并开放局域网端口给其他电脑连入。
//
// client 是其他电脑用的模式：本机**不持有凭证**，把请求转发给网关，
// 凭证由网关同步过来、加密存在本机，仅在切到网关模式时才使用。
//
// 两者的关键差别不是「谁连谁」，而是**谁持有凭证**：
// 全网只有网关一个 refreshToken，所以多设备同时请求不会互相把对方踢下线。
package config

import (
	"flag"
	"fmt"
)

// 运行模式。
const (
	ModeGateway = "gateway"
	ModeClient  = "client"
)

// 端口布局。三个口是固定的，不是随便定的：
//
//   - 本机明文 HTTP：pi 连它。pi 配的是 http://，改成 TLS 就得动 models.json。
//   - 局域网 TLS：其他电脑的 client 连它，带 PSK 与证书指纹两道关。
//   - 控制台：只绑本机，绝不能对外——它含余额、请求日志与停止/重启按钮。
//
// 同一个端口不能又跑明文又跑 TLS（那要靠猜首字节，SSE 长连接下猜错极难复现），
// 所以必须是三个。
var (
	// localPort 是本机 pi 用的明文 HTTP 端口，**所有机器都一样**。
	// 这是刻意的：pi 的 models.json 在哪台机器上都不用改。
	// 网关模式下它与对外端口是同一个号，只是绑在 loopback 上。
	localPort = 8788
	// consolePort 是控制台端口，固定只绑本机。
	consolePort = 8789
	// defaultRelayPort 是网关对外服务的默认端口。
	// 其他电脑填「这台机器的 IP:这个端口」。
	defaultRelayPort = 8788
)

// localAddr 是本机 pi 要连的地址。
func localAddr() string { return fmt.Sprintf("127.0.0.1:%d", localPort) }

// LocalAddr 返回本机 pi 要连的地址（明文 HTTP），供文档与面板引用。
func LocalAddr() string { return localAddr() }

// ConsoleAddr 返回控制台监听地址。
func ConsoleAddr() string { return fmt.Sprintf("127.0.0.1:%d", consolePort) }

// MaxConcurrent 是打到后端的并发上限。
//
// 多设备共用一个 WorkBuddy 账号时并发会翻倍，而后端本来就有限流
// （实测 HTTP 429）。在这里排队比让后端随机拒绝好解释得多。
const MaxConcurrent = 4

// Config 是网关的运行期配置。
type Config struct {
	// Mode 是运行模式：gateway（默认）或 client。
	Mode string
	// ListenAddr 是本机 pi 要连的地址。固定值，两种模式都一样。
	ListenAddr string
	// ConsoleAddr 是控制台监听地址。固定值，只绑本机。
	ConsoleAddr string
	// GatewayAddress 只对 client 模式有意义：要连的那台网关，
	// 形如 192.168.1.5:8788。它来自 -addr。
	GatewayAddress string
	// RelayAddr 只对 gateway 模式有意义：对外服务的地址，来自 -addr。
	// 其他电脑填「这台机器的 IP:它的端口」。
	RelayAddr string
	// Daemon 为真时脱离终端在后台运行。
	Daemon bool
}

// Parse 从命令行参数解析配置。args 不含程序名本身。
func Parse(args []string) (*Config, error) {
	// 先认子命令：不写就是 gateway。
	mode := ModeGateway
	if len(args) > 0 && (args[0] == ModeGateway || args[0] == ModeClient) {
		mode = args[0]
		args = args[1:]
	}

	cfg := &Config{Mode: mode, ListenAddr: localAddr(), ConsoleAddr: ConsoleAddr()}
	fs := flag.NewFlagSet("pi-love-workbuddy-gateway", flag.ContinueOnError)
	// 出错时不打印一大段用法：Parse 的调用方会决定怎么报。
	fs.SetOutput(discard{})
	fs.Usage = func() {}

	var addr string
	fs.StringVar(&addr, "addr", "", "gateway 模式：对外服务地址，默认 0.0.0.0:"+
		fmt.Sprint(defaultRelayPort)+"；client 模式：要连的网关地址，形如 192.168.1.5:8788")
	fs.BoolVar(&cfg.Daemon, "d", true, "后台运行（脱离终端）；在终端里看日志用 -d=false")

	if err := fs.Parse(args); err != nil {
		// flag 自己的报错只说「没有这个参数」，不告诉用户有什么。
		// 一并带上用法，省一轮来回。
		return nil, fmt.Errorf("%v\n\n%s", err, Usage())
	}
	if fs.NArg() > 0 {
		return nil, errUnknownArgument(fs.Args()[0])
	}

	if mode == ModeClient {
		if addr == "" {
			return nil, errClientNeedsAddress()
		}
		cfg.GatewayAddress = addr
		return cfg, nil
	}
	if addr == "" {
		addr = fmt.Sprintf("0.0.0.0:%d", defaultRelayPort)
	}
	cfg.RelayAddr = addr
	return cfg, nil
}

// discard 是一个吞掉输出的 io.Writer，用于抑制 flag 包自带的用法打印。
// 用法由 Usage() 统一给出，不在这里重复一遍。
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
