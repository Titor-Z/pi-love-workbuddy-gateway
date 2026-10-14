package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/daemon"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/gateway"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/netif"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/panel"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relaytls"
)

// shutdownTimeout 是收到中断信号后等待在途请求结束的时间。
const shutdownTimeout = 10 * time.Second

// commandModels 是「打印模型清单」子命令；不写子命令时默认启动服务。
const commandModels = "models"

// commandServe 是「前台运行」子命令。
// 默认是后台运行，这个子命令用于在终端里盯着日志。
const commandServe = "serve"

// commandBilling 是「查看余额」子命令。
const commandBilling = "billing"

// version 是编译期注入的版本号（-ldflags "-X main.version=..."），
// 直接 go build 时为 dev。
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
		os.Exit(1)
	}
}

// run 解析参数并按子命令分发。
func run(args []string) error {
	// -help / --help / -h 打用法；这是唯一一个不属于「两个子命令两个参数」
	// 的入口，因为它必须存在。
	if wantsHelp(args) {
		fmt.Print("\n" + config.Usage())
		return nil
	}
	if hasVersionFlag(args) {
		fmt.Printf("pi-love-workbuddy-gateway %s\n", version)
		return nil
	}
	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}
	// 后台模式：派生一个脱离终端的子进程，父进程随即退出。
	// 子进程带着 daemon.IsChild() 为真，因此不会再次派生。
	if cfg.Daemon && !daemon.IsChild() {
		return runInBackground(cfg, args)
	}
	return runServe(cfg, args)
}

// runInBackground 派生后台进程并把 PID 报给用户，方便随时停掉。
func runInBackground(cfg *config.Config, args []string) error {
	pid, err := daemon.Start(args, appdir.LogPath())
	if err != nil {
		return err
	}
	fmt.Printf("已转入后台，PID %d\n", pid)
	fmt.Printf("日志：%s\n", appdir.LogPath())
	fmt.Printf("控制台：http://%s/\n", cfg.ConsoleAddr)
	fmt.Printf("停止：kill %d\n", pid)
	return nil
}

