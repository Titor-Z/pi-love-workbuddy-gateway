package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
)

// TestParseDefaults 不传参数时的默认值必须与 README 写的一致，
// 否则文档和实际行为会悄悄分叉。
func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8788" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	// 控制台必须与 API 分开：控制台含余额、请求日志与停止/重启，
	// 跟着可能被改成 0.0.0.0 的 API 端口一起暴露是真实的安全缺口。
	if cfg.ConsoleAddr != "127.0.0.1:8789" {
		t.Errorf("ConsoleAddr = %q", cfg.ConsoleAddr)
	}
	if cfg.APIKey != "" || cfg.AuthFile != "" || cfg.AtRestSecret != "" || cfg.Promotions != "" {
		t.Errorf("默认配置不该有非空的可选字段：%+v", cfg)
	}
	// 日志默认写到配置目录，而不是只进标准输出；
	// 否则重启后历史就没了，拦截率之类的统计无从谈起。
	if cfg.LogPath != appdir.LogPath() {
		t.Errorf("LogPath = %q，期望默认路径 %q", cfg.LogPath, appdir.LogPath())
	}
	if cfg.PiOutput {
		t.Error("默认不该输出 pi 配置")
	}
}

// TestParseFlags 所有参数都要能被命令行覆盖。
func TestParseFlags(t *testing.T) {
	cfg, err := Parse([]string{
		"-addr", "127.0.0.1:9999",
		"-console-addr", "127.0.0.1:9998",
		"-api-key", "secret",
		"-auth-file", "/tmp/a.info",
		"-at-rest-secret", "key",
		"-promotions", "/tmp/p.json",
		"-log", "/tmp/out.log",
		"-pi",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9999" || cfg.APIKey != "secret" ||
		cfg.AuthFile != "/tmp/a.info" || cfg.AtRestSecret != "key" ||
		cfg.ConsoleAddr != "127.0.0.1:9998" ||
		cfg.Promotions != "/tmp/p.json" || cfg.LogPath != "/tmp/out.log" || !cfg.PiOutput {
		t.Errorf("参数没有全部生效：%+v", cfg)
	}
}

// TestParseRejectsUnknownFlag 写错的参数要报错，不能静默忽略。
func TestParseRejectsUnknownFlag(t *testing.T) {
	if _, err := Parse([]string{"-nope"}, &bytes.Buffer{}); err == nil {
		t.Fatal("未知参数应当报错")
	}
}

// TestParseRejectsSameAddrForConsole 控制台与 API 不能共用一个端口：
// 共用了就等于把控制台跟着 API 一起暴露出去，而控制台含余额、请求日志与停止/重启。
func TestParseRejectsSameAddrForConsole(t *testing.T) {
	_, err := Parse([]string{"-addr", "0.0.0.0:8788", "-console-addr", "0.0.0.0:8788"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "控制台") {
		t.Fatalf("应当报控制台地址冲突，实际：%v", err)
	}
}

// TestParseRejectsEmptyConsoleAddr 控制台地址为空要拦下，否则会起在随机端口上，
// 用户根本找不到面板在哪。
func TestParseRejectsEmptyConsoleAddr(t *testing.T) {
	_, err := Parse([]string{"-console-addr", ""}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "控制台监听地址") {
		t.Fatalf("应当报控制台监听地址不能为空，实际：%v", err)
	}
}

// TestParseAllowsSilencingLog 显式传空串表示不落盘，只写标准输出。
func TestParseAllowsSilencingLog(t *testing.T) {
	cfg, err := Parse([]string{"-log", ""}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.LogPath != "" {
		t.Errorf("LogPath = %q，期望空串", cfg.LogPath)
	}
}

// TestParseDaemonFlag 后台运行是默认值——本项目带 Web 面板，
// 主要用法是启动一次后长期在后台跑。
func TestParseDaemonFlag(t *testing.T) {
	cfg, err := Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !cfg.Daemon {
		t.Error("默认应当在后台运行")
	}

	// 想在前台盯着日志时可以显式关掉。
	cfg, err = Parse([]string{"-d=false"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.Daemon {
		t.Error("-d=false 应当关闭后台模式")
	}
}

// TestParseDefaultsModeAndRelay 默认是网关模式，且转发服务默认关闭。
//
// 转发默认关不是因为它危险到不能用——PSK 与指纹已经兜住了——
// 而是因为它不需要：只在一台机器上用的人不该多一个对外监听口。
func TestParseDefaultsModeAndRelay(t *testing.T) {
	cfg, err := Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.Mode != ModeGateway {
		t.Errorf("Mode = %q，期望 %q", cfg.Mode, ModeGateway)
	}
	if cfg.RelayEnabled {
		t.Error("转发服务应当默认关闭，由用户在控制台里开")
	}
	if cfg.RelayAddr != "0.0.0.0:8790" {
		t.Errorf("RelayAddr = %q", cfg.RelayAddr)
	}
	if cfg.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent = %d，期望 4", cfg.MaxConcurrent)
	}
}

// TestParseRejectsClientWithoutGateway 客户端模式不知道要转发给谁就没法工作，
// 这种组合必须当场报错，而不是启动后每次请求都失败。
func TestParseRejectsClientWithoutGateway(t *testing.T) {
	_, err := Parse([]string{"-mode", "client"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "-gateway") {
		t.Fatalf("应当提示缺少 -gateway，实际：%v", err)
	}
}

// TestParseRejectsClientWithCredentialFlags 客户端模式不持有凭证。
// 给出这些参数说明用户以为它会读登录态——不如直接说清楚。
func TestParseRejectsClientWithCredentialFlags(t *testing.T) {
	base := []string{"-mode", "client", "-gateway", "https://1.2.3.4:8790"}
	for _, extra := range [][]string{
		{"-auth-file", "/tmp/a.info"},
		{"-at-rest-secret", "key"},
		{"-relay"},
	} {
		args := append(append([]string{}, base...), extra...)
		if _, err := Parse(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%v 在客户端模式下应当报错", extra)
		}
	}
}

// TestParseRejectsGatewayWithGatewayFlag gateway 模式持有自己的凭证，
// 指定另一个网关多半是参数打错了。
func TestParseRejectsGatewayWithGatewayFlag(t *testing.T) {
	_, err := Parse([]string{"-gateway", "https://1.2.3.4:8790"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "-gateway") {
		t.Fatalf("应当报 -gateway 不适用于网关模式，实际：%v", err)
	}
}

// TestParseRejectsUnknownMode 未知模式要在启动时报错，
// 默默当成网关会让用户以为自己在跑客户端。
func TestParseRejectsUnknownMode(t *testing.T) {
	_, err := Parse([]string{"-mode", "Gateway"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "未知运行模式") {
		t.Fatalf("应当报未知模式，实际：%v", err)
	}
}

// TestParseRejectsRelaySameAsAPI 转发端口与 API 端口不能重合。
func TestParseRejectsRelaySameAsAPI(t *testing.T) {
	_, err := Parse([]string{"-relay", "-addr", "0.0.0.0:8790",
		"-relay-addr", "0.0.0.0:8790"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "转发地址") {
		t.Fatalf("应当报转发地址冲突，实际：%v", err)
	}
}
