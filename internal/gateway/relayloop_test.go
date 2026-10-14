package gateway_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/gateway"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relay"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relaytls"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// TestRelayLoopBucketsEventsByDevice 完整链路：
// relay 客户端 → TLS → relay 服务端 → 网关 Handler → 事件文件。
//
// 这条串起来是为了回答两个不可能靠单测回答的问题：
//  1. 客户端模式下 requestHandler真的走到了后端（凭证不在本机也照样工作）；
//  2. 事件记录里的 device 字段确实填上了，网关因此能分桶。
func TestRelayLoopBucketsEventsByDevice(t *testing.T) {
	dir := t.TempDir()
	certificate, fingerprint, err := relaytls.LoadOrCreate(dir+"/cert.pem", dir+"/key.pem")
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	const psk = "TEST-PSK-TEST-PSK-TEST-PSK-TEST"

	upstream := httptest.NewServer(newSSEBackend())
	defer upstream.Close()

	// 网关侧：同一个 Server 实例同时挂进 API 与 relay 两个 mux。
	eventsPath := dir + "/gateway.jsonl"
	events, err := journal.Open(eventsPath)
	if err != nil {
		t.Fatalf("打开事件文件失败：%v", err)
	}
	defer events.Close()

	logger, err := logging.New("", false)
	if err != nil {
		t.Fatalf("构造日志器失败：%v", err)
	}
	gatewayServer := gateway.NewServer(&config.Config{}, nil,
		&liveBackend{baseURL: upstream.URL}, nil, logger, events, 0)

	registry, err := devices.Open(dir + "/devices.json")
	if err != nil {
		t.Fatalf("打开设备表失败：%v", err)
	}
	relayServer := relay.NewServer(relay.Deps{PSK: psk, Devices: registry, Version: "test"})
	relayServer.SetBackend(gatewayServer.Handler())

	apiMux := http.NewServeMux()
	apiMux.Handle("/v1/", gatewayServer.Handler())

	tlsListener := httptest.NewUnstartedServer(relayServer.Handler())
	tlsListener.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	tlsListener.StartTLS()
	defer tlsListener.Close()

	// 客户端侧：客户端侧手上没有任何凭证——这正是客户端模式的定义。
	relayClient, err := relay.NewClient(relay.Config{
		BaseURL:     tlsListener.URL,
		PSK:         psk,
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "laptop-01", Name: "笔记本"},
	})
	if err != nil {
		t.Fatalf("构造转发客户端失败：%v", err)
	}
	clientSide := gateway.NewServer(&config.Config{}, nil, relayClient, nil, logger, nil, 0)

	clientAPI := httptest.NewServer(clientSide.Handler())
	defer clientAPI.Close()

	if code := postRelayChat(t, clientAPI.URL+"/v1/chat/completions"); code != http.StatusOK {
		t.Fatalf("客户端模式下的请求状态码 = %d，期望 200", code)
	}

	// 等事件落盘：记录是在响应写完之后才 append 的。
	records := waitForRelayRecords(t, events)
	found := false
	for _, record := range records {
		if record.Device == "laptop-01" {
			found = true
			if record.Model != "glm-5.3" {
				t.Errorf("记录的模型 = %q，期望 glm-5.3", record.Model)
			}
		}
	}
	if !found {
		t.Fatalf("事件里没有来自这台设备的记录：%+v", records)
	}
	if registry.NameOf("laptop-01") != "笔记本" {
		t.Errorf("网关没有把设备名登记下来：%q", registry.NameOf("laptop-01"))
	}
}

