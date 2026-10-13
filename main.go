package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/daemon"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/gateway"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/panel"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
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
	if hasVersionFlag(args) {
		fmt.Printf("pi-love-workbuddy-gateway %s\n", version)
		return nil
	}
	if len(args) > 0 && args[0] == commandModels {
		cfg, err := config.Parse(args[1:], os.Stderr)
		if err != nil {
			return err
		}
		return runModels(cfg)
	}
	if len(args) > 0 && args[0] == commandBilling {
		cfg, err := config.Parse(args[1:], os.Stderr)
		if err != nil {
			return err
		}
		return runBilling(cfg)
	}
	if len(args) > 0 && args[0] == commandServe {
		// serve 的意图是「就在这个终端里跑起来」，所以它强制前台。
		args = args[1:]
		cfg, err := config.Parse(args, os.Stderr)
		if err != nil {
			return err
		}
		cfg.Daemon = false
		return runServe(cfg, args)
	}
	cfg, err := config.Parse(args, os.Stderr)
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
	pid, err := daemon.Start(args, cfg.LogPath)
	if err != nil {
		return err
	}
	fmt.Printf("已转入后台，PID %d\n", pid)
	if cfg.LogPath != "" {
		fmt.Printf("日志：%s\n", cfg.LogPath)
	}
	fmt.Printf("停止：kill %d\n", pid)
	return nil
}

// runServe 装配各模块并启动 HTTP 服务，直到收到中断信号。
func runServe(cfg *config.Config, args []string) error {
	// 后台运行时子进程的标准输出已指向日志文件，再写一遍会让每行重复。
	logger, err := logging.New(cfg.LogPath, !daemon.IsChild())
	if err != nil {
		return err
	}
	defer logger.Close()

	cred, cipher, err := buildCredential(cfg, logger.Printf)
	if err != nil {
		return err
	}
	promotions, err := catalog.LoadPromotions(resolvePromotionsPath(cfg))
	if err != nil {
		return err
	}
	events, err := journal.Open(cfg.JournalPath)
	if err != nil {
		return err
	}
	defer events.Close()

	// 网关与面板共用一个上游客户端：两条路径共用令牌续期与重试逻辑。
	client := upstream.NewClient(cred, "")
	startedAt := time.Now()

	gatewayHandler := gateway.NewServer(cfg, cred, client, promotions, logger, events).Handler()
	control := panel.NewControl()
	console, err := panel.New(panel.Deps{
		Version:        version,
		StartedAt:      startedAt,
		AuthFile:       cred.Path(),
		ListenAddr:     cfg.ListenAddr,
		LogPath:        cfg.LogPath,
		JournalPath:    cfg.JournalPath,
		PromotionsPath: resolvePromotionsPath(cfg),
		Credential:     cred,
		Upstream:       client,
		Billing:        billing.NewClient(cred, ""),
		Promotions:     promotions,
		Journal:        events,
		Logger:         logger,
		Control:        control,
	})
	if err != nil {
		return err
	}

	// 面板挂在根路径，网关的接口挂在各自的路径上。
	// /v1/* 与 /health 仍然只走网关那套 OpenAPI 兼容处理，不受面板影响。
	root := http.NewServeMux()
	root.Handle("/v1/", gatewayHandler)
	root.Handle("/health", gatewayHandler)
	root.Handle("/", console.Handler())

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Printf("版本：%s", version)
	logger.Printf("登录态：%s", cred.Path())
	logger.Printf("密钥指纹：%s", cipher.KeyID())
	logger.Printf("日志：%s", describeLogPath(cfg.LogPath))
	logger.Printf("事件：%s", describeLogPath(cfg.JournalPath))
	logger.Printf("监听：http://%s", cfg.ListenAddr)
	logger.Printf("    控制台    http://%s/", cfg.ListenAddr)
	logger.Printf("    GET  /health")
	logger.Printf("    GET  /v1/models")
	logger.Printf("    POST /v1/chat/completions")
	if cfg.APIKey != "" {
		logger.Printf("已启用本地访问密钥校验")
	}
	logger.Printf("提示：本软件为第三方工具，与腾讯 / WorkBuddy 无官方关联，" +
		"使用风险自负；完整条款见 README 的《免责声明》。")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	events.Append(journal.Record{Kind: journal.KindStart, Version: version,
		Message: fmt.Sprintf("启动 %s，监听 %s", version, cfg.ListenAddr)})

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()

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
	shutdownErr := httpServer.Shutdown(shutdownCtx)

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
	if _, err := daemon.Start(args, cfg.LogPath); err != nil {
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

// runModels 拉取实时模型目录并打印，可选输出成 pi 的 models.json。
func runModels(cfg *config.Config) error {
	cred, _, err := buildCredential(cfg, nil)
	if err != nil {
		return err
	}
	promotions, err := catalog.LoadPromotions(resolvePromotionsPath(cfg))
	if err != nil {
		return err
	}
	models, err := upstream.NewClient(cred, "").Models(context.Background())
	if err != nil {
		return err
	}
	entries := catalog.Build(models, promotions)
	if cfg.PiOutput {
		return catalog.WritePiConfig(os.Stdout, entries, piGatewayBaseURL(cfg), cfg.APIKey)
	}
	return writeTable(os.Stdout, entries)
}

// runBilling 读取并打印账号的资源包（余额）。
func runBilling(cfg *config.Config) error {
	cred, _, err := buildCredential(cfg, nil)
	if err != nil {
		return err
	}
	snapshot, err := billing.NewClient(cred, "").Fetch(context.Background())
	if err != nil {
		return err
	}
	return writeBilling(os.Stdout, snapshot)
}

// buildCredential 定位登录态并派生解密器。report 允许为 nil。
func buildCredential(cfg *config.Config, report credential.Reporter) (*credential.Manager, *atrest.Cipher, error) {
	authFile, err := credential.Locate(cfg.AuthFile)
	if err != nil {
		return nil, nil, err
	}
	cipher, err := atrest.Shared(cfg.AtRestSecret)
	if err != nil {
		return nil, nil, err
	}
	return credential.NewManager(authFile, cipher, report, ""), cipher, nil
}

// resolvePromotionsPath 给出促销配置文件的实际路径。
func resolvePromotionsPath(cfg *config.Config) string {
	if cfg.Promotions != "" {
		return cfg.Promotions
	}
	return appdir.PromotionsPath()
}

// describeLogPath 把「不落盘」这种情况说清楚，而不是让日志行以空值结尾。
func describeLogPath(path string) string {
	if path == "" {
		return "（未启用，只写标准输出）"
	}
	return path
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
