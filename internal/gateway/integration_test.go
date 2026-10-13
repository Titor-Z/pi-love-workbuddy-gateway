// 这一组是集成测试：用一个假的 WorkBuddy 后端把整条请求路径跑通，
// 覆盖单测覆盖不到的地方——鉴权头是否真的发出去了、SSE 是否真的透传、
// 后端错误是否被正确翻译、令牌过期时是否真的去续期并回写文件。
//
// 全程离线，不碰真实网络，也不需要 WorkBuddy 登录过。
package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/gateway"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// 测试用的固定密钥与令牌。密钥必须是合法 base64 形态，但不是真实密钥。
const (
	testSecret       = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testAccessToken  = "test-access-token"
	testRefreshToken = "test-refresh-token"
	testRefreshed    = "refreshed-access-token"
	testUID          = "test-uid"
)

// chatSSE 是假后端返回的对话流：两段正文 + 一段用量。
const chatSSE = `data: {"id":"cmb-1","model":"glm-5.3","choices":[{"index":0,"delta":{"content":"你"}}]}

data: {"id":"cmb-1","model":"glm-5.3","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":"stop"}]}

data: {"id":"cmb-1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}

data: [DONE]

`

// capturedRequest 记录假后端收到的一次请求。
type capturedRequest struct {
	Header http.Header
	Body   map[string]any
}

// fakeBackend 是一个可编程的假 WorkBuddy 后端。
type fakeBackend struct {
	server *httptest.Server

	mu           sync.Mutex
	chats        []capturedRequest
	refreshes    int
	chatStatus   int
	chatRaw      string
	refreshCode  int
	refreshToken string
	modelsJSON   string
}

// newFakeBackend 起一个假后端，默认行为是「一切正常」。
func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	backend := &fakeBackend{
		chatStatus:   http.StatusOK,
		chatRaw:      chatSSE,
		refreshCode:  0,
		refreshToken: testRefreshed,
		modelsJSON: `{"code":0,"data":{"models":[
			{"id":"glm-5.3","name":"GLM-5.3","credits":"x0.79","maxInputTokens":1000000,
			 "maxOutputTokens":64000,"supportsImages":true,"supportsToolCall":true,
			 "supportsReasoning":true,"reasoning":{"supportedEfforts":["low","high","max"]}},
			{"id":"hy3","name":"Hy3","credits":"x0.00 credits","maxInputTokens":192000,
			 "maxOutputTokens":64000,"supportsImages":true,"supportsReasoning":true},
			{"id":"kling-v3-i2v","name":"Kling-V3-I2V","tags":["image-to-video"]}
		]}}`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/chat/completions", backend.handleChat)
	mux.HandleFunc("/v2/plugin/auth/token/refresh", backend.handleRefresh)
	mux.HandleFunc("/console/enterprises/personal/models", backend.handleModels)

	backend.server = httptest.NewServer(mux)
	t.Cleanup(backend.server.Close)
	return backend
}

// URL 返回假后端的地址。
func (f *fakeBackend) URL() string { return f.server.URL }

// handleChat 记录请求，并按配置返回对话流或错误。
func (f *fakeBackend) handleChat(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	f.mu.Lock()
	f.chats = append(f.chats, capturedRequest{Header: r.Header.Clone(), Body: body})
	status, payload := f.chatStatus, f.chatRaw
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, payload)
}

// handleRefresh 返回一个新的访问令牌。
func (f *fakeBackend) handleRefresh(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.refreshes++
	code, token := f.refreshCode, f.refreshToken
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "刷新被拒绝"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code": 0,
		"data": map[string]any{"accessToken": token, "expiresIn": 3600},
	})
}

// handleModels 返回模型目录。
func (f *fakeBackend) handleModels(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	payload := f.modelsJSON
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, payload)
}

// lastChat 取出最近一次对话请求。
func (f *fakeBackend) lastChat(t *testing.T) capturedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.chats) == 0 {
		t.Fatal("假后端没有收到任何对话请求")
	}
	return f.chats[len(f.chats)-1]
}

// chatCount 返回假后端收到的对话请求数。
func (f *fakeBackend) chatCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chats)
}

// refreshCount 返回续期请求数。
func (f *fakeBackend) refreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes
}

