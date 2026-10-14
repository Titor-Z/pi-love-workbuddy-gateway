package config

import (
	"fmt"
	"strings"
	"testing"
)

// TestDefaultIsGateway 不写子命令就是网关模式——这是「什么都不用加」的那个默认。
func TestDefaultIsGateway(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.Mode != ModeGateway {
		t.Errorf("Mode = %q，期望 %q", cfg.Mode, ModeGateway)
	}
	if cfg.RelayAddr != fmt.Sprintf("0.0.0.0:%d", defaultRelayPort) {
		t.Errorf("RelayAddr = %q", cfg.RelayAddr)
	}
	// 本机 pi 的口与对外口**同号**，只是绑在不同网卡上。
	// 这样 pi 的配置在哪台机器上都不用改。
	if cfg.ListenAddr != localAddr() {
		t.Errorf("本机 pi 地址 = %q，期望 %q", cfg.ListenAddr, localAddr())
	}
	if !cfg.Daemon {
		t.Error("默认应当后台运行")
	}
}

// TestExplicitGatewaySubcommandIsSameAsNothing 显式写 gateway 与不写等价。
func TestExplicitGatewaySubcommandIsSameAsNothing(t *testing.T) {
	implicit, err := Parse([]string{"-addr", "0.0.0.0:9999"})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	explicit, err := Parse([]string{"gateway", "-addr", "0.0.0.0:9999"})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if implicit.Mode != explicit.Mode || implicit.RelayAddr != explicit.RelayAddr {
		t.Errorf("两种写法结果不同：%+v vs %+v", implicit, explicit)
	}
}

// TestClientTakesGatewayAddress 客户端模式的 -addr 是「要连的网关」。
func TestClientTakesGatewayAddress(t *testing.T) {
	cfg, err := Parse([]string{"client", "-addr", "192.168.1.5:8788"})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.GatewayAddress != "192.168.1.5:8788" {
		t.Errorf("GatewayAddress = %q", cfg.GatewayAddress)
	}
	// 本机 pi 端口不能被网关地址顶掉：客户端自己也是完整网关，
	// pi 连的始终是本机的那个口。
	if cfg.ListenAddr != localAddr() {
		t.Errorf("ListenAddr = %q，期望 %q", cfg.ListenAddr, localAddr())
	}
}

// TestClientRequiresAddress 客户端模式不知道要连谁就没法工作，
// 必须当场报错并给出可照抄的例子。
func TestClientRequiresAddress(t *testing.T) {
	_, err := Parse([]string{"client"})
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "192.168.1.5:8788") {
		t.Errorf("报错里应当有可照抄的例子：%v", err)
	}
}

// TestRejectsUnknownArguments 多余的参数必须报错，并列出支持的参数。
// 静默忽略会让人以为生效了，然后花半小时排查为什么没变化。
func TestRejectsUnknownArguments(t *testing.T) {
	for _, args := range [][]string{
		{"-mode", "client"},
		{"-relay"},
		{"-psk", "abc"},
		{"whatever"},
		{"client", "extra"},
	} {
		_, err := Parse(args)
		if err == nil {
			t.Errorf("%v 应当报错", args)
			continue
		}
		if !strings.Contains(err.Error(), "-addr") {
			t.Errorf("%v 的报错应当列出支持的参数：%v", args, err)
		}
	}
}

// TestDaemonCanBeDisabled 想在终端里看日志时可以显式关掉后台。
func TestDaemonCanBeDisabled(t *testing.T) {
	cfg, err := Parse([]string{"-d=false"})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.Daemon {
		t.Error("-d=false 应当关闭后台模式")
	}
}

// TestUsageListsTheRealPorts 用法里写的端口必须与实际默认值一致，
// 否则用户照抄会连不上。
func TestUsageListsTheRealPorts(t *testing.T) {
	usage := Usage()
	for _, want := range []string{localAddr(), ConsoleAddr()} {
		if !strings.Contains(usage, want) {
			t.Errorf("用法里没有 %s：\n%s", want, usage)
		}
	}
}