// TestRelayLoopRejectsWrongPSKFromClientSide 密钥不对时，
// 客户端侧要把它变成一次明确的失败，而不是拿到一个空的成功的响应。
func TestRelayLoopRejectsWrongPSKFromClientSide(t *testing.T) {
	dir := t.TempDir()
	certificate, fingerprint, err := relaytls.LoadOrCreate(dir+"/cert.pem", dir+"/key.pem")
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	relayServer := relay.NewServer(relay.Deps{PSK: "REAL-PSK-REAL-PSK-REAL-PSK-REAL"})
	relayServer.SetBackend(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	tlsListener := httptest.NewUnstartedServer(relayServer.Handler())
	tlsListener.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	tlsListener.StartTLS()
	defer tlsListener.Close()

	relayClient, err := relay.NewClient(relay.Config{
		BaseURL:     tlsListener.URL,
		PSK:         "WRONG-PSK-WRONG-PSK-WRONG-PSK-XY",
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "laptop-02"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	logger, err := logging.New("", false)
	if err != nil {
		t.Fatalf("构造日志器失败：%v", err)
	}
	clientSide := gateway.NewServer(&config.Config{}, nil, relayClient, nil, logger, nil, 0)
	clientAPI := httptest.NewServer(clientSide.Handler())
	defer clientAPI.Close()

	if code := postRelayChat(t, clientAPI.URL+"/v1/chat/completions"); code == http.StatusOK {
		t.Fatal("密钥不对却得到了 200")
	}
}

// waitForRelayRecords 轮询等事件落盘。
func waitForRelayRecords(t *testing.T, events *journal.Journal) []journal.Record {
	t.Helper()
	for attempt := 0; attempt < 60; attempt++ {
		records, err := events.ReadSince(time.Time{})
		if err != nil {
			t.Fatalf("读取事件失败：%v", err)
		}
		if len(records) > 0 {
			return records
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等不到事件落盘")
	return nil
}

// postChat 发一次对话请求并返回状态码。
func postRelayChat(t *testing.T, url string) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    "glm-5.3",
		"messages": []map[string]string{{"role": "user", "content": "你好"}},
		"stream":   false,
	})
	if err != nil {
		t.Fatalf("构造请求体失败：%v", err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

// newSSEBackend 是一个返回标准 SSE 的假 WorkBuddy 后端。
func newSSEBackend() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"choices":[{"delta":{"content":"你好"},"index":0}],"model":"glm-5.3"}`,
			`data: {"choices":[{"finish_reason":"stop","index":0}],"model":"glm-5.3","usage":{"prompt_tokens":10,"completion_tokens":2,"credit":0.05}}`,
			`data: [DONE]`,
		} {
			_, _ = w.Write([]byte(line + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
}

// liveBackend 是一个最小够用的 gateway.Backend：把请求发到一个给定的地址。
type liveBackend struct{ baseURL string }

func (l *liveBackend) ChatStream(ctx context.Context, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		l.baseURL+"/v1/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(request)
}

func (l *liveBackend) Models(context.Context) ([]upstream.ModelInfo, error) {
	return []upstream.ModelInfo{{ID: "glm-5.3"}}, nil
}

// TestRelayRequestsBypassAPIKey 两个端口的鉴权彼此独立：
// 配了 -api-key 也不该要求来自转发端口的请求带上它，
// 否则开了密钥的用户会看到所有客户端都 401。
func TestRelayRequestsBypassAPIKey(t *testing.T) {
	logger, err := logging.New("", false)
	if err != nil {
		t.Fatalf("构造日志器失败：%v", err)
	}
	// 故意在网关侧配置一个本地密钥；转发请求不带它。
	server := gateway.NewServer(&config.Config{APIKey: "local-secret"}, nil,
		&liveBackend{baseURL: "http://127.0.0.1:1"}, nil, logger, nil, 0)

	direct := httptest.NewServer(server.Handler())
	defer direct.Close()
	if code := postRelayChat(t, direct.URL+"/v1/chat/completions"); code != http.StatusUnauthorized {
		t.Errorf("本机直连不带密钥：状态码 = %d，期望 401", code)
	}

	// 走上真实的转发链路，由 relay 打上来源标记。
	dir := t.TempDir()
	certificate, fingerprint, err := relaytls.LoadOrCreate(dir+"/cert.pem", dir+"/key.pem")
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	relayServer := relay.NewServer(relay.Deps{PSK: "REAL-PSK-REAL-PSK-REAL-PSK-REAL"})
	relayServer.SetBackend(server.Handler())
	tlsListener := httptest.NewUnstartedServer(relayServer.Handler())
	tlsListener.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	tlsListener.StartTLS()
	defer tlsListener.Close()

	relayClient, err := relay.NewClient(relay.Config{
		BaseURL:     tlsListener.URL,
		PSK:         "REAL-PSK-REAL-PSK-REAL-PSK-REAL",
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "laptop-03"},
	})
	if err != nil {
		t.Fatalf("构造转发客户端失败：%v", err)
	}
	clientSide := gateway.NewServer(&config.Config{}, nil, relayClient, nil, logger, nil, 0)
	clientAPI := httptest.NewServer(clientSide.Handler())
	defer clientAPI.Close()

	if code := postRelayChat(t, clientAPI.URL+"/v1/chat/completions"); code == http.StatusUnauthorized {
		t.Fatal("转发请求被本机的 -api-key 拦下了——两套鉴权串在了一起")
	}
}
