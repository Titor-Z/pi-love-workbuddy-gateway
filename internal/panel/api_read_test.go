package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/settings"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// 测试用的固定密钥与令牌，与 gateway 包那套同理：都是合法形态，但不是真值。
const (
	testSecret      = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testAccessToken = "test-access-token"
	testUID         = "test-uid"
)

// 假后端返回的模型目录与账单响应，字段名照抄真实后端。
const (
	fakeModels = `{"code":0,"data":{"models":[
		{"id":"glm-5.3","name":"GLM-5.3","credits":"x0.79","maxInputTokens":1000000,
		 "maxOutputTokens":64000,"supportsImages":true,"supportsReasoning":true,
		 "reasoning":{"supportedEfforts":["low","high"]}},
		{"id":"hy3","name":"Hy3","credits":"x0.00 credits","maxInputTokens":192000,
		 "maxOutputTokens":64000,"supportsReasoning":true}
	]}}`

	// 两个同名的资源包：这正是真实账号的样子，也是面板必须用下标当 key 的原因。
	fakeBilling = `{"code":0,"data":{"Response":{"Data":{"Accounts":[
		{"PackageName":"CodeBuddy个人体验版","CapacityRemain":500,"CapacitySize":500,
		 "CycleCapacityRemainPrecise":"492.94000001","CycleCapacityUsedPrecise":"7.05999999",
		 "CycleCapacitySizePrecise":"500","CapacityRemainPrecise":"500",
		 "CycleEndTime":"2026-10-31 23:59:59"},
		{"PackageName":"CodeBuddy个人版国内运营裂变包","CapacityRemain":100,
		 "CycleCapacityRemainPrecise":"100","CycleCapacityUsedPrecise":"0",
		 "CycleEndTime":"2026-11-14 02:12:56"},
		{"PackageName":"CodeBuddy个人版国内运营裂变包","CapacityRemain":300,
		 "CycleCapacityRemainPrecise":"300","CycleCapacityUsedPrecise":"0",
		 "CycleEndTime":"2026-11-14 02:12:56"}
	]}}}}`
)

