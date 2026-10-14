package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
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
		PSKHint:        "abcd…（共 44 位）",
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
	if !strings.Contains(recorder.Body.String(), "abcd…") {
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
