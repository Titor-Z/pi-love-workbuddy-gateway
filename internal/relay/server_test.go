package relay

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/paircode"
)

// newTestServer 起一个走 TLS 的 relay 服务端，返回 HTTP 客户端与真实指纹。
func newTestServer(t *testing.T, deps Deps) (*http.Client, string, *httptest.Server) {
	t.Helper()
	certificate, err := testCertificate(t)
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	fingerprint, err := testFingerprint(certificate)
	if err != nil {
		t.Fatalf("算指纹失败：%v", err)
	}
	server := httptest.NewUnstartedServer(NewServer(deps).Handler())
	server.TLS = serverTLS(certificate)
	server.StartTLS()
	t.Cleanup(server.Close)
	return testHTTPClient(t, fingerprint), fingerprint, server
}

// fakeRegistry 是一个够用的设备表替身，记录到访情况。
type fakeRegistry struct {
	observed map[string]string
	revoked  map[string]bool
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{observed: map[string]string{}, revoked: map[string]bool{}}
}

func (f *fakeRegistry) Observe(id, name string) { f.observed[id] = name }

func (f *fakeRegistry) IsRevoked(id string) bool { return f.revoked[id] }

// TestRejectsMissingPSK 没有密钥的请求必须挡在门外。
func TestRejectsMissingPSK(t *testing.T) {
	called := false
	client, _, server := newTestServer(t, Deps{
		PSK:    "secret",
		Logger: discardLogger(),
		Backend: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called = true
		}),
	})
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		server.URL+"/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("状态码 = %d，期望 401", response.StatusCode)
	}
	if called {
		t.Error("没有密钥也进到了网关处理器")
	}
}

// TestPSKIsTheOnlyCredential relay 只认 PSK；
// -api-key 是本机 API 端口那一套，跟这里没有关系。
func TestPSKIsTheOnlyCredential(t *testing.T) {
	client, _, server := newTestServer(t, Deps{
		PSK:    "secret",
		Logger: discardLogger(),
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}),
	})

	cases := map[string]int{
		"":        http.StatusUnauthorized,
		"secret":  http.StatusTeapot, // 匹配 → 交给后端
		"wrong":   http.StatusUnauthorized,
		"Secret":  http.StatusUnauthorized, // 大小写敏感
		"secrets": http.StatusUnauthorized, // 前缀匹配不算匹配
	}
	for psk, want := range cases {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", nil)
		if err != nil {
			t.Fatalf("构造请求失败：%v", err)
		}
		if psk != "" {
			request.Header.Set(HeaderPSK, psk)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%q：请求失败：%v", psk, err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("PSK=%q：状态码 = %d，期望 %d", psk, response.StatusCode, want)
		}
	}
}

// TestRevokedDeviceRejected 已停用的设备返回 403，而且错误信息必须说清
// 「这只是挡一个自报的名字，真要踢人得换密钥」——不说就会被误读成访问控制。
func TestRevokedDeviceRejected(t *testing.T) {
	registry := newFakeRegistry()
	registry.revoked["bad"] = true
	client, _, server := newTestServer(t, Deps{
		PSK:     "secret",
		Devices: registry,
		Logger:  discardLogger(),
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		}),
	})

	for deviceID, want := range map[string]int{"bad": http.StatusForbidden, "good": http.StatusTeapot} {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", nil)
		if err != nil {
			t.Fatalf("构造请求失败：%v", err)
		}
		request.Header.Set(HeaderPSK, "secret")
		request.Header.Set(HeaderDevice, deviceID)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%q：请求失败：%v", deviceID, err)
		}
		body := readBody(response)
		response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("设备 %q：状态码 = %d，期望 %d", deviceID, response.StatusCode, want)
		}
		if deviceID == "bad" && !strings.Contains(body, "重置密钥") {
			t.Errorf("停用提示没说明它不是安全机制：%s", body)
		}
	}
}

// TestObserveRegistersDevice 没有被登记过的设备会自动出现在表里，
// 不需要用户先加白名单——能用正确的 PSK 连上，本来就是被允许了。
func TestObserveRegistersDevice(t *testing.T) {
	registry := newFakeRegistry()
	client, _, server := newTestServer(t, Deps{
		PSK:     "secret",
		Devices: registry,
		Logger:  discardLogger(),
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	})

	if err := pingWith(t, client, server.URL, "d1", "笔记本"); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if name := registry.observed["d1"]; name != "笔记本" {
		t.Errorf("登记的设备名 = %q，期望「笔记本」", name)
	}
}

// TestPingNeedsNoBusinessData ping 不触发任何业务处理，
// 它只用来回答「网关在不在线」。
func TestPingNeedsNoBusinessData(t *testing.T) {
	pinged := false
	client, _, server := newTestServer(t, Deps{
		PSK:    "secret",
		Logger: discardLogger(),
		Backend: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			pinged = true
		}),
	})
	if err := pingWith(t, client, server.URL, "d1", "笔记本"); err != nil {
		t.Fatalf("ping 失败：%v", err)
	}
	if pinged {
		t.Error("ping 不该落到业务处理器上")
	}
}

