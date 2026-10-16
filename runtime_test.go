package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/panel"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relay"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/settings"
)

// TestJournalTargetDiffersPerMode 两种模式必须写不同的分片前缀与旧文件。
//
// 同一台机器上它们可能被同时跑着（先切过去试一试），而跨进程就没有
// Journal 那把进程内互斥锁了，写同一个文件会交错成坏行。
func TestJournalTargetDiffersPerMode(t *testing.T) {
	gatewayDir, gatewayPrefix, gatewayLegacy := journalTargetForMode(&config.Config{Mode: config.ModeGateway})
	clientDir, clientPrefix, clientLegacy := journalTargetForMode(&config.Config{Mode: config.ModeClient})
	if gatewayDir == "" || clientDir == "" {
		t.Fatalf("事件目录不该为空：gateway=%q client=%q", gatewayDir, clientDir)
	}
	if gatewayPrefix == clientPrefix {
		t.Fatalf("两种模式共用同一个分片前缀 %q——跨进程并发会把行切开", gatewayPrefix)
	}
	if gatewayLegacy == clientLegacy {
		t.Fatalf("两种模式的旧事件文件路径相同：%q", gatewayLegacy)
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
	store, err := catalog.OpenStore(appdir.ModelOverridesPath())
	if err != nil {
		t.Fatalf("建覆盖表 Store 失败：%v", err)
	}
	parts, err := buildRuntime(cfg, logger, store)
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

// TestMigrateLegacyJournal 旧版单文件要按记录时间拆进分片，成功后源文件改名——
// 不改名的话下次启动会再导入一遍，日志就重了。
func TestMigrateLegacyJournal(t *testing.T) {
	dir := t.TempDir()
	logger, err := logging.New(filepath.Join(dir, "test.log"), false)
	if err != nil {
		t.Fatalf("建日志失败：%v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	events, err := journal.Open(filepath.Join(dir, "journal"), "gateway")
	if err != nil {
		t.Fatalf("打开事件目录失败：%v", err)
	}
	t.Cleanup(func() { _ = events.Close() })

	// 基准对齐到秒：事件里存的是 RFC3339（秒精度）。
	base := time.Now().Truncate(time.Second)
	legacy := filepath.Join(dir, "gateway.jsonl")
	body := `{"t":"` + base.AddDate(0, 0, -1).Format(time.RFC3339) +
		`","kind":"chat","msg":"昨天"}` + "\n" +
		`{"t":"` + base.Format(time.RFC3339) + `","kind":"chat","msg":"今天"}` + "\n"
	if err := os.WriteFile(legacy, []byte(body), 0o600); err != nil {
		t.Fatalf("写旧文件失败：%v", err)
	}

	migrateLegacyJournal(events, legacy, logger)

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("源文件应当被改名，否则下次启动会重复导入")
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Errorf("改名后的源文件不在了：%v", err)
	}

	records, err := events.Read(time.Time{})
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(records) != 2 {
		t.Fatalf("读回 %d 条，期望 2", len(records))
	}
	if records[0].Message != "昨天" || records[1].Message != "今天" {
		t.Errorf("导入顺序不对：%q / %q", records[0].Message, records[1].Message)
	}
}

// TestMigrateLegacyJournalMissingFile 没有旧文件是正常情况：不报错、不 panic，
// 也不能凭空生成一个 .migrated 文件。
func TestMigrateLegacyJournalMissingFile(t *testing.T) {
	dir := t.TempDir()
	logger, err := logging.New(filepath.Join(dir, "test.log"), false)
	if err != nil {
		t.Fatalf("建日志失败：%v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	events, err := journal.Open(filepath.Join(dir, "journal"), "gateway")
	if err != nil {
		t.Fatalf("打开事件目录失败：%v", err)
	}
	t.Cleanup(func() { _ = events.Close() })

	legacy := filepath.Join(dir, "gateway.jsonl")
	migrateLegacyJournal(events, legacy, logger)

	if _, err := os.Stat(legacy + ".migrated"); !os.IsNotExist(err) {
		t.Error("没有旧文件时不该生成 .migrated")
	}
}

// TestRunJournalPruneStopsOnCancel 清理任务是常驻 goroutine：
// 启动时要先跑一轮，收到取消信号要能退出——退出路径写错会让进程关不掉。
func TestRunJournalPruneStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	logger, err := logging.New(filepath.Join(dir, "test.log"), false)
	if err != nil {
		t.Fatalf("建日志失败：%v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	events, err := journal.Open(filepath.Join(dir, "journal"), "gateway")
	if err != nil {
		t.Fatalf("打开事件目录失败：%v", err)
	}
	t.Cleanup(func() { _ = events.Close() })

	store, err := settings.Open(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("打开偏好失败：%v", err)
	}

	// 铺一个早就过期的分片：启动那一轮就该把它删掉，
	// 否则用户要等 6 小时才看到自己改的保留期生效。
	oldPath := filepath.Join(events.Dir(),
		"gateway-"+time.Now().AddDate(0, 0, -30).Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(oldPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("写旧分片失败：%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runJournalPrune(ctx, events, store, logger)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(oldPath); os.IsNotExist(err) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("启动那一轮没有清理过期分片")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("收到取消信号后没有退出")
	}
}

// TestBillingSourceFor 余额来源要按模式选对：网关模式读本机账单，
// 客户端模式经 relay 向网关取。选错的症状是客户端控制台一直读不出余额。
func TestBillingSourceFor(t *testing.T) {
	billingClient := billing.NewClient(nil, "http://127.0.0.1:1")

	gateway := &runtime{billing: billingClient}
	if _, ok := billingSourceFor(gateway).(panel.LocalBilling); !ok {
		t.Errorf("网关模式应当用本机账单客户端，得到 %T", billingSourceFor(gateway))
	}

	// 客户端模式：backend 是 relay 客户端，**而且 billing 是 nil**——
	// 凭证在网关那台机器上，本机确实没有账单客户端。
	// 这一条必须照实构造：早先这里顺手把 billing 也塞了进去，
	// 于是「客户端模式下 billing 是 nil」这个真实前提被掩盖，
	// 判断顺序写反了测试也不会红，而线上会一直 501。
	relayClient := &relay.Client{}
	client := &runtime{backend: relayClient}
	if source, ok := billingSourceFor(client).(*relay.Client); !ok || source != relayClient {
		t.Errorf("客户端模式应当经 relay 取余额，得到 %T", billingSourceFor(client))
	}

	// 没有账单客户端（未配对的客户端）时返回 nil，面板据此给出明确原因。
	if got := billingSourceFor(&runtime{}); got != nil {
		t.Errorf("没有余额来源时应当是 nil，得到 %T", got)
	}
}

// TestRunPackageExports 子命令要把扩展真的写出去，多余参数要报错。
func TestRunPackageExports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pi-workbuddy")
	if err := runPackage([]string{"--export", dir}); err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.ts")); err != nil {
		t.Fatalf("扩展没写出去：%v", err)
	}

	// 多给一个目录要报错：静默忽略第一个会让用户以为写到了自己想要的地方。
	if err := runPackage([]string{"--export", dir, "多余"}); err == nil {
		t.Error("多给参数应当报错")
	}
	// 不写 --export 时只打印用法，不写文件。
	if err := runPackage(nil); err != nil {
		t.Errorf("不带 --export 时不该报错：%v", err)
	}
}

// TestGatewayCipherFallsBackToClientKey 是「出门在外用不了」的回归测试。
//
// 客户端机器按设计不装 WorkBuddy，所以桌面端与 WORKBUDDY_AT_REST_SECRET
// 都取不到密钥。没有这条兜底，切换后的网关模式会在启动第一步就报
// 「找不到 WorkBuddy 可执行文件，无法获取 at-rest 根密钥」——
// 同步过来的凭证一个都用不上。取桌面端密钥那一步由参数注入，
// 因为本机装了 WorkBuddy，走真路径永远成功、碰不到兜底分支。
func TestGatewayCipherFallsBackToClientKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	noDesktop := func() (*atrest.Cipher, error) {
		return nil, fmt.Errorf("找不到 WorkBuddy 可执行文件")
	}

	// 还没有客户端密钥：必须报错，而不是默默新建一把。
	// 新建会让重新封装的信封换 keyId，与同步来的那份对不上。
	if _, err := gatewayCipherWith(noDesktop); err == nil {
		t.Fatal("既没有桌面端密钥、也没有客户端密钥时应当报错")
	}

	// 客户端同步时会写下自己那把密钥（见 clientCipher），此后要有兜底。
	secret := "AOhvbmNlLWNsaWVudC1hdC1yZXN0LXNlY3JldC1rZXk9"
	if _, err := appdir.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appdir.ClientAtRestPath(), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cipher, err := gatewayCipherWith(noDesktop)
	if err != nil {
		t.Fatalf("应当退回客户端自己的密钥，却报错：%v", err)
	}
	want, err := atrest.NewCipher(secret)
	if err != nil {
		t.Fatal(err)
	}
	if cipher.KeyID() != want.KeyID() {
		t.Errorf("退回应使用客户端自己的密钥：keyID=%s，期望 %s", cipher.KeyID(), want.KeyID())
	}
}

// TestOutingFlowWorksWithoutWorkBuddy 是「出门在外用不了」的端到端回归测试。
//
// 完整走一遍：客户端同步到一份副本 → 出门前切到网关模式 → 在没有装
// WorkBuddy 的机器上把网关模式起起来 → 真的读出令牌。
//
// 两个环境变量的作用：WORKBUDDY_ELECTRON_PATH 指一个不存在的文件，
// 让 electronCandidates 返回空——这就是客户端机器的样子（它按设计不装
// WorkBuddy），于是 atrest.Shared 必然失败、必须走客户端自己那把密钥。
// 这台开发机装了 WorkBuddy，不这样做的话测的是桌面端那条路，压根碰不到缺口。
func TestOutingFlowWorksWithoutWorkBuddy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("WORKBUDDY_AT_REST_SECRET", "")
	t.Setenv("WORKBUDDY_ELECTRON_PATH", filepath.Join(dir, "nonexistent-electron"))
	if _, err := appdir.EnsureDir(); err != nil {
		t.Fatal(err)
	}

	// 1) 客户端同步到的那份副本：用**客户端自己的**密钥重新封装过的明文登录态。
	//    这里直接构造，同步本身由 credentialsync 的测试覆盖。
	plain := map[string]any{
		"auth": map[string]any{
			"accessToken":  "plain-access-token",
			"refreshToken": "plain-refresh-token",
			"expiresAt":    float64(time.Now().Add(30 * 24 * time.Hour).UnixMilli()),
		},
	}
	sealed, err := sealWithClientCipher(plain)
	if err != nil {
		t.Fatalf("封装副本失败：%v", err)
	}
	if err := credentialsync.Save(appdir.ClientCredentialPath(), credentialsync.LocalFile{
		Epoch: 7, SyncedAt: time.Now(), Credential: sealed,
	}); err != nil {
		t.Fatalf("写副本失败：%v", err)
	}

	// 2) 出门前切到网关模式
	if err := SwitchToGateway(); err != nil {
		t.Fatalf("切换失败：%v", err)
	}
	if _, err := os.Stat(appdir.LocalAuthPath()); err != nil {
		t.Fatalf("切换后应当有本地登录态：%v", err)
	}

	// 3) 在没有 WorkBuddy 的机器上启动网关模式
	logger, err := logging.New(filepath.Join(dir, "test.log"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	device, err := identity.LoadDevice(appdir.DevicePath())
	if err != nil {
		t.Fatal(err)
	}
	overrides, err := catalog.OpenStore(appdir.ModelOverridesPath())
	if err != nil {
		t.Fatal(err)
	}
	parts, err := buildGatewayRuntime(
		&config.Config{Mode: config.ModeGateway}, logger, device, overrides)
	if err != nil {
		t.Fatalf("没有 WorkBuddy 的机器上起不来网关模式：%v", err)
	}

	// 4) 真的能读出令牌——证明用的是同一把密钥（同步时封、启动时解）。
	headers, err := parts.credential.Headers()
	if err != nil {
		t.Fatalf("读令牌失败：%v", err)
	}
	if !strings.HasPrefix(headers["Authorization"], "Bearer plain-access-token") {
		t.Errorf("Authorization 头不对：%q", headers["Authorization"])
	}

	// 5) 写出去的那份不能是明文：磁盘上那份文件就是本机的账号凭据。
	raw, err := os.ReadFile(appdir.LocalAuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "plain-access-token") ||
		strings.Contains(string(raw), "plain-refresh-token") {
		t.Error("本地登录态里出现了明文令牌——它必须先用自己的密钥封好再落盘")
	}
}