// runServe 装配各模块并启动 HTTP 服务，直到收到中断信号。
func runServe(cfg *config.Config, args []string) error {
	// 后台运行时子进程的标准输出已指向日志文件，再写一遍会让每行重复。
	logger, err := logging.New(appdir.LogPath(), !daemon.IsChild())
	if err != nil {
		return err
	}
	defer logger.Close()

	promotions, err := catalog.LoadPromotions(appdir.PromotionsPath())
	if err != nil {
		return err
	}
	events, err := journal.Open(journalPathForMode(cfg))
	if err != nil {
		return err
	}
	defer events.Close()

	// 两种模式的差别只在这里：谁去取数据。
	// 之后的一切（网关 Handler、面板、HTTP 服务）完全一样。
	parts, err := buildRuntime(cfg, logger)
	if err != nil {
		return err
	}
	startedAt := time.Now()

	// 并发闸门只对持有凭证的那一侧有意义：
	// 客户端模式的请求在网关那里已经排过队，这里再限一次
	// 只会让人以为网关坏了。
	// 并发闸门只对持有凭证的那一侧有意义：
	// 客户端模式的请求在网关那里已经排过队，这里再限一次
	// 只会让人以为网关坏了。
	maxConcurrent := config.MaxConcurrent
	if cfg.Mode == config.ModeClient {
		maxConcurrent = 0
	}
	gatewayHandler := gateway.NewServer(cfg, parts.credential, parts.backend, promotions, logger, events, maxConcurrent)
	control := panel.NewControl()
	console, err := panel.New(panel.Deps{
		Version:        version,
		StartedAt:      startedAt,
		Mode:           cfg.Mode,
		RelayAddr:      cfg.RelayAddr,
		RelayEnabled:   parts.relay != nil,
		TLSFingerprint: parts.fingerprint,
		PSKHint:        parts.pskHint(),
		Devices:        parts.devices,
		Device:         parts.device,
		Syncer:         parts.syncer,
		PairingErr:     parts.pairingErr,
		PairingAddress: parts.address,
		AuthFile:       parts.authFile,
		ListenAddr:     cfg.ListenAddr,
		ConsoleAddr:    cfg.ConsoleAddr,
		LogPath:        appdir.LogPath(),
		JournalPath:    events.Path(),
		PromotionsPath: appdir.PromotionsPath(),
		Credential:     parts.credential,
		Upstream:       parts.backend,
		Billing:        parts.billing,
		Promotions:     promotions,
		Journal:        events,
		Logger:         logger,
		Control:        control,
	})
	if err != nil {
		return err
	}

	// 端口布局：同一个端口号，两种绑定。
	//
	//	127.0.0.1:<端口>   明文 HTTP，只给本机的 pi。pi 的配置在哪台机器上都不变。
	//	<局域网IP>:<端口>  TLS + 密钥，给其他电脑的客户端连。
	//
	// 为什么不直接绑 0.0.0.0：那会把 127.0.0.1 一起占掉，本机 pi 就没地方放了。
	// 为什么不干脆两个端口：用户只需要记一个号，其他电脑填「电脑A:这个号」。
	apiMux := http.NewServeMux()
	apiMux.Handle("/v1/", gatewayHandler.Handler())
	apiMux.Handle("/health", gatewayHandler.Handler())
	apiMux.HandleFunc("/", apiRootNotice(cfg.ConsoleAddr))

	apiServer := &http.Server{Handler: apiMux, ReadHeaderTimeout: 10 * time.Second}
	consoleServer := &http.Server{
		Addr:              cfg.ConsoleAddr,
		Handler:           console.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 本机 pi 的口：明文，只绑 loopback。
	localListener, err := listenOne(cfg.ListenAddr)
	if err != nil {
		return err
	}

	// 对外服务的口：TLS，逐个局域网网卡。
	var relayListeners []net.Listener
	var relayServer *http.Server
	if parts.relay != nil {
		addresses, expandErr := netif.ListenAddrs(cfg.RelayAddr)
		if expandErr != nil {
			return expandErr
		}
		parts.relay.SetBackend(gatewayHandler.Handler())
		relayServer = &http.Server{
			Handler:           parts.relay.Handler(),
			TLSConfig:         relaytls.ServerConfig(parts.certificate),
			ReadHeaderTimeout: 10 * time.Second,
		}
		for _, address := range addresses {
			listener, listenErr := listenOne(address)
			if listenErr != nil {
				return listenErr
			}
			relayListeners = append(relayListeners, tls.NewListener(listener, relayServer.TLSConfig))
			logger.Printf("对外    https://%s", address)
		}
	}

	logger.Printf("版本：%s", version)
	if parts.credential != nil {
		logger.Printf("登录态：%s", parts.authFile)
		logger.Printf("密钥指纹：%s", parts.keyID)
	}
	logger.Printf("日志：%s", describeLogPath(appdir.LogPath()))
	logger.Printf("事件：%s", describeLogPath(events.Path()))
	logger.Printf("pi 连   http://%s/v1", cfg.ListenAddr)
	logger.Printf("控制台  http://%s/", cfg.ConsoleAddr)
	if !isLoopbackAddr(cfg.ConsoleAddr) {
		// 控制台含余额、请求日志（含被拦对话正文）与停止/重启按钮，
		// 绑到非本机地址等于把这些一起开放出去。允许，但必须明说。
		logger.Printf("⚠️ 控制台未只绑本机：余额、请求日志与停止/重启按钮已暴露到该地址，请自行加认证")
	}
	if cfg.Mode == config.ModeClient {
		logger.Printf("转发到  %s", cfg.GatewayAddress)
	}
	if parts.relay != nil {

		logger.Printf("网关密钥：%s", parts.pskHint())
	}
	logger.Printf("提示：本软件为第三方工具，与腾讯 / WorkBuddy 无官方关联，" +
		"使用风险自负；完整条款见 README 的《免责声明》。")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	events.Append(journal.Record{Kind: journal.KindStart, Version: version,
		Message: fmt.Sprintf("启动 %s，监听 %s", version, cfg.ListenAddr)})

	// 配对请求要建 TLS 连接才能取指纹，那件事在 relaytls 里。
	// 注入而不是让面板直接依赖，是为了让面板不必知道连接细节。
	panel.SetProbeFingerprint(func(address string) (string, error) {
		host := strings.TrimPrefix(strings.TrimPrefix(address, "https://"), "http://")
		return relaytls.ProbeFingerprint(ctx, host)
	})

	// 客户端模式：后台同步凭证。网关模式没有这一步——凭证本来就在本机。
	if parts.syncer != nil {
		go runCredentialSync(ctx, parts.syncer, logger, events)
		panel.SetSwitchExecutor(SwitchToGateway)
		panel.SetSwitchChecker(func() (bool, string, int64, time.Time) {
			problem, file, _, err := CheckSwitchToGateway()
			if err != nil {
				return false, err.Error(), file.Epoch, file.SyncedAt
			}
			return problem == problemNone, problem.String(), file.Epoch, file.SyncedAt
		})
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- apiServer.Serve(localListener) }()
	go func() {
		if err := consoleServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("控制台监听失败：%w", err)
		}
	}()
	for _, listener := range relayListeners {
		go func(listener net.Listener) {
			// 对外这个口服务的是 relay handler（带 PSK 准入），
			// 不是本机那套 apiMux——否则局域网里的任何人都能用你的账号。
			if err := relayServer.Serve(listener); err != nil &&
				!errors.Is(err, http.ErrServerClosed) {
				serveErr <- fmt.Errorf("对外服务监听失败：%w", err)
			}
		}(listener)
	}

	// 等三种事：服务出错、收到退出信号、或者面板请求停止/重启。
	var action panel.Action
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Printf("收到退出信号，正在关闭")
	case action = <-control.Actions():
	}

	// 先把 HTTP 服务关掉再动手：Shutdown 会等本次响应发完，
	// 而且它会释放端口，重启时新进程才能立刻绑上。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// 控制台不对外，先关；其余可能有在途的流式请求，留到最后。
	_ = consoleServer.Shutdown(shutdownCtx)
	if relayServer != nil {
		_ = relayServer.Shutdown(shutdownCtx)
	}
	shutdownErr := apiServer.Shutdown(shutdownCtx)

	switch action {
	case panel.ActionRestart:
		events.Append(journal.Record{Kind: journal.KindStop, Message: "面板请求重启"})
		return restartSelf(cfg, args, logger, events)
	case panel.ActionStop:
		events.Append(journal.Record{Kind: journal.KindStop, Message: "面板请求停止"})
		logger.Printf("已按面板请求停止")
	default:
		events.Append(journal.Record{Kind: journal.KindStop, Message: "收到退出信号，正在关闭"})
	}
	return shutdownErr
}

