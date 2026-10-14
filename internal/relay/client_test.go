package relay

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
)

// TestEndToEndForwardsChat 端到端：relay 客户端 → TLS → relay 服务端 → 网关 Handler。
// 请求体必须原封不动地到达后端，一个字节都不许改写。
func TestEndToEndForwardsChat(t *testing.T) {
	var gotBody string
	var gotPSK, gotDevice, gotName string
	client, fingerprint, server := newTestServer(t, Deps{
		PSK:    "topsecret",
		Logger: discardLogger(),
		Backend: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			gotBody = string(raw)
			gotPSK = r.Header.Get(HeaderPSK)
			gotDevice = r.Header.Get(HeaderDevice)
			gotName = r.Header.Get(HeaderDeviceName)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "data: {}\n\n")
		}),
	})

	relayClient, err := NewClient(Config{
		BaseURL:     server.URL,
		PSK:         "topsecret",
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "d1", Name: "笔记本"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}

	const body = `{"model":"glm-5.3","messages":[{"role":"user","content":"你好"}]}`
	response, err := relayClient.ChatStream(t.Context(), []byte(body))
	if err != nil {
		t.Fatalf("转发失败：%v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d，期望 200", response.StatusCode)
	}
	if gotBody != body {
		t.Errorf("请求体被改动了：%q", gotBody)
	}
	if gotPSK != "topsecret" || gotDevice != "d1" || gotName != "笔记本" {
		t.Errorf("鉴权头没传全：psk=%q device=%q name=%q", gotPSK, gotDevice, gotName)
	}
	_ = client
}

