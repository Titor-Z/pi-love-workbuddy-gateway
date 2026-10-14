package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/pairing"
)

// newRelayPanel 造一个处于网关模式、带设备表的面板。
func newRelayPanel(t *testing.T) (*Panel, *devices.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	registry, err := devices.Open(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatalf("打开设备表失败：%v", err)
	}
	console, err := New(Deps{
		Version:        "test",
		StartedAt:      time.Now(),
		Mode:           config.ModeGateway,
		RelayEnabled:   true,
		RelayAddr:      "0.0.0.0:8790",
		TLSFingerprint: "aa:bb",
		PSK:            "SUPER-SECRET-PSK-VALUE",
		PSKHint:        "SUPE…（共 22 位）",
		Devices:        registry,
		ListenAddr:     "127.0.0.1:8788",
		Logger:         newTestLogger(t),
		Control:        NewControl(),
	})
	if err != nil {
		t.Fatalf("建面板失败：%v", err)
	}
	return console, registry, dir
}

func newTestLogger(t *testing.T) *logging.Logger {
	t.Helper()
	logger, err := logging.New(filepath.Join(t.TempDir(), "panel.log"), false)
	if err != nil {
		t.Fatalf("建日志失败：%v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger
}

// TestRelayStatusNeverLeaksPSK 面板绝不能返回密钥本身。
// 密钥一旦出现在接口里，等于把账号开放给任何能访问面板的地方。
func TestRelayStatusNeverLeaksPSK(t *testing.T) {
	console, _, dir := newRelayPanel(t)
	// 先造一个真的密钥文件，证明它不会被打出去。
	if err := os.MkdirAll(filepath.Dir(appdir.PSKPath()), 0o700); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(appdir.PSKPath(), []byte("SUPER-SECRET-PSK-VALUE\n"), 0o600); err != nil {
		t.Fatalf("写密钥文件失败：%v", err)
	}
	recorder := call(t, console, http.MethodGet, "/api/relay", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "SUPER-SECRET") {
		t.Fatal("响应里出现了密钥原文")
	}
	if !strings.Contains(recorder.Body.String(), "SUPE…") {
		t.Errorf("没有给出便于核对的密钥提示：%s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "aa:bb") {
		t.Errorf("没有给出证书指纹：%s", recorder.Body.String())
	}
	_ = dir
}

// TestRelayStatusListsDevices 设备表要出现在面板上，且空表不是 null。
func TestRelayStatusListsDevices(t *testing.T) {
	console, registry, _ := newRelayPanel(t)
	recorder := call(t, console, http.MethodGet, "/api/relay", nil, nil)
	var payload relayResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if payload.Devices == nil {
		t.Error("设备列表为 nil——前端要判空两次")
	}
	if len(payload.Devices) != 0 {
		t.Errorf("空表长度 = %d", len(payload.Devices))
	}

	registry.Observe("laptop", "笔记本")
	recorder = call(t, console, http.MethodGet, "/api/relay", nil, nil)
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if len(payload.Devices) != 1 || payload.Devices[0].Name != "笔记本" {
		t.Errorf("设备没有出现在面板上：%+v", payload.Devices)
	}
}

// TestRevokeDeviceTogglesAccess 停用要立刻生效，撤销停用也要能回头。
func TestRevokeDeviceTogglesAccess(t *testing.T) {
	console, registry, _ := newRelayPanel(t)
	registry.Observe("laptop", "笔记本")

	if code := revoke(t, console, "laptop", true); code != http.StatusOK {
		t.Fatalf("停用状态码 = %d，期望 200", code)
	}
	if !registry.IsRevoked("laptop") {
		t.Error("停用没有生效")
	}
	if code := revoke(t, console, "laptop", false); code != http.StatusOK {
		t.Fatalf("撤销停用状态码 = %d，期望 200", code)
	}
	if registry.IsRevoked("laptop") {
		t.Error("撤销停用没有生效")
	}
}

// TestRevokeUnknownDeviceReports404 停用一台不存在的设备要说「没有这台」，
// 而不是假装成功——用户会以为已经挡掉了某个人。
func TestRevokeUnknownDeviceReports404(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	recorder := call(t, console, http.MethodPost, "/api/devices/revoke?id=ghost", nil, nil)
	if recorder.Code != http.StatusNotFound {
		t.Errorf("状态码 = %d，期望 404", recorder.Code)
	}
}

// TestRevokeRequiresDeviceID 缺少 ID 要给 400 而不是 500。
func TestRevokeRequiresDeviceID(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	recorder := call(t, console, http.MethodPost, "/api/devices/revoke", nil, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d，期望 400", recorder.Code)
	}
}

// TestResetPSKRequiresConfirmation 重置会踢掉所有客户端，且无法只踢一台。
// 所以它必须二次确认，不能一个按钮就执行。
func TestResetPSKRequiresConfirmation(t *testing.T) {
	console, _, dir := newRelayPanel(t)
	before := readPSKFile(t, dir)

	recorder := call(t, console, http.MethodPost, "/api/relay/psk",
		map[string]any{"confirm": false}, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("未确认时状态码 = %d，期望 400", recorder.Code)
	}
	if readPSKFile(t, dir) != before {
		t.Error("未确认就改了密钥")
	}

	recorder = call(t, console, http.MethodPost, "/api/relay/psk",
		map[string]any{"confirm": true}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("确认后状态码 = %d，期望 200", recorder.Code)
	}
	after := readPSKFile(t, dir)
	if after == "" || after == before {
		t.Error("密钥没有被换掉")
	}
	// 新密钥必须返回给用户：要把它填进各台客户端。
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if payload["psk"] != after {
		t.Errorf("返回的密钥与落盘的不一致：%q vs %q", payload["psk"], after)
	}
}

// TestClientModeHasNoRelayEndpoints 客户端模式没有转发端口也没有设备表，
// 这些接口必须明确拒绝，而不是返回一堆空数据。
func TestClientModeHasNoRelayEndpoints(t *testing.T) {
	console, err := New(Deps{
		Version:   "test",
		StartedAt: time.Now(),
		Mode:      config.ModeClient,
		Logger:    newTestLogger(t),
		Control:   NewControl(),
	})
	if err != nil {
		t.Fatalf("建面板失败：%v", err)
	}
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/relay"},
		{http.MethodPost, "/api/relay/psk"},
		{http.MethodPost, "/api/relay/enable"},
	}
	for _, testCase := range cases {
		recorder := call(t, console, testCase.method, testCase.path, nil, nil)
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s %s 状态码 = %d，期望 501",
				testCase.method, testCase.path, recorder.Code)
		}
	}
}

