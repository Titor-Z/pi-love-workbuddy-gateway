package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
)

// TestAuthorizeAlwaysAllowsLocal 本机 API 不再设密钥：它只听 127.0.0.1，
// 而能访问它的程序本来就能直接读配置目录里的登录态。
func TestAuthorizeAlwaysAllowsLocal(t *testing.T) {
	server := newTestServer()
	recorder := httptest.NewRecorder()
	if !server.authorize(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil)) {
		t.Fatal("本机请求应当放行")
	}
}

// newTestServer 造一个只带配置的最小 Server，用于测试准入与输出。
func newTestServer() *Server {
	return &Server{cfg: &config.Config{}}
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