// writeAuthFile 造一份加密的登录态文件，返回它的路径。
// 未知字段刻意保留，用来验证续期回写不会丢掉它们。
func writeAuthFile(t *testing.T, dir, accessToken, refreshToken string, expiresAt time.Time) string {
	t.Helper()
	cipher, err := atrest.NewCipher(testSecret)
	if err != nil {
		t.Fatalf("构造解密器失败：%v", err)
	}
	sealedAccess, err := cipher.Encrypt(accessToken)
	if err != nil {
		t.Fatalf("加密访问令牌失败：%v", err)
	}
	sealedRefresh, err := cipher.Encrypt(refreshToken)
	if err != nil {
		t.Fatalf("加密刷新令牌失败：%v", err)
	}
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken":  sealedAccess,
			"refreshToken": sealedRefresh,
			"domain":       "www.codebuddy.cn",
			"expiresAt":    float64(expiresAt.UnixMilli()),
		},
		"account":  map[string]any{"uid": testUID, "enterpriseId": "test-enterprise"},
		"accounts": "这个字段必须原样保留",
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化登录态失败：%v", err)
	}
	path := filepath.Join(dir, "workbuddy-desktop.info")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入登录态失败：%v", err)
	}
	return path
}

// newGateway 把网关接到假后端上，返回处理器与登录态文件路径。
func newGateway(t *testing.T, backend *fakeBackend, apiKey string, expiresAt time.Time) (http.Handler, string) {
	t.Helper()
	authFile := writeAuthFile(t, t.TempDir(), testAccessToken, testRefreshToken, expiresAt)

	cipher, err := atrest.NewCipher(testSecret)
	if err != nil {
		t.Fatalf("构造解密器失败：%v", err)
	}
	promotions, err := catalog.LoadPromotions("")
	if err != nil {
		t.Fatalf("加载促销配置失败：%v", err)
	}
	logger, err := logging.New(filepath.Join(t.TempDir(), "test.log"), false)
	if err != nil {
		t.Fatalf("构造日志器失败：%v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	// 后端地址与登录态路径都指到测试环境，不碰任何真实资源。
	cred := credential.NewManager(authFile, cipher, nil, backend.URL())
	server := gateway.NewServer(
		&config.Config{ListenAddr: "127.0.0.1:8788", APIKey: apiKey},
		cred,
		upstream.NewClient(cred, backend.URL()),
		promotions,
		logger,
		nil, // 事件文件改为由专门的测试注入
	)
	return server.Handler(), authFile
}

// postChat 向网关发一次对话请求。
func postChat(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// later 返回一个「还早」的有效期。
func later() time.Time { return time.Now().Add(24 * time.Hour) }

// ---------- 对话：正常路径 ----------

// TestChatNonStreamingAggregatesSSE 非流式请求要把后端的 SSE 聚合成一个对象。
func TestChatNonStreamingAggregatesSSE(t *testing.T) {
	backend := newFakeBackend(t)
	handler, _ := newGateway(t, backend, "", later())

	recorder := postChat(t, handler, `{"model":"glm-5.3","messages":[{"role":"user","content":"你好"}]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，正文：%s", recorder.Code, recorder.Body.String())
	}
	var result struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message      map[string]any `json:"message"`
			FinishReason string         `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if result.Object != "chat.completion" {
		t.Errorf("object = %q", result.Object)
	}
	if result.Model != "glm-5.3" {
		t.Errorf("model = %q", result.Model)
	}
	if got := result.Choices[0].Message["content"]; got != "你好" {
		t.Errorf("content = %v，期望 你好", got)
	}
	if result.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q", result.Choices[0].FinishReason)
	}
	if result.Usage["total_tokens"] != float64(9) {
		t.Errorf("用量没有透传：%v", result.Usage)
	}
}

// TestChatStreamingPassesBytesThrough 流式请求必须逐字节原样转发，
// 网关不做任何协议翻译，多一个空格都可能让客户端的 SSE 解析器出问题。
func TestChatStreamingPassesBytesThrough(t *testing.T) {
	backend := newFakeBackend(t)
	handler, _ := newGateway(t, backend, "", later())

	recorder := postChat(t, handler,
		`{"model":"glm-5.3","stream":true,"messages":[{"role":"user","content":"你好"}]}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := recorder.Body.String(); got != chatSSE {
		t.Errorf("转发内容被改动了\n实际：%q\n期望：%q", got, chatSSE)
	}
}

// TestChatForwardsAuthHeadersAndRewritesBody 这是本项目最核心的契约：
// 鉴权头必须带上，请求体必须被正确改写后再发出去。
func TestChatForwardsAuthHeadersAndRewritesBody(t *testing.T) {
	backend := newFakeBackend(t)
	handler, _ := newGateway(t, backend, "", later())

	// 客户端要非流式、带一个未知字段、还用 developer 角色。
	postChat(t, handler, `{
		"model":"glm-5.3","stream":false,"logprobs":true,
		"messages":[{"role":"developer","content":"规则"},{"role":"user","content":"你好"}]
	}`)

	captured := backend.lastChat(t)

	if got := captured.Header.Get("Authorization"); got != "Bearer "+testAccessToken {
		t.Errorf("Authorization = %q，期望带上访问令牌", got)
	}
	for name, want := range map[string]string{
		"X-User-Id":       testUID,
		"X-Enterprise-Id": "test-enterprise",
		"X-Tenant-Id":     "test-enterprise",
		"X-Domain":        "www.codebuddy.cn",
	} {
		if got := captured.Header.Get(name); got != want {
			t.Errorf("%s = %q，期望 %q", name, got, want)
		}
	}
	if captured.Header.Get("User-Agent") == "" {
		t.Error("缺少 User-Agent")
	}

	// 后端只支持流式，即便客户端要非流式也必须发 stream=true。
	if captured.Body["stream"] != true {
		t.Errorf("上游 stream = %v，期望 true", captured.Body["stream"])
	}
	options, _ := captured.Body["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Errorf("缺少 stream_options.include_usage：%v", captured.Body["stream_options"])
	}
	if _, ok := captured.Body["logprobs"]; ok {
		t.Error("白名单外的字段被透传了")
	}
	messages, _ := captured.Body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages 条目数 = %d", len(messages))
	}
	if role := messages[0].(map[string]any)["role"]; role != "system" {
		t.Errorf("developer 应被改写成 system，实际 %v", role)
	}
}

// ---------- 对话：错误路径 ----------

// TestChatRejectsBadInputBeforeCallingBackend 参数不对要在本地就拦下，
// 不为一个注定失败的请求消耗后端的额度和配额。
func TestChatRejectsBadInputBeforeCallingBackend(t *testing.T) {
	cases := map[string]string{
		"请求体不是 JSON":  `{这不是 JSON`,
		"缺少 messages": `{"model":"glm-5.3"}`,
		"messages 为空": `{"messages":[]}`,
	}
	for name, body := range cases {
		backend := newFakeBackend(t)
		handler, _ := newGateway(t, backend, "", later())

		recorder := postChat(t, handler, body)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s：状态码 = %d，期望 400", name, recorder.Code)
		}
		if backend.chatCount() != 0 {
			t.Errorf("%s：不该把请求转发给后端", name)
		}
	}
}

// TestChatRequiresAPIKey 配了本地密钥就必须校验。
func TestChatRequiresAPIKey(t *testing.T) {
	backend := newFakeBackend(t)
	handler, _ := newGateway(t, backend, "local-secret", later())
	body := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`

	if recorder := postChat(t, handler, body); recorder.Code != http.StatusUnauthorized {
		t.Errorf("不带密钥：状态码 = %d，期望 401", recorder.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer local-secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("带对密钥：状态码 = %d，期望 200", recorder.Code)
	}
}

// TestChatPassesJSONErrorThrough 后端返回的 JSON 错误要原样转给客户端，
// 连它自己的错误码一起保留——那些码有具体含义，包装掉就查不出问题了。
func TestChatPassesJSONErrorThrough(t *testing.T) {
	backend := newFakeBackend(t)
	backend.chatStatus = http.StatusForbidden
	backend.chatRaw = `{"code":11128,"msg":"安全策略拦截"}`
	handler, _ := newGateway(t, backend, "", later())

	recorder := postChat(t, handler, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("状态码 = %d，期望 403", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "11128") {
		t.Errorf("后端原始错误码丢了：%s", recorder.Body.String())
	}
}

// TestChatWrapsNonJSONError 后端返回 HTML 之类的非 JSON 时，
// 要包装成标准错误结构，否则客户端 SDK 解析不了。
func TestChatWrapsNonJSONError(t *testing.T) {
	backend := newFakeBackend(t)
	backend.chatStatus = http.StatusBadGateway
	backend.chatRaw = "<html>502 Bad Gateway</html>"
	handler, _ := newGateway(t, backend, "", later())

	recorder := postChat(t, handler, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)

	var payload struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%s", recorder.Body.String())
	}
	if payload.Error.Type != "upstream_error" {
		t.Errorf("type = %q，期望 upstream_error", payload.Error.Type)
	}
}

// ---------- 令牌续期 ----------

// TestExpiredTokenTriggersRefreshAndWritesBack 令牌过期时要真去续期，
// 用上新令牌，并把新令牌加密写回文件（否则 WorkBuddy 客户端会读不了）。
func TestExpiredTokenTriggersRefreshAndWritesBack(t *testing.T) {
	backend := newFakeBackend(t)
	handler, authFile := newGateway(t, backend, "", time.Now().Add(-time.Hour))

	recorder := postChat(t, handler, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，正文：%s", recorder.Code, recorder.Body.String())
	}
	if backend.refreshCount() != 1 {
		t.Fatalf("续期次数 = %d，期望 1", backend.refreshCount())
	}
	if got := backend.lastChat(t).Header.Get("Authorization"); got != "Bearer "+testRefreshed {
		t.Errorf("对话用的还是旧令牌：%q", got)
	}

	// 回写的文件必须是加密格式，且内容是新令牌。
	raw, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatalf("读取登录态失败：%v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("登录态不是合法 JSON：%v", err)
	}
	auth, _ := doc["auth"].(map[string]any)
	if _, ok := auth["accessToken"].(map[string]any); !ok {
		t.Fatalf("回写的访问令牌不是加密信封，明文回写会让客户端重置登录态：%v", auth["accessToken"])
	}
	cipher, _ := atrest.NewCipher(testSecret)
	plain, err := cipher.Decrypt(auth["accessToken"])
	if err != nil {
		t.Fatalf("回写的令牌解不开：%v", err)
	}
	if plain != testRefreshed {
		t.Errorf("回写的令牌 = %q，期望 %q", plain, testRefreshed)
	}
	// 未识别字段必须原样保留，否则会破坏客户端的其它状态。
	if doc["accounts"] != "这个字段必须原样保留" {
		t.Errorf("未识别字段丢失或被改动：%v", doc["accounts"])
	}
	account, _ := doc["account"].(map[string]any)
	if account["uid"] != testUID {
		t.Errorf("account 段被改动了：%v", account)
	}
}

// TestValidTokenSkipsRefresh 令牌还有效时不该多发一次续期请求。
func TestValidTokenSkipsRefresh(t *testing.T) {
	backend := newFakeBackend(t)
	handler, _ := newGateway(t, backend, "", later())

	postChat(t, handler, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)

	if got := backend.refreshCount(); got != 0 {
		t.Errorf("续期次数 = %d，期望 0", got)
	}
}

// TestRefreshFailureSurfaces 续期被拒时要明确报错，
// 不能默默用一个已经失效的令牌继续请求。
func TestRefreshFailureSurfaces(t *testing.T) {
	backend := newFakeBackend(t)
	backend.refreshCode = 40001
	handler, _ := newGateway(t, backend, "", time.Now().Add(-time.Hour))

	recorder := postChat(t, handler, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)

	if recorder.Code == http.StatusOK {
		t.Fatal("续期失败时不该返回 200")
	}
	if !strings.Contains(recorder.Body.String(), "刷新") {
		t.Errorf("错误信息应说明是续期失败：%s", recorder.Body.String())
	}
	if backend.chatCount() != 0 {
		t.Error("续期失败后不该继续发对话请求")
	}
}

// ---------- 其余端点 ----------

// TestModelsEndpointFiltersNonChatModels 图片、视频模型不能出现在列表里。
func TestModelsEndpointFiltersNonChatModels(t *testing.T) {
	backend := newFakeBackend(t)
	handler, _ := newGateway(t, backend, "", later())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID        string `json:"id"`
			OwnedBy   string `json:"owned_by"`
			WorkBuddy struct {
				Free      bool   `json:"free"`
				Credits   string `json:"credits"`
				FreeUntil string `json:"free_until"`
			} `json:"workbuddy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if payload.Object != "list" {
		t.Errorf("object = %q", payload.Object)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("模型数 = %d，期望 2（视频模型应被过滤）", len(payload.Data))
	}
	if payload.Data[0].ID != "glm-5.3" || payload.Data[0].OwnedBy != "workbuddy" {
		t.Errorf("条目不对：%+v", payload.Data[0])
	}
	if payload.Data[0].WorkBuddy.Credits != "0.79x" {
		t.Errorf("费用标记没带上：%+v", payload.Data[0].WorkBuddy)
	}
	if !payload.Data[1].WorkBuddy.Free {
		t.Errorf("x0.00 的模型应标记为免费：%+v", payload.Data[1].WorkBuddy)
	}
	if payload.Data[1].WorkBuddy.Credits != "" {
		t.Errorf("免费模型不该再显示倍率：%q", payload.Data[1].WorkBuddy.Credits)
	}
}

// TestHealthNeverLeaksToken 健康检查可以显示账号，但绝不能带出任何令牌。
func TestHealthNeverLeaksToken(t *testing.T) {
	backend := newFakeBackend(t)
	handler, _ := newGateway(t, backend, "", later())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, testUID) {
		t.Errorf("应显示账号 uid：%s", body)
	}
	for _, secret := range []string{testAccessToken, testRefreshToken, testSecret} {
		if strings.Contains(body, secret) {
			t.Errorf("健康检查泄漏了凭据 %q：%s", secret, body)
		}
	}
}
