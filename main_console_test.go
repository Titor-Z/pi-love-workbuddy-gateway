package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPIRootTellsWhereConsoleWent 控制台搬到独立端口后，
// 老用户打开 http://127.0.0.1:8788/ 如果只看到 404，第一反应会是「服务坏了」。
// 这里必须如实告诉他控制台在哪。
func TestAPIRootTellsWhereConsoleWent(t *testing.T) {
	handler := apiRootNotice("127.0.0.1:8789")

	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("根路径状态码 = %d，期望 200", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "127.0.0.1:8789") {
		t.Errorf("根路径没有说明控制台地址：%q", body)
	}

	// 其它路径仍是 404，但也要顺带说清本端口只提供什么。
	recorder = httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/console", nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("未知路径状态码 = %d，期望 404", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "/v1/") {
		t.Errorf("404 没有提示本端口只提供 /v1/*：%q", body)
	}
}

// TestIsLoopbackAddr 只认三种本机写法，其余一律当作非本机。
// 判错方向的代价不对称：漏判会让控制台被静默暴露，多警告一次只是多一行日志。
func TestIsLoopbackAddr(t *testing.T) {
	loopback := []string{"127.0.0.1:8789", "127.0.0.1", "127.0.0.2:8789", "localhost:8789", "[::1]:8789"}
	for _, addr := range loopback {
		if !isLoopbackAddr(addr) {
			t.Errorf("%q 应当判定为只绑本机", addr)
		}
	}
	exposed := []string{"0.0.0.0:8789", ":8789", "192.168.1.5:8789", "[::]:8789", "10.0.0.2:8789"}
	for _, addr := range exposed {
		if isLoopbackAddr(addr) {
			t.Errorf("%q 不应当判定为只绑本机", addr)
		}
	}
}