// restartSelf 在退出前把新的自己派生出来，实现面板上的「重启」。
//
// 不用 exec 替换自身：那样能保住 PID，但要求调用发生在主线程，
// 且 Windows 上不可用。派生新进程 + 退出是跨平台一致的写法。
// 代价是 PID 会变，因此面板不显示「固定 PID」，只显示当前进程。
func restartSelf(cfg *config.Config, args []string, logger *logging.Logger,
	events *journal.Journal) error {

	logger.Printf("正在重启：派生新进程")
	if _, err := daemon.Start(args, appdir.LogPath()); err != nil {
		// 重启失败要及时报出来——这时服务已经停了，
		// 用户面对的是一片空白，必须知道该手工拉起来。
		logger.Printf("✗ 重启失败：%v", err)
		events.Append(journal.Record{Kind: journal.KindError,
			Message: "重启失败：" + err.Error(), Error: err.Error()})
		return fmt.Errorf("重启失败：%w", err)
	}
	logger.Printf("新进程已接管，本进程退出")
	return nil
}

// apiRootNotice 是 API 端口根路径的响应。
//
// v0.3.0 起控制台搬到了独立端口。老用户习惯打开 http://127.0.0.1:8788/ 找面板，
// 如果这里直接 404，升级后第一反应会是「服务坏了」。所以如实告诉他去哪儿。
func apiRootNotice(consoleAddr string) http.HandlerFunc {
	body := "pi-love-workbuddy-gateway\n\n" +
		"这里是 API 端口，只提供 /v1/* 与 /health。\n" +
		"Web 控制台已迁到独立端口：http://" + consoleAddr + "/\n"
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			writeNotFound(w, r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
}

// writeNotFound 是 API 端口上未匹配路径的统一响应。
func writeNotFound(w http.ResponseWriter, path string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "未找到："+path+"\n本端口只提供 /v1/* 与 /health，控制台在另一个端口。\n")
}

// isLoopbackAddr 判断监听地址是不是只绑本机。
// 只认最常见的三种写法：127.*、localhost、[::1]；其余一律当作「非本机」，
// 宁可多警告一次，也不要因为漏判而把控制台静默暴露出去。
func isLoopbackAddr(addr string) bool {
	host := addr
	if index := strings.LastIndex(host, ":"); index >= 0 && !strings.Contains(host[index+1:], "]") {
		host = host[:index]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || strings.HasPrefix(host, "127.") ||
		host == "localhost" || host == "::1"
}

// describeLogPath 把「不落盘」这种情况说清楚，而不是让日志行以空值结尾。
func describeLogPath(path string) string {
	if path == "" {
		return "（未启用，只写标准输出）"
	}
	return path
}

// wantsHelp 判断命令行里有没有请求打印用法。
func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "-help" || arg == "--help" {
			return true
		}
	}
	return false
}

// hasVersionFlag 判断命令行里有没有请求打印版本号。
func hasVersionFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-version" || arg == "--version" {
			return true
		}
	}
	return false
}