// TestRelayPreferenceRoundTrip 转发开关要能落盘并在重启后保持。
func TestRelayPreferenceRoundTrip(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	if PanelReadRelayPreference() {
		t.Fatal("默认应当是关闭的")
	}
	if err := console.setRelayEnabled(true); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if !PanelReadRelayPreference() {
		t.Error("开启后应当读得到")
	}
	if perm := filePerm(t, appdir.RelayPreferencePath()); perm != 0o644 {
		t.Errorf("偏好文件权限 = %o", perm)
	}
}

// TestReadRelayPreferenceToleratesCorruptFile 读坏了的偏好文件当作未开启——
// 转发服务是选择开启的功能，不能让一个坏文件把它偷偷打开。
func TestReadRelayPreferenceToleratesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Dir(appdir.RelayPreferencePath()), 0o700); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(appdir.RelayPreferencePath(), []byte("不是 JSON"), 0o600); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if PanelReadRelayPreference() {
		t.Error("坏文件不该被当成「已开启」")
	}
}

// readPSKFile 读配置目录里的密钥。路径必须与实现用同一个 appdir 函数，
// 否则测试会去一个实现根本不看的路径上找，然后得出「没有生效」的错误结论。
func readPSKFile(t *testing.T, _ string) string {
	t.Helper()
	raw, err := os.ReadFile(appdir.PSKPath())
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	return strings.TrimSpace(string(raw))
}

func filePerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
	return info.Mode().Perm()
}

