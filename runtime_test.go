package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
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
	cfg := &config.Config{Mode: config.ModeClient, ListenAddr: "127.0.0.1:1"}
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
	// 未配对时没有 backend，但控制台必须起得来——配对就在那里完成。
	if parts.backend != nil {
		t.Error("未配对时不该有 backend")
	}
	if parts.pairingErr == nil {
		t.Error("未配对时应当记下原因，供面板显示")
	}
}

// TestCheckSwitchToGatewayCoversAllFourPreconditions 切网关前的四项检查
// 必须各自给出对应原因。共用一句「无法切换」会让用户无从下手。
func TestCheckSwitchToGatewayCoversAllFourPreconditions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// ① 没有副本。
	problem, file, _, err := CheckSwitchToGateway()
	if err != nil {
		t.Fatalf("没有副本不该报错：%v", err)
	}
	if problem != problemNoCopy {
		t.Errorf("没有副本时问题 = %v，期望 problemNoCopy", problem)
	}
	if !strings.Contains(problem.String(), "同步") {
		t.Errorf("原因没有提示去同步：%q", problem.String())
	}

	// ② 有副本但解不开（本机密钥不对）。
	cipher, err := atrest.NewCipher("key-one")
	if err != nil {
		t.Fatalf("构造密钥失败：%v", err)
	}
	sealedAuth, err := cipher.Encrypt("token")
	if err != nil {
		t.Fatalf("封装失败：%v", err)
	}
	writeCredentialCopy(t, 1, map[string]any{"auth": map[string]any{"accessToken": sealedAuth}})

	// 换成另一把密钥再解，应当报「解不开」。
	other, err := atrest.NewCipher("key-two")
	if err != nil {
		t.Fatalf("构造第二把密钥失败：%v", err)
	}
	if err := os.WriteFile(appdir.ClientAtRestPath(), []byte("key-two\n"), 0o600); err != nil {
		t.Fatalf("写密钥失败：%v", err)
	}
	_ = other
	problem, file, _, err = CheckSwitchToGateway()
	if problem != problemUndecryptable {
		t.Errorf("解不开时问题 = %v，期望 problemUndecryptable（err=%v）", problem, err)
	}

	// ③ 解开了但没有访问令牌。
	if err := os.WriteFile(appdir.ClientAtRestPath(), []byte("key-one\n"), 0o600); err != nil {
		t.Fatalf("写密钥失败：%v", err)
	}
	writeCredentialCopy(t, 2, map[string]any{"auth": map[string]any{"refreshToken": "r"}})
	if problem, file, _, _ = CheckSwitchToGateway(); problem != problemNoToken {
		t.Errorf("缺少访问令牌时问题 = %v，期望 problemNoToken", problem)
	}

	// ④ 令牌已过期。
	expired := map[string]any{"auth": map[string]any{
		"accessToken": "plain-token",
		"expiresAt":   float64(time.Now().Add(-time.Hour).UnixMilli()),
	}}
	writePlainCredentialCopy(t, 3, expired)
	if problem, file, _, _ = CheckSwitchToGateway(); problem != problemExpired {
		t.Errorf("令牌过期时问题 = %v，期望 problemExpired", problem)
	}

	// 全部满足时可以切。
	valid := map[string]any{"auth": map[string]any{
		"accessToken": "plain-token",
		"expiresAt":   float64(time.Now().Add(time.Hour).UnixMilli()),
	}}
	writePlainCredentialCopy(t, 4, valid)
	problem, file, _, err = CheckSwitchToGateway()
	if err != nil {
		t.Fatalf("满足条件时不该报错：%v", err)
	}
	if problem != problemNone {
		t.Fatalf("问题 = %v（%q），期望 problemNone", problem, problem)
	}
	if file.Epoch != 4 {
		t.Errorf("epoch = %d，期望 4", file.Epoch)
	}
}

// TestWriteLocalAuthFileIsPrivate 切网关后写的登录态就是账号凭据，权限必须 0600。
func TestWriteLocalAuthFileIsPrivate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := writeLocalAuthFile(map[string]any{
		"auth": map[string]any{"accessToken": "t"},
	}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	info, err := os.Stat(localAuthPath())
	if err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限 = %o，期望 600", perm)
	}
}

// TestLocalAuthIsDiscoverableByLocate 切网关后启动要能找到那份登录态，
// 否则用户切了模式却起不来。
func TestLocalAuthIsDiscoverableByLocate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := writeLocalAuthFile(map[string]any{
		"auth": map[string]any{"accessToken": "t"},
	}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	found, err := credential.Locate("")
	if err != nil {
		t.Fatalf("Locate 找不到刚写的登录态：%v", err)
	}
	if found != localAuthPath() {
		t.Errorf("Locate 返回 %q，期望 %q", found, localAuthPath())
	}
}

func writeCredentialCopy(t *testing.T, epoch int64, credential map[string]any) {
	t.Helper()
	if err := credentialsync.Save(appdir.ClientCredentialPath(), credentialsync.LocalFile{
		Epoch:      epoch,
		Credential: credential,
	}); err != nil {
		t.Fatalf("写副本失败：%v", err)
	}
}

// writePlainCredentialCopy 写一份明文副本，用来构造「缺字段 / 已过期」两种情形。
func writePlainCredentialCopy(t *testing.T, epoch int64, credential map[string]any) {
	t.Helper()
	if err := credentialsync.Save(appdir.ClientCredentialPath(), credentialsync.LocalFile{
		Epoch:      epoch,
		Credential: credential,
	}); err != nil {
		t.Fatalf("写副本失败：%v", err)
	}
}