// TestRelayRequestSkipsAPIKeyCheck 两个端口的鉴权彼此独立：
// 网关配置了 -api-key 也不该要求来自 relay 的请求带上它，
// 否则配了密钥的用户会看到所有客户端都 401。
func TestRelayRequestSkipsAPIKeyCheck(t *testing.T) {
	var seen bool
	client, _, server := newTestServer(t, Deps{
		PSK:    "secret",
		Logger: discardLogger(),
		Backend: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen = FromRelay(r.Context())
		}),
	})
	if err := callV1(t, client, server.URL, "d1", "笔记本"); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if !seen {
		t.Error("上下文里没有 relay 标记——网关那侧会误判成本机请求去查 -api-key")
	}
}

// TestDeviceReachesContext device_id 必须传到上下文里，
// 否则网关写事件时不知道这个请求属于哪台设备。
func TestDeviceReachesContext(t *testing.T) {
	var got string
	client, _, server := newTestServer(t, Deps{
		PSK:    "secret",
		Logger: discardLogger(),
		Backend: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = DeviceFrom(r.Context())
		}),
	})
	if err := callV1(t, client, server.URL, "abc123", "笔记本"); err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	if got != "abc123" {
		t.Errorf("上下文里的设备 ID = %q，期望 abc123", got)
	}
}

// TestUnknownPathRejected relay 端口不提供 /relay 之外的东西。
func TestUnknownPathRejected(t *testing.T) {
	client, _, server := newTestServer(t, Deps{PSK: "secret", Logger: discardLogger()})
	request, err := http.NewRequest(http.MethodGet, server.URL+"/console", nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set(HeaderPSK, "secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("未知路径状态码 = %d，期望 404", response.StatusCode)
	}
}

// TestTransportRejectsWrongFingerprint 复用 relaytls 的传输层，
// 指纹不对必须连不上（这条在这里守住端到端，不只是单元级）。
func TestTransportRejectsWrongFingerprint(t *testing.T) {
	_, _, server := newTestServer(t, Deps{PSK: "secret", Logger: discardLogger()})
	wrong := testHTTPClient(t, strings.Repeat("a", 64))
	if _, err := wrong.Get(server.URL); err == nil {
		t.Fatal("指纹不对还能连上")
	} else if !strings.Contains(err.Error(), "网关证书已变化") {
		t.Errorf("报错没说明是证书变化：%v", err)
	}
}

// TestServerConfigUsesTLS12OrAbove TLS 版本下限必须明确，
// 否则依赖环境默认值，不同 Go 版本会有不同表现。
func TestServerConfigUsesTLS12OrAbove(t *testing.T) {
	certificate, err := testCertificate(t)
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	config := serverTLS(certificate)
	if config.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = %d，至少应当是 TLS 1.2", config.MinVersion)
	}
	if len(config.Certificates) != 1 {
		t.Errorf("证书数量 = %d，期望 1", len(config.Certificates))
	}
}

// serverTLS 是 relay 端口的 TLS 配置。
func serverTLS(certificate tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
}

// TestPairExchangesCodeForPSK 短码换长密钥：客户端要的是长密钥，不是短码本身。
func TestPairExchangesCodeForPSK(t *testing.T) {
	codes := paircode.New()
	code, _, err := codes.Issue()
	if err != nil {
		t.Fatalf("生成配对码失败：%v", err)
	}
	server := NewServer(Deps{PSK: "LONG-PSK-VALUE", PairingCodes: codes, Version: "test"})

	body, _ := json.Marshal(map[string]string{"code": code})
	request := httptest.NewRequest(http.MethodPost, pairPath, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200：%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if payload["psk"] != "LONG-PSK-VALUE" {
		t.Errorf("换回来的密钥 = %v，期望长密钥原文", payload["psk"])
	}
	// 一次性：同一个短码不能再用。
	second := httptest.NewRecorder()
	server.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodPost, pairPath,
		bytes.NewReader(body)))
	if second.Code != http.StatusUnauthorized {
		t.Errorf("同一个短码第二次使用的状态码 = %d，期望 401", second.Code)
	}
}

// TestPairRejectsBadCode 错误的短码必须被拒，且**不能**泄漏长密钥。
func TestPairRejectsBadCode(t *testing.T) {
	codes := paircode.New()
	if _, _, err := codes.Issue(); err != nil {
		t.Fatalf("生成配对码失败：%v", err)
	}
	server := NewServer(Deps{PSK: "LONG-PSK-VALUE", PairingCodes: codes})

	body, _ := json.Marshal(map[string]string{"code": "ZZZZZZ"})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, pairPath,
		bytes.NewReader(body)))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "LONG-PSK-VALUE") {
		t.Fatal("失败的配对响应里出现了长密钥")
	}
}

// TestPairRefusedWhenRelayOff 没有开启转发服务时，配对端点不能把密钥给出去。
func TestPairRefusedWhenRelayOff(t *testing.T) {
	server := NewServer(Deps{Version: "test"})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, pairPath,
		strings.NewReader(`{"code":"ABC123"}`)))
	if recorder.Code != http.StatusNotImplemented {
		t.Errorf("状态码 = %d，期望 501", recorder.Code)
	}
}