// revoke 调用停用/恢复接口，revoked 为 true 表示停用。
func revoke(t *testing.T, console *Panel, id string, revoked bool) int {
	t.Helper()
	path := "/api/devices/revoke?id=" + id
	if !revoked {
		path = "/api/devices/unrevoke?id=" + id
	}
	recorder := call(t, console, http.MethodPost, path, nil, nil)
	return recorder.Code
}

// TestSyncToggleDefaultsOffAndRequiresConfirmation 凭证同步默认关，
// 且开启必须二次确认——它交出的是账号令牌。
func TestSyncToggleDefaultsOffAndRequiresConfirmation(t *testing.T) {
	console, _, _ := newRelayPanel(t)

	// 默认两个开关都是关的。
	if PanelReadRelayPreference() || ReadSyncPreference() {
		t.Fatal("两个开关都应当默认关闭")
	}
	// 开了转发不该顺带开启凭证同步——两者风险差一个量级。
	if err := console.setRelayEnabled(true); err != nil {
		t.Fatalf("开转发失败：%v", err)
	}
	if !PanelReadRelayPreference() {
		t.Fatal("转发没开成")
	}
	if ReadSyncPreference() {
		t.Error("开转发不该顺带开启凭证同步")
	}
	// 反向也一样：开同步不该把转发关掉。
	if err := SetSyncPreference(true); err != nil {
		t.Fatalf("开同步失败：%v", err)
	}
	if !ReadSyncPreference() {
		t.Fatal("同步没开成")
	}
	if !PanelReadRelayPreference() {
		t.Error("开同步不该把转发关掉")
	}

	// 端点层面：**开启**未确认必须 400，且不改动开关。
	// 关闭不需要确认——关掉一个敏感开关不该有额外门槛。
	recorder := call(t, console, http.MethodPost, "/api/sync",
		map[string]any{"enable": false, "confirm": false}, nil)
	if recorder.Code != http.StatusOK {
		t.Errorf("关闭不需要确认，状态码 = %d", recorder.Code)
	}
	if ReadSyncPreference() {
		t.Error("关闭没有生效")
	}

	recorder = call(t, console, http.MethodPost, "/api/sync",
		map[string]any{"enable": true, "confirm": false}, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("开启未确认时状态码 = %d，期望 400", recorder.Code)
	}
	if ReadSyncPreference() {
		t.Error("未确认的开启请求把开关改掉了")
	}

	recorder = call(t, console, http.MethodPost, "/api/sync",
		map[string]any{"enable": true, "confirm": true}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("确认后开启状态码 = %d，期望 200", recorder.Code)
	}
	if !ReadSyncPreference() {
		t.Error("确认后仍没有开启")
	}
}

// TestSyncEndpointReportsGatewayAndLocalEpoch 面板要能同时看到两边版本，
// 否则用户没法判断「该用哪一份」。
func TestSyncEndpointReportsGatewayAndLocalEpoch(t *testing.T) {
	console, _, dir := newRelayPanel(t)
	if err := os.MkdirAll(filepath.Dir(appdir.CredentialEpochPath()), 0o700); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(appdir.CredentialEpochPath(), []byte("12\n"), 0o600); err != nil {
		t.Fatalf("写 epoch 失败：%v", err)
	}
	if err := credentialsync.Save(appdir.ClientCredentialPath(), credentialsync.LocalFile{
		Epoch:      7,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "sealed"}},
	}); err != nil {
		t.Fatalf("写副本失败：%v", err)
	}

	recorder := call(t, console, http.MethodGet, "/api/sync", nil, nil)
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if payload["gateway_epoch"].(float64) != 12 {
		t.Errorf("网关 epoch = %v，期望 12", payload["gateway_epoch"])
	}
	local := payload["local"].(map[string]any)
	if local["local_epoch"].(float64) != 7 {
		t.Errorf("本机 epoch = %v，期望 7", local["local_epoch"])
	}
	_ = dir
}

// TestSwitchCheckWithoutChecker 没注入检查器时不能假装可以切。
func TestSwitchCheckWithoutChecker(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	SetSwitchChecker(nil)
	recorder := call(t, console, http.MethodGet, "/api/sync/switch-check", nil, nil)
	if recorder.Code != http.StatusNotImplemented {
		t.Errorf("状态码 = %d，期望 501", recorder.Code)
	}
}