// TestEndToEndWrongPSK relay 侧给回 401，客户端要把它读出来而不是当作网络错误。
func TestEndToEndWrongPSK(t *testing.T) {
	_, fingerprint, server := newTestServer(t, Deps{PSK: "topsecret", Logger: discardLogger()})
	relayClient, err := NewClient(Config{
		BaseURL:     server.URL,
		PSK:         "WRONG-PSK-BUT-SAME-LENGTH-XYZ",
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "d1"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	if err := relayClient.Ping(t.Context()); err == nil {
		t.Fatal("密钥不对也 ping 通了")
	}
}

// TestNewClientRequiresFingerprint 没确认过指纹就建不了客户端。
// 留空等于任何人都能冒充网关，与明文没有区别。
func TestNewClientRequiresFingerprint(t *testing.T) {
	cases := map[string]Config{
		"缺指纹": {BaseURL: "https://1.2.3.4:8790", PSK: "k", Device: &identity.Device{ID: "d"}},
		"缺密钥": {BaseURL: "https://1.2.3.4:8790", Fingerprint: "ab", Device: &identity.Device{ID: "d"}},
		"缺地址": {PSK: "k", Fingerprint: "ab", Device: &identity.Device{ID: "d"}},
		"缺设备": {BaseURL: "https://1.2.3.4:8790", PSK: "k", Fingerprint: "ab"},
	}
	for name, cfg := range cases {
		if _, err := NewClient(cfg); err == nil {
			t.Errorf("%s：应当报错", name)
		}
	}
}

// TestNormalizeBaseURL 地址规整：补协议、去斜杠、拒绝 http。
func TestNormalizeBaseURL(t *testing.T) {
	ok := map[string]string{
		"192.168.1.5:8790":      "https://192.168.1.5:8790",
		"https://h:8790":        "https://h:8790",
		"https://h:8790/":       "https://h:8790",
		"  https://h:8790  ":    "https://h:8790",
		"https://h:8790/relay":  "https://h:8790/relay",
		"https://h:8790/relay/": "https://h:8790/relay",
	}
	for input, want := range ok {
		got, err := normalizeBaseURL(input)
		if err != nil {
			t.Errorf("%q：不该报错：%v", input, err)
			continue
		}
		if got != want {
			t.Errorf("%q → %q，期望 %q", input, got, want)
		}
	}

	// http 必须拒绝：请求正文会经过局域网，明文等于直播对话内容。
	for _, bad := range []string{"", "http://h:8790", "h:8790/x?y=1", "h:8790/x", "://bad"} {
		if got, err := normalizeBaseURL(bad); err == nil {
			t.Errorf("%q：应当报错，实际得到 %q", bad, got)
		}
	}
}

// TestModelsParsesGatewayCatalog 客户端要能从网关的 /v1/models 里读出模型列表。
func TestModelsParsesGatewayCatalog(t *testing.T) {
	_, fingerprint, server := newTestServer(t, Deps{PSK: "topsecret", Logger: discardLogger(),
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"glm-5.3"},{"id":"auto"}]}`)
		}),
	})
	relayClient, err := NewClient(Config{
		BaseURL:     server.URL,
		PSK:         "topsecret",
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "d1"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	models, err := relayClient.Models(t.Context())
	if err != nil {
		t.Fatalf("拉取失败：%v", err)
	}
	if len(models) != 2 {
		t.Fatalf("模型数 = %d，期望 2", len(models))
	}
	if models[0].ID != "glm-5.3" {
		t.Errorf("第一个模型 = %q", models[0].ID)
	}
}

// TestErrorsPassthroughBody 网关的错误要原样到达客户端，
// 否则用户看到的只能是一句「请求失败」。
func TestErrorsPassthroughBody(t *testing.T) {
	_, fingerprint, server := newTestServer(t, Deps{PSK: "topsecret", Logger: discardLogger(),
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeRelayError(w, http.StatusForbidden, "device_revoked", "这台设备已被停用")
		}),
	})
	relayClient, err := NewClient(Config{
		BaseURL:     server.URL,
		PSK:         "topsecret",
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "d1"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	if err := relayClient.Ping(t.Context()); err != nil {
		t.Fatalf("ping 失败：%v", err)
	}
	if err := modelsExpectError(t, relayClient); err == nil {
		t.Fatal("后端返回错误时不该成功")
	} else if !strings.Contains(err.Error(), "这台设备已被停用") {
		t.Errorf("错误信息没有透传：%v", err)
	}
}

// TestModelsFormatErrorSurvey 非 JSON 的错误响应也不能丢信息。
func TestModelsFormatErrorSurvey(t *testing.T) {
	cases := map[string]struct {
		code int
		body string
		want string
	}{
		"JSON 错误": {http.StatusForbidden, `{"error":{"message":"密钥不对"}}`, "密钥不对"},
		"非 JSON":  {http.StatusBadGateway, `<html>502</html>`, "HTTP 502"},
		"空响应体":    {http.StatusInternalServerError, "", "HTTP 500"},
	}
	for name, testCase := range cases {
		err := errorsFromBody(testCase.code, []byte(testCase.body))
		if err == nil {
			t.Errorf("%s：应当报错", name)
			continue
		}
		if !strings.Contains(err.Error(), testCase.want) {
			t.Errorf("%s：错误 = %q，应当包含 %q", name, err, testCase.want)
		}
	}
}

// TestTruncateKeepsShortText 截断不该改动短文，也不该越界。
func TestTruncateKeepsShortText(t *testing.T) {
	if got := truncate("短"); got != "短" {
		t.Errorf("短文被改动了：%q", got)
	}
	if got := truncate(strings.Repeat("长", 500)); len([]rune(got)) != 301 {
		t.Errorf("截断后长度 = %d，期望 300+省略号", len([]rune(got)))
	}
}

// TestUnreachableGatewayFailsFast 网关不可达要报错；
// 这里用的是 relaytls 的 5 秒拨号超时，不该挂住几十秒。
func TestUnreachableGatewayFailsFast(t *testing.T) {
	relayClient, err := NewClient(Config{
		BaseURL:     "https://127.0.0.1:1",
		PSK:         "k",
		Fingerprint: strings.Repeat("a", 64),
		Device:      &identity.Device{ID: "d1"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	if err := relayClient.Ping(t.Context()); err == nil {
		t.Fatal("对端不存在时不该成功")
	} else if !strings.Contains(err.Error(), "无法连接网关") {
		t.Errorf("报错应当说明是连不上网关：%v", err)
	}
}

// modelsExpectError 调 Models 并期望它失败。
func modelsExpectError(t *testing.T, client *Client) error {
	t.Helper()
	_, err := client.Models(context.Background())
	return err
}

// TestPSKMatchesUsesConstantTime relay 用的密钥比较必须是常量时间的同一条路，
// 否则「改了 identity 忘了这里」的漂移没人会发现。
func TestPSKMatchesUsesConstantTime(t *testing.T) {
	if !PSKMatches("same", "same") || PSKMatches("same", "different") {
		t.Error("PSK 比较与 identity 不一致")
	}
}