// fakeBackend 是一个只服务面板所需的两个接口的假后端。
type fakeBackend struct {
	server     *httptest.Server
	modelsCode int
	billing    string
	requests   []string
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	backend := &fakeBackend{modelsCode: 0, billing: fakeBilling}

	mux := http.NewServeMux()
	mux.HandleFunc("/console/enterprises/personal/models", func(w http.ResponseWriter, r *http.Request) {
		backend.requests = append(backend.requests, r.URL.Path)
		if backend.modelsCode != 0 {
			w.WriteHeader(backend.modelsCode)
			_, _ = w.Write([]byte(`{"code":500,"msg":"后端炸了"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeModels))
	})
	mux.HandleFunc("/v2/billing/meter/get-user-resource", func(w http.ResponseWriter, r *http.Request) {
		backend.requests = append(backend.requests, r.URL.Path)
		// 账单身份全在 header 里，缺了要如实报错，方便验证「头有没有真的发出去」。
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(backend.billing))
	})

	backend.server = httptest.NewServer(mux)
	t.Cleanup(backend.server.Close)
	return backend
}

// writeAuthFile 造一份加密的登录态文件。
func writeAuthFile(t *testing.T, dir string) string {
	t.Helper()
	cipher, err := atrest.NewCipher(testSecret)
	if err != nil {
		t.Fatalf("构造解密器失败：%v", err)
	}
	access, err := cipher.Encrypt(testAccessToken)
	if err != nil {
		t.Fatalf("加密访问令牌失败：%v", err)
	}
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken": access,
			"domain":      "www.codebuddy.cn",
			"expiresAt":   float64(time.Now().Add(time.Hour).UnixMilli()),
		},
		"account": map[string]any{"uid": testUID},
	}
	raw, _ := json.Marshal(doc)
	path := filepath.Join(dir, "workbuddy-desktop.info")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入登录态失败：%v", err)
	}
	return path
}

// newLivePanel 造一个接上假后端的面板，用来测真实读取路径。
func newLivePanel(t *testing.T) (*Panel, *fakeBackend) {
	t.Helper()

	dir := t.TempDir()
	backend := newFakeBackend(t)

	cipher, err := atrest.NewCipher(testSecret)
	if err != nil {
		t.Fatalf("构造解密器失败：%v", err)
	}
	cred := credential.NewManager(writeAuthFile(t, dir), cipher, nil, backend.server.URL)

	logger, err := logging.New(filepath.Join(dir, "test.log"), false)
	if err != nil {
		t.Fatalf("建日志失败：%v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	events, err := journal.Open(filepath.Join(dir, "journal"), "gateway")
	if err != nil {
		t.Fatalf("建事件文件失败：%v", err)
	}
	t.Cleanup(func() { _ = events.Close() })

	preferences, err := settings.Open(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("建偏好失败：%v", err)
	}

	promotions, err := catalog.LoadPromotions("")
	if err != nil {
		t.Fatalf("加载促销配置失败：%v", err)
	}

	console, err := New(Deps{
		Version:        "test",
		StartedAt:      time.Now(),
		AuthFile:       filepath.Join(dir, "workbuddy-desktop.info"),
		ListenAddr:     "127.0.0.1:8788",
		LogPath:        filepath.Join(dir, "gateway.log"),
		JournalPath:    events.Path(),
		PromotionsPath: filepath.Join(dir, "promotions.json"),
		SettingsPath:   filepath.Join(dir, "settings.json"),
		Credential:     cred,
		Upstream:       upstream.NewClient(cred, backend.server.URL),
		Billing:        billing.NewClient(cred, backend.server.URL),
		Promotions:     promotions,
		Journal:        events,
		Settings:       preferences,
		Logger:         logger,
		Control:        NewControl(),
	})
	if err != nil {
		t.Fatalf("建面板失败：%v", err)
	}
	return console, backend
}

// TestStatusEndpoint 确认状态页给出面板要显示的全部字段，且不含令牌。
func TestStatusEndpoint(t *testing.T) {
	console, _ := newLivePanel(t)

	recorder := call(t, console, http.MethodGet, "/api/status", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}

	var payload statusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if payload.Version != "test" || payload.ListenAddr != "127.0.0.1:8788" || payload.PID == 0 {
		t.Fatalf("状态字段不对：%+v", payload)
	}
	if payload.Account["uid"] != testUID {
		t.Fatalf("账号信息不对：%+v", payload.Account)
	}
	// 状态页会把这些路径显示给用户，必须如实给出，不能是空串。
	for name, value := range map[string]string{
		"auth_file": payload.AuthFile, "log_path": payload.LogPath,
		"journal_path": payload.JournalPath, "promotions_path": payload.PromotionsPath,
	} {
		if value == "" {
			t.Errorf("%s 是空的", name)
		}
	}
	// 令牌绝不能出现在响应里。
	if strings.Contains(recorder.Body.String(), testAccessToken) {
		t.Fatal("状态响应里出现了访问令牌")
	}
}

// TestBillingEndpoint 确认余额接口打通，且同名资源包原样返回。
func TestBillingEndpoint(t *testing.T) {
	console, _ := newLivePanel(t)

	recorder := call(t, console, http.MethodGet, "/api/billing", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}

	var payload billingResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(payload.Packages) != 3 {
		t.Fatalf("资源包数 = %d，想要 3", len(payload.Packages))
	}
	// 关键：读的必须是 Precise 字段。整数 CapacityRemain 永远是包的总量。
	if payload.Packages[0].Remain != 492.94000001 {
		t.Fatalf("首个包的剩余 = %v，想要 492.94000001（说明读错字段了）",
			payload.Packages[0].Remain)
	}
	if payload.Packages[0].Use != 7.05999999 {
		t.Fatalf("首个包的已用 = %v，想要 7.05999999", payload.Packages[0].Use)
	}
	// 三个包 492.94 + 100 + 300。
	if want := 892.94000001; payload.TotalRemain != want {
		t.Fatalf("合计剩余 = %v，想要 %v", payload.TotalRemain, want)
	}
	// 结算延迟提示必须带上：没有它用户会以为数字是实时的。
	if payload.SettleHint == "" {
		t.Fatal("缺少结算延迟提示")
	}
	if payload.Packages[0].CycleEnd == nil {
		t.Fatal("周期结束时间没有解析出来")
	}
}

// TestBillingEndpointUpstreamFailure 确认后端出错时返回 502 而不是假装成功，
// 也不把内部错误文本原样抛给前端以外的位置。
func TestBillingEndpointUpstreamFailure(t *testing.T) {
	console, backend := newLivePanel(t)
	backend.billing = `{"code":500,"msg":"boom"}`

	recorder := call(t, console, http.MethodGet, "/api/billing", nil, nil)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，想要 502：%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "upstream_error") {
		t.Fatalf("响应体没有给出机器可读的错误码：%s", recorder.Body.String())
	}
}

// TestModelsEndpoint 确认模型目录带上了费用标记。
func TestModelsEndpoint(t *testing.T) {
	console, backend := newLivePanel(t)

	recorder := call(t, console, http.MethodGet, "/api/models", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Models []modelEntry `json:"models"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(payload.Models) != 2 {
		t.Fatalf("模型数 = %d，想要 2", len(payload.Models))
	}
	if payload.Models[0].Credits != "0.79x" {
		t.Errorf("glm-5.3 的倍率 = %q，想要 0.79x", payload.Models[0].Credits)
	}
	if !payload.Models[1].Free {
		t.Errorf("hy3 是免费的，free 应为 true：%+v", payload.Models[1])
	}
	if !payload.Models[0].Thinking || payload.Models[0].Context != 1000000 {
		t.Errorf("glm-5.3 的能力字段不对：%+v", payload.Models[0])
	}
	if len(backend.requests) == 0 {
		t.Fatal("面板没有真的去请求后端")
	}
}

// TestModelsEndpointUpstreamFailure 确认拉不到目录时返回 502。
func TestModelsEndpointUpstreamFailure(t *testing.T) {
	console, backend := newLivePanel(t)
	backend.modelsCode = http.StatusInternalServerError

	recorder := call(t, console, http.MethodGet, "/api/models", nil, nil)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，想要 502：%s", recorder.Code, recorder.Body.String())
	}
}

// TestPiConfigEndpoint 确认可直接落盘的 pi 配置能生成出来。
func TestPiConfigEndpoint(t *testing.T) {
	console, _ := newLivePanel(t)

	recorder := call(t, console, http.MethodGet, "/api/models/pi", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	if disposition := recorder.Header().Get("Content-Disposition"); !strings.Contains(disposition, "models.json") {
		t.Fatalf("下载文件名不对：%q", disposition)
	}

	var payload struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("生成的不是合法 JSON：%v", err)
	}
	provider := payload.Providers[catalog.PiProviderName]
	if provider.BaseURL != "http://127.0.0.1:8788/v1" {
		t.Fatalf("baseUrl = %q，应当是面板自己的地址", provider.BaseURL)
	}
	if len(provider.Models) != 2 {
		t.Fatalf("模型数 = %d，想要 2", len(provider.Models))
	}
}

// TestEventsEndpointLimit 确认尾部截取与上限都生效。
func TestEventsEndpointLimit(t *testing.T) {
	console, _ := newLivePanel(t)

	for i := 0; i < 12; i++ {
		console.deps.Journal.Append(journal.Record{
			Kind: journal.KindChat, Model: "glm-5.3", Credit: 1,
		})
	}

	recorder := call(t, console, http.MethodGet, "/api/events?limit=5", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200", recorder.Code)
	}
	var payload struct {
		Events []journal.Record `json:"events"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(payload.Events) != 5 {
		t.Fatalf("事件数 = %d，想要 5", len(payload.Events))
	}

	// 非法 limit 要走默认值，不能让接口报错。
	for _, raw := range []string{"0", "-3", "abc"} {
		recorder := call(t, console, http.MethodGet, "/api/events?limit="+raw, nil, nil)
		if recorder.Code != http.StatusOK {
			t.Errorf("limit=%s 的状态码 = %d，想要 200", raw, recorder.Code)
		}
	}
}

// TestStatusIncludesMode 前端要按模式决定显示哪些区块，
// 所以 status 必须带上 mode；客户端模式没有凭证时账号摘要留空而不是报错。
func TestStatusIncludesMode(t *testing.T) {
	console, _ := newLivePanel(t)
	console.deps.Mode = config.ModeGateway
	payload := readStatus(t, console)
	if payload["mode"] != config.ModeGateway {
		t.Errorf("mode = %v，期望 %q", payload["mode"], config.ModeGateway)
	}

	// 客户端模式：没有凭证管理器，account 必须是 null 而不是读取失败。
	console.deps.Mode = config.ModeClient
	console.deps.Credential = nil
	payload = readStatus(t, console)
	if payload["account"] != nil {
		t.Errorf("客户端模式的 account 应当为空，实际 %v", payload["account"])
	}
	if payload["mode"] != config.ModeClient {
		t.Errorf("mode = %v", payload["mode"])
	}
}

// TestBillingAndModelsReportUnavailableInClientMode 客户端模式没有凭证，
// 余额与模型目录都属于网关那一侧。这里必须给出明确的原因，
// 而不是一个「读取失败」——那会被误读成网络问题。
func TestBillingAndModelsReportUnavailableInClientMode(t *testing.T) {
	console, _ := newLivePanel(t)
	console.deps.Credential = nil
	console.deps.Billing = nil
	console.deps.Upstream = nil

	for _, path := range []string{"/api/billing", "/api/models", "/api/models/pi"} {
		recorder := call(t, console, http.MethodGet, path, nil, nil)
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s 状态码 = %d，期望 501", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "not_configured") {
			t.Errorf("%s 的错误类型不对：%s", path, recorder.Body.String())
		}
	}
	// 余额那条要明确说「属于网关那一侧」，否则用户会去查自己的网络。
	billingRecorder := call(t, console, http.MethodGet, "/api/billing", nil, nil)
	if !strings.Contains(billingRecorder.Body.String(), "客户端模式") {
		t.Errorf("余额错误没有说明是模式问题：%s", billingRecorder.Body.String())
	}
}

func readStatus(t *testing.T, console *Panel) map[string]any {
	t.Helper()
	recorder := call(t, console, http.MethodGet, "/api/status", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	return payload
}
