package config

import (
	"bytes"
	"strings"
	"testing"
)

// TestDefaultsAreGatewayMode 什么都不输入时必须是网关模式，
// 而且直接就能后台跑起来——这是「默认的服务模式」。
func TestDefaultsAreGatewayMode(t *testing.T) {
	cfg, err := Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.Mode != ModeGateway {
		t.Errorf("Mode = %q，期望 %q", cfg.Mode, ModeGateway)
	}
	if cfg.ListenAddr != defaultListenAddr {
		t.Errorf("ListenAddr = %q，期望 %s", cfg.ListenAddr, defaultListenAddr)
	}
	if cfg.ConsoleAddr != defaultConsoleAddr {
		t.Errorf("ConsoleAddr = %q", cfg.ConsoleAddr)
	}
	if !cfg.Daemon {
		t.Error("默认应当后台运行")
	}
}

// TestClientModeTakesGatewayAddress 客户端模式下 -addr 是「要连的网关地址」，
// 不给必须报错——否则它不知道该转发给谁。
func TestClientModeTakesGatewayAddress(t *testing.T) {
	_, err := Parse([]string{"-mode", "client"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "网关地址") {
		t.Fatalf("应当提示缺少网关地址，实际：%v", err)
	}

	cfg, err := Parse([]string{"-mode", "client", "-addr", "192.168.1.5:8790"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.GatewayAddress != "192.168.1.5:8790" {
		t.Errorf("GatewayAddress = %q，期望 192.168.1.5:8790", cfg.GatewayAddress)
	}
	// 本机 API 端口不能被网关地址顶掉——客户端自己也是完整网关，
	// pi 连的始终是本机的 8788。
	if cfg.ListenAddr != defaultListenAddr {
		t.Errorf("ListenAddr = %q，期望 %s", cfg.ListenAddr, defaultListenAddr)
	}
}

// TestRejectsUnknownArguments 多余的参数必须报错。
// 静默忽略会让用户以为生效了，然后花半小时排查为什么没变化。
func TestRejectsUnknownArguments(t *testing.T) {
	for _, arg := range []string{"-relay", "-psk", "abc", "-probe", "-sync-now"} {
		if _, err := Parse([]string{arg}, &bytes.Buffer{}); err == nil {
			t.Errorf("%q 应当报错", arg)
		}
	}
}

// TestRejectsUnknownMode 模式写错要当场报错，不能默默当成网关跑。
func TestRejectsUnknownMode(t *testing.T) {
	_, err := Parse([]string{"-mode", "Gateway"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "未知运行模式") {
		t.Fatalf("应当报未知模式，实际：%v", err)
	}
}

// TestDaemonCanBeDisabled 想在前台盯着日志时可以显式关掉后台。
func TestDaemonCanBeDisabled(t *testing.T) {
	cfg, err := Parse([]string{"-d=false"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.Daemon {
		t.Error("-d=false 应当关闭后台模式")
	}
}
