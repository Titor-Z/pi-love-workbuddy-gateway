package main

import (
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
)

// TestJournalPathDiffersPerMode 两种模式必须写不同的事件文件。
//
// 同一台机器上它们可能被同时跑着（先切过去试一试），而跨进程就没有
// Journal 那把进程内互斥锁了，写同一个文件会交错成坏行。
func TestJournalPathDiffersPerMode(t *testing.T) {
	gateway := journalPathForMode(&config.Config{Mode: config.ModeGateway})
	client := journalPathForMode(&config.Config{Mode: config.ModeClient})
	if gateway == "" || client == "" {
		t.Fatalf("事件文件路径不该为空：gateway=%q client=%q", gateway, client)
	}
	if gateway == client {
		t.Fatalf("两种模式写同一个文件 %q——跨进程并发会把行切开", gateway)
	}
}

// TestPSKHintNeverLeaksTheKey 面板能拿到的是「提示」不是密钥本身。
// 密钥一旦出现在 /api/status 里，等于把账号开放给任何能访问面板的地方。
func TestPSKHintNeverLeaksTheKey(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"shortkey":                  "shor…（共 8 位）",
		"AAAAAAAAAAAAAAAAAAAAAAAAA": "AAAA…（共 25 位）",
	}
	for input, want := range cases {
		parts := &runtime{psk: input}
		if got := parts.pskHint(); got != want {
			t.Errorf("pskHint(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestLoadDeviceHonoursNameOverride 命令行给的名字要盖过主机名，
// 这样用户在网关那侧能把它认出来。
func TestLoadDeviceHonoursNameOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	device, err := loadDevice(&config.Config{DeviceName: "客厅 Mac"})
	if err != nil {
		t.Fatalf("载入失败：%v", err)
	}
	if device.Name != "客厅 Mac" {
		t.Errorf("设备名 = %q，期望「客厅 Mac」", device.Name)
	}
	if device.ID == "" {
		t.Error("设备 ID 不该为空")
	}
}

// TestClientRuntimeHasNoCredential 客户端模式**必须**不加载凭证。
//
// 这是 P0 的硬约束：客户端手上不该有令牌。有两条理由——
// 它本机没有装过 WorkBuddy（多半压根没有登录态文件），
// 而且一旦这里也去刷新令牌，多设备共享就退化成互相踢。
func TestClientRuntimeHasNoCredential(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	logger, err := logging.New("", false)
	if err != nil {
		t.Fatalf("构造日志器失败：%v", err)
	}
	cfg := &config.Config{
		Mode:       config.ModeClient,
		GatewayURL: "https://127.0.0.1:1",
		PSK:        "x",
		Trust:      "ab",
	}
	parts, err := buildRuntime(cfg, logger)
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if parts.credential != nil {
		t.Error("客户端模式不该持有凭证管理器")
	}
	if parts.billing != nil {
		t.Error("客户端模式不该有账单客户端")
	}
	if parts.authFile != "" || parts.keyID != "" {
		t.Errorf("客户端模式不该有登录态信息：auth=%q key=%q", parts.authFile, parts.keyID)
	}
	if parts.backend == nil {
		t.Error("客户端模式也要产出可用的 Backend")
	}
}