// TestSwitchCheckReportsReason 四种被拒原因都要能传到前端。
func TestSwitchCheckReportsReason(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	SetSwitchChecker(func() (bool, string, int64, time.Time) {
		return false, "凭证已过期，请先连回网关同步新的", 3, time.Now()
	})
	defer SetSwitchChecker(nil)

	recorder := call(t, console, http.MethodGet, "/api/sync/switch-check", nil, nil)
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if payload["can_switch"].(bool) {
		t.Error("不满足条件时不该报告可以切")
	}
	if payload["reason"] != "凭证已过期，请先连回网关同步新的" {
		t.Errorf("原因没有传到前端：%v", payload["reason"])
	}
}

// TestPairingProbeRejectsBadInput 表单填错要给具体原因，
// 而不是一个「失败」让用户回去猜。
func TestPairingProbeRejectsBadInput(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	console.deps.Mode = config.ModeClient
	SetProbeFingerprint(func(string) (string, error) { return "aa:bb", nil })
	defer SetProbeFingerprint(nil)

	cases := []struct {
		name    string
		body    map[string]any
		wantSub string
	}{
		{"带路径的地址", map[string]any{"address": "h:8788/x"}, "主机:端口"},
		{"地址为空", map[string]any{"address": ""}, "主机:端口"},
	}
	for _, testCase := range cases {
		recorder := call(t, console, http.MethodPost, "/api/pairing/probe", testCase.body, nil)
		if !strings.Contains(recorder.Body.String(), testCase.wantSub) {
			t.Errorf("%s：响应没有说明原因：%s", testCase.name, recorder.Body.String())
		}
	}
}

// TestPairingProbeNeedsNoCredential 探测这一步**不要任何凭据**。
//
// 顺序很重要：用户必须先看到指纹并比对，之后才该把配对码交出去。
// 如果探测就要密钥，冒充者可以在验证之前把凭据收走。
func TestPairingProbeNeedsNoCredential(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	console.deps.Mode = config.ModeClient
	SetProbeFingerprint(func(string) (string, error) { return "aa:bb", nil })
	defer SetProbeFingerprint(nil)

	recorder := call(t, console, http.MethodPost, "/api/pairing/probe",
		map[string]any{"address": "192.168.1.5:8788"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "aa:bb") {
		t.Errorf("没有带密钥就没有给出指纹：%s", recorder.Body.String())
	}
}

// TestPairingProbeReturnsFingerprintForConfirmation 第一步只取指纹、不建信任。
// 返回的指纹要交给用户比对，所以必须真的回给前端。
func TestPairingProbeReturnsFingerprintForConfirmation(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	console.deps.Mode = config.ModeClient
	SetProbeFingerprint(func(address string) (string, error) { return "11:22:33", nil })
	defer SetProbeFingerprint(nil)

	recorder := call(t, console, http.MethodPost, "/api/pairing/probe",
		map[string]any{"address": "192.168.1.5:8790", "psk": "secret"}, nil)
	var payload pairingResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if !payload.NeedsConfirm {
		t.Error("应当进入「等待确认」这一步")
	}
	if payload.Fingerprint != "11:22:33" {
		t.Errorf("指纹 = %q", payload.Fingerprint)
	}
	// 此时还不能算配对完成：用户还没比对过。
	if payload.Paired {
		t.Error("未确认指纹就报告配对完成——那等于自动信任")
	}
}

// TestPairingConfirmRequiresFingerprint 没有指纹不给确认。
// 少了它等于「连一次就信」，中间人换张证书照样通过。
func TestPairingConfirmRequiresFingerprint(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	console.deps.Mode = config.ModeClient
	recorder := call(t, console, http.MethodPost, "/api/pairing/confirm",
		map[string]any{"address": "h:8790", "psk": "k"}, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d，期望 400", recorder.Code)
	}
}

// TestPairingConfirmWritesPrivateFile 确认后落盘，权限 0600。
func TestPairingConfirmWritesPrivateFile(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	console.deps.Mode = config.ModeClient
	recorder := call(t, console, http.MethodPost, "/api/pairing/confirm",
		map[string]any{"address": "192.168.1.5:8790", "psk": "k", "fingerprint": "AB:CD"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200：%s", recorder.Code, recorder.Body.String())
	}
	info, exists, err := pairing.Load(appdir.ClientPath())
	if err != nil || !exists {
		t.Fatalf("配对信息没落盘：%v", err)
	}
	if info.Fingerprint != "abcd" {
		t.Errorf("指纹没有被规整：%q", info.Fingerprint)
	}
	if info.URL != "https://192.168.1.5:8790" {
		t.Errorf("地址没有被补全：%q", info.URL)
	}
	info2, err := os.Stat(appdir.ClientPath())
	if err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
	if perm := info2.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限 = %o，期望 600", perm)
	}
}

// TestPairingEndpointsRejectedInGatewayMode 网关模式不需要配对，
// 这些接口必须明确拒绝，而不是写出一份毫无意义的配对文件。
func TestPairingEndpointsRejectedInGatewayMode(t *testing.T) {
	console, _, _ := newRelayPanel(t) // 网关模式
	for _, path := range []string{"/api/pairing/probe", "/api/pairing/confirm"} {
		recorder := call(t, console, http.MethodPost, path, map[string]any{}, nil)
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s 状态码 = %d，期望 501", path, recorder.Code)
		}
	}
}

