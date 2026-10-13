package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
)

// newTestServer 造一个只带配置的最小 Server，用于测试鉴权与输出。
func newTestServer(apiKey string) *Server {
	return &Server{cfg: &config.Config{APIKey: apiKey}}
}

// TestAuthorizeDisabledAllowsEveryone 没配密钥时本地任意客户端都能访问。
func TestAuthorizeDisabledAllowsEveryone(t *testing.T) {
	recorder := httptest.NewRecorder()
	if !newTestServer("").authorize(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil)) {
		t.Fatal("未配置密钥时应当放行")
	}
}

// TestAuthorizeAcceptsBearerAndAPIKey 两种携带方式都要认。
func TestAuthorizeAcceptsBearerAndAPIKey(t *testing.T) {
	server := newTestServer("secret")
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"Bearer", map[string]string{"Authorization": "Bearer secret"}},
		{"X-Api-Key", map[string]string{"X-Api-Key": "secret"}},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		for name, value := range testCase.headers {
			request.Header.Set(name, value)
		}
		recorder := httptest.NewRecorder()
		if !server.authorize(recorder, request) {
			t.Errorf("%s：应当放行", testCase.name)
		}
	}
}

// TestAuthorizeRejectsWrongKey 密钥不对要返回 401 且不回显正确密钥。
func TestAuthorizeRejectsWrongKey(t *testing.T) {
	server := newTestServer("secret")
	cases := []string{
		"",
		"Bearer wrong",
		"Bearer ",             // 只有前缀
		"secret",              // 少了 Bearer 前缀
		"Bearer secret-extra", // 前缀匹配但值不对
	}
	for _, header := range cases {
		request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		recorder := httptest.NewRecorder()
		if server.authorize(recorder, request) {
			t.Errorf("Authorization=%q：应当拒绝", header)
			continue
		}
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("Authorization=%q：状态码 = %d，期望 401", header, recorder.Code)
		}
	}
}

// TestWriteErrorShape 错误响应要是 OpenAI 的 {error:{message,type}} 结构，
// 否则客户端 SDK 解析不到错误信息。
func TestWriteErrorShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeError(recorder, http.StatusBadRequest, "invalid_request_error", "messages is required")

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("状态码 = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if payload.Error.Message != "messages is required" || payload.Error.Type != "invalid_request_error" {
		t.Errorf("错误结构不对：%+v", payload.Error)
	}
}

// TestWriteUpstreamErrorPassesJSONThrough 后端的 JSON 错误应原样透传，
// 保留它自己的错误码与文案。
func TestWriteUpstreamErrorPassesJSONThrough(t *testing.T) {
	raw := []byte(`{"code":11128,"msg":"安全策略拦截"}`)
	recorder := httptest.NewRecorder()
	writeUpstreamError(recorder, http.StatusForbidden, raw)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状态码 = %d", recorder.Code)
	}
	if recorder.Body.String() != string(raw) {
		t.Errorf("正文被改动了：%s", recorder.Body.String())
	}
}

// TestWriteUpstreamErrorWrapsPlainText 非 JSON 的响应要包装成标准错误结构，
// 否则客户端会拿到一段无法解析的 HTML 或纯文本。
func TestWriteUpstreamErrorWrapsPlainText(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeUpstreamError(recorder, http.StatusBadGateway, []byte("<html>502</html>"))

	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if payload.Error.Type != "upstream_error" {
		t.Errorf("type = %q", payload.Error.Type)
	}
}

// TestTruncate 日志截断不该越界，也不该改动短文。
func TestTruncate(t *testing.T) {
	if got := truncate("短", 10); got != "短" {
		t.Errorf("短文被改动了：%q", got)
	}
	if got := truncate("0123456789", 4); got != "0123…" {
		t.Errorf("截断结果 = %q", got)
	}
	if got := truncate("", 4); got != "" {
		t.Errorf("空串变成了 %q", got)
	}
}
