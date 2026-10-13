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

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/gateway"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// shutdownTimeout 是收到中断信号后等待在途请求结束的时间。
const shutdownTimeout = 10 * time.Second

// commandModels 是「打印模型清单」子命令；不写子命令时默认启动服务。
const commandModels = "models"

// commandServe 是显式启动服务的子命令。
const commandServe = "serve"

// version 是编译期注入的版本号（-ldflags "-X main.version=..."），
// 直接 go build 时为 dev。
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
		os.Exit(1)
	}
}

// run 按子命令分发。
func run(args []string) error {
	if hasVersionFlag(args) {
		fmt.Printf("pi-love-workbuddy-gateway %s\n", version)
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case commandModels:
			return runModels(args[1:])
		case commandServe:
			return runServe(args[1:])
		}
	}
	return runServe(args)
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

// runServe 装配各模块并启动 HTTP 服务，直到收到中断信号。
func runServe(args []string) error {
	cfg, err := config.Parse(args, os.Stderr)
	if err != nil {
		return err
	}
	logger, err := logging.New(cfg.LogPath)
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

	server := gateway.NewServer(cfg, cred, upstream.NewClient(cred), promotions, logger)
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger.Printf("版本：%s", version)
	logger.Printf("登录态：%s", cred.Path())
	logger.Printf("密钥指纹：%s", cipher.KeyID())
	logger.Printf("监听：http://%s", cfg.ListenAddr)
	logger.Printf("    GET  /health")
	logger.Printf("    GET  /v1/models")
	logger.Printf("    POST /v1/chat/completions")
	if cfg.APIKey != "" {
		logger.Printf("已启用本地访问密钥校验")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Printf("收到退出信号，正在关闭")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

// runModels 拉取实时模型目录并打印，可选输出成 pi 的 models.json。
func runModels(args []string) error {
	cfg, err := config.Parse(args, os.Stderr)
	if err != nil {
		return err
	}
	cred, _, err := buildCredential(cfg, nil)
	if err != nil {
		return err
	}
	promotions, err := catalog.LoadPromotions(resolvePromotionsPath(cfg))
	if err != nil {
		return err
	}
	models, err := upstream.NewClient(cred).Models(context.Background())
	if err != nil {
		return err
	}
	entries := catalog.Build(models, promotions)
	if cfg.PiOutput {
		return writePiModels(os.Stdout, entries, piGatewayBaseURL(cfg), cfg.APIKey)
	}
	return writeTable(os.Stdout, entries)
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
	return credential.NewManager(authFile, cipher, report), cipher, nil
}

// resolvePromotionsPath 给出促销配置文件的实际路径。
func resolvePromotionsPath(cfg *config.Config) string {
	if cfg.Promotions != "" {
		return cfg.Promotions
	}
	return catalog.DefaultPromotionsPath()
}