// TestSwitchExecuteReportsReason 切换失败要把原因原样带给用户，
// 而不是一个 500。四种被拒原因各自的处置方式不同。
func TestSwitchExecuteReportsReason(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	console.deps.Mode = config.ModeClient
	SetSwitchExecutor(func() error { return errors.New("凭证已过期，请先连回网关同步新的") })
	defer SetSwitchExecutor(nil)

	recorder := call(t, console, http.MethodPost, "/api/sync/switch", nil, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("状态码 = %d，期望 409", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "凭证已过期") {
		t.Errorf("原因没有传到前端：%s", recorder.Body.String())
	}
}

// TestSwitchExecuteSuccessTellsUserToRestart 切换完成后必须明说「要重启」。
// 模式由启动参数决定，不重启是切不过去的。
func TestSwitchExecuteSuccessTellsUserToRestart(t *testing.T) {
	console, _, _ := newRelayPanel(t)
	console.deps.Mode = config.ModeClient
	SetSwitchExecutor(func() error { return nil })
	defer SetSwitchExecutor(nil)

	recorder := call(t, console, http.MethodPost, "/api/sync/switch", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "重新启动") {
		t.Errorf("没有提示要重启：%s", recorder.Body.String())
	}
}

// TestRevealPSKReturnsFullKey 面板能按需给出完整密钥。
//
// 「看不全」本身就是缺陷：密钥只显示前四位的话，用户根本没法把它填进客户端。
// 列表接口不返回它（避免被轮询反复拉走），但专用接口必须给全。
func TestRevealPSKReturnsFullKey(t *testing.T) {
	console, _, _ := newRelayPanel(t)

	// 列表接口仍然不给原文。
	listing := call(t, console, http.MethodGet, "/api/relay", nil, nil)
	if strings.Contains(listing.Body.String(), "SUPER-SECRET") {
		t.Fatal("列表接口泄漏了密钥原文")
	}

	recorder := call(t, console, http.MethodPost, "/api/relay/psk/reveal", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", recorder.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if payload["psk"] != "SUPER-SECRET-PSK-VALUE" {
		t.Errorf("返还的密钥 = %q，期望完整原文", payload["psk"])
	}
}

// TestRevealPSKRefusedInClientMode 客户端模式没有密钥可给。
func TestRevealPSKRefusedInClientMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	console, err := New(Deps{
		Version:   "test",
		StartedAt: time.Now(),
		Mode:      config.ModeClient,
		Logger:    newTestLogger(t),
		Control:   NewControl(),
	})
	if err != nil {
		t.Fatalf("建面板失败：%v", err)
	}
	recorder := call(t, console, http.MethodPost, "/api/relay/psk/reveal", nil, nil)
	if recorder.Code != http.StatusNotImplemented {
		t.Errorf("状态码 = %d，期望 501", recorder.Code)
	}
}
