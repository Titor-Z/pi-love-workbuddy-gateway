package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
)

// testSecret 是固定的 at-rest 根密钥，仅供测试派生解密器。
const testSecret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// TestParseEfforts reasoning 字段有两种写法：
// 字符串（只有默认档位）和对象（含可选档位集合）。搞混会导致
// pi 里凭空多出或丢失思考档位选择。
func TestParseEfforts(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"字符串写法没有可选集合", `"high"`, nil},
		{"对象写法取 supportedEfforts",
			`{"defaultEffort":"high","supportedEfforts":["low","high","max"]}`,
			[]string{"low", "high", "max"}},
		{"对象但没有 supportedEfforts", `{"defaultEffort":"medium"}`, nil},
		{"supportedEfforts 为空", `{"supportedEfforts":[]}`, nil},
		{"空字段", ``, nil},
		{"垃圾内容", `12345`, nil},
	}
	for _, testCase := range cases {
		got := parseEfforts(json.RawMessage(testCase.raw))
		if len(got) != len(testCase.want) {
			t.Errorf("%s：got %v，期望 %v", testCase.name, got, testCase.want)
			continue
		}
		for i := range got {
			if got[i] != testCase.want[i] {
				t.Errorf("%s：got %v，期望 %v", testCase.name, got, testCase.want)
				break
			}
		}
	}
}

// TestRawModelIsNonChat 图片、视频模型必须被过滤掉，
// 否则 pi 的模型列表里会混进根本没法对话的条目。
func TestRawModelIsNonChat(t *testing.T) {
	cases := []struct {
		name  string
		tags  []string
		nonCh bool
	}{
		{"对话模型", []string{"craft"}, false},
		{"没有标签", nil, false},
		{"文生图", []string{"text-to-image"}, true},
		{"图生图", []string{"image-to-image"}, true},
		{"文生视频", []string{"text-to-video"}, true},
		{"图生视频", []string{"image-to-video"}, true},
		{"混合标签", []string{"craft", "text-to-video"}, true},
	}
	for _, testCase := range cases {
		model := rawModel{ID: "m", Tags: testCase.tags}
		if got := model.isNonChat(); got != testCase.nonCh {
			t.Errorf("%s：isNonChat = %v，期望 %v", testCase.name, got, testCase.nonCh)
		}
	}
}

// TestRawModelToInfo 字段映射要一一对上，尤其别把
// supportsReasoning 与 reasoning 搞混（前者是布尔，后者是档位集合）。
func TestRawModelToInfo(t *testing.T) {
	model := rawModel{
		ID:               "glm-5.3",
		Name:             "GLM-5.3",
		Credits:          "x0.79",
		MaxInputTokens:   1000000,
		MaxOutputTokens:  64000,
		SupportsImages:   true,
		SupportsToolCall: true,
		SupportsThinking: true,
		Tags:             []string{"craft", "badge:夜间免费:#FF0000"},
		Reasoning:        json.RawMessage(`{"supportedEfforts":["low","high","max"]}`),
	}
	info := model.toInfo()

	if info.ID != "glm-5.3" || info.Name != "GLM-5.3" || info.Credits != "x0.79" {
		t.Errorf("基础字段不对：%+v", info)
	}
	if info.MaxInputTokens != 1000000 || info.MaxOutputTokens != 64000 {
		t.Errorf("窗口参数不对：%+v", info)
	}
	if !info.SupportsImages || !info.SupportsToolCall || !info.SupportsThinking {
		t.Errorf("能力标志不对：%+v", info)
	}
	if len(info.Efforts) != 3 || len(info.Tags) != 2 {
		t.Errorf("集合字段不对：%+v", info)
	}
}

// TestParseEffortsIsIndependentOfThinkingFlag 没有 supportedEfforts 时，
// 即便模型支持思考，也不该凭空造出档位。
func TestParseEffortsIsIndependentOfThinkingFlag(t *testing.T) {
	model := rawModel{
		ID:               "glm-5.2",
		SupportsThinking: true,
		Reasoning:        json.RawMessage(`{"defaultEffort":"medium"}`),
	}
	info := model.toInfo()
	if !info.SupportsThinking {
		t.Error("SupportsThinking 应当为真")
	}
	if info.Efforts != nil {
		t.Errorf("没有声明可选档位时不该造出档位：%v", info.Efforts)
	}
}

// rotatingCredential 是一份可以在请求之间被外部改写的登录态，
// 用来模拟 WorkBuddy 桌面端在网关请求途中轮换令牌。
type rotatingCredential struct {
	*credential.Manager
	path string
}

// rotate 换一枚新的访问令牌，就像桌面端刷新后写回文件那样。
func (r rotatingCredential) rotate(t *testing.T, token string) {
	t.Helper()
	cipher, err := atrest.NewCipher(testSecret)
	if err != nil {
		t.Fatalf("构造解密器失败：%v", err)
	}
	sealed, err := cipher.Encrypt(token)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken": sealed,
			"domain":      "www.codebuddy.cn",
			"expiresAt":   float64(time.Now().Add(24 * time.Hour).UnixMilli()),
		},
		"account": map[string]any{"uid": "test-uid", "enterpriseId": "test-enterprise"},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if err := os.WriteFile(r.path, raw, 0o600); err != nil {
		t.Fatalf("改写登录态失败：%v", err)
	}
}

// newRotatingCredential 造一份可轮换的登录态管理器。
func newRotatingCredential(t *testing.T, token string) rotatingCredential {
	t.Helper()
	cipher, err := atrest.NewCipher(testSecret)
	if err != nil {
		t.Fatalf("构造解密器失败：%v", err)
	}
	sealed, err := cipher.Encrypt(token)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	doc := map[string]any{
		"auth": map[string]any{
			"accessToken": sealed,
			"domain":      "www.codebuddy.cn",
			"expiresAt":   float64(time.Now().Add(24 * time.Hour).UnixMilli()),
		},
		"account": map[string]any{"uid": "test-uid", "enterpriseId": "test-enterprise"},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	path := filepath.Join(t.TempDir(), "workbuddy-desktop.info")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入登录态失败：%v", err)
	}
	return rotatingCredential{
		Manager: credential.NewManager(path, cipher, nil, ""),
		path:    path,
	}
}

// countingServer 记录收到的请求次数与每次携带的 Authorization。
type countingServer struct {
	server *httptest.Server
	hits   int
	tokens []string
}

// newCountingServer 起一个假后端，用 handler 决定每次响应。
func newCountingServer(t *testing.T, handler func(attempt int, w http.ResponseWriter, r *http.Request)) *countingServer {
	t.Helper()
	rec := &countingServer{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits++
		rec.tokens = append(rec.tokens, r.Header.Get("Authorization"))
		handler(rec.hits, w, r)
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

// TestChatStreamRetriesOnceAfterTokenRotation 桌面端在网关发出请求的
// 同一瞬间轮换令牌时，后端会回 401。此时必须重读登录态再试一次，
// 而且只试一次——否则真的失效时会变成无效轰炸。
func TestChatStreamRetriesOnceAfterTokenRotation(t *testing.T) {
	cred := newRotatingCredential(t, "stale-token")
	rec := newCountingServer(t, func(attempt int, w http.ResponseWriter, _ *http.Request) {
		// 第一次请求时轮换令牌，模拟桌面端并发写回登录态。
		if attempt == 1 {
			cred.rotate(t, "fresh-token")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"msg":"token rotated"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	})

	client := NewClient(cred.Manager, rec.server.URL)
	resp, err := client.ChatStream(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatalf("令牌被轮换后应当自动恢复，实际报错：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重试后状态码 = %d，期望 200", resp.StatusCode)
	}
	if rec.hits != 2 {
		t.Fatalf("请求次数 = %d，期望 2（一次原始 + 一次重试）", rec.hits)
	}
	if rec.tokens[0] != "Bearer stale-token" {
		t.Errorf("首次令牌 = %q", rec.tokens[0])
	}
	if rec.tokens[1] != "Bearer fresh-token" {
		t.Errorf("重试令牌 = %q，应当是用重读登录态得到的新令牌", rec.tokens[1])
	}
}

// TestChatStreamDoesNotRetryWhenTokenUnchanged 重读登录态后令牌没变，
// 说明不是竞态而是真的失效，必须报错且不再重试。
func TestChatStreamDoesNotRetryWhenTokenUnchanged(t *testing.T) {
	cred := newRotatingCredential(t, "same-token")
	rec := newCountingServer(t, func(attempt int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"msg":"expired"}`))
	})

	client := NewClient(cred.Manager, rec.server.URL)
	resp, err := client.ChatStream(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err == nil {
		resp.Body.Close()
		t.Fatal("令牌未变化时的 401 应当报错，而不是静默重试")
	}
	if !errors.Is(err, credential.ErrUnauthorized) {
		t.Fatalf("错误应包裹 ErrUnauthorized，实际：%v", err)
	}
	if rec.hits != 1 {
		t.Fatalf("请求次数 = %d，期望 1（不重试）", rec.hits)
	}
	if !strings.Contains(err.Error(), "重新登录") && !strings.Contains(err.Error(), "失效") {
		t.Errorf("错误信息应告诉用户怎么办，实际：%v", err)
	}
}

// TestChatStreamDoesNotRetrySecondUnauthorized 重试后依然 401 时，
// 必须停在第二次，不能变成重试循环。
func TestChatStreamDoesNotRetrySecondUnauthorized(t *testing.T) {
	cred := newRotatingCredential(t, "first-token")
	rec := newCountingServer(t, func(attempt int, w http.ResponseWriter, _ *http.Request) {
		// 每次都先轮换，让「令牌变了」这个条件恒成立，
		// 但后端始终回 401——这时仍然只能试两次。
		cred.rotate(t, fmt.Sprintf("token-%d", attempt+1))
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"msg":"revoked"}`))
	})

	client := NewClient(cred.Manager, rec.server.URL)
	_, err := client.ChatStream(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err == nil {
		t.Fatal("始终 401 时应报错")
	}
	if rec.hits != 2 {
		t.Fatalf("请求次数 = %d，期望 2（上限）", rec.hits)
	}
}

// TestChatStreamPassesThroughNonUnauthorized 非 401 的状态码必须原样返回，
// 重试逻辑不能把后端的正常错误响应吞掉。
func TestChatStreamPassesThroughNonUnauthorized(t *testing.T) {
	cred := newRotatingCredential(t, "token")
	for _, status := range []int{http.StatusBadGateway, http.StatusTooManyRequests, http.StatusForbidden} {
		rec := newCountingServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":1,"msg":"nope"}`))
		})
		client := NewClient(cred.Manager, rec.server.URL)
		resp, err := client.ChatStream(context.Background(), []byte(`{"model":"m","messages":[]}`))
		if err != nil {
			t.Fatalf("HTTP %d 不应报错：%v", status, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != status {
			t.Errorf("状态码 = %d，期望 %d", resp.StatusCode, status)
		}
		if string(body) != `{"code":1,"msg":"nope"}` {
			t.Errorf("响应体被改动了：%s", body)
		}
		if rec.hits != 1 {
			t.Errorf("HTTP %d 时请求次数 = %d，期望 1（不重试）", status, rec.hits)
		}
	}
}

// TestChatStreamRespectsCancelledContext 客户端已经取消时不该重发请求。
func TestChatStreamRespectsCancelledContext(t *testing.T) {
	cred := newRotatingCredential(t, "token")
	rec := newCountingServer(t, func(attempt int, w http.ResponseWriter, _ *http.Request) {
		if attempt == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	client := NewClient(cred.Manager, rec.server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 请求前就取消
	if _, err := client.ChatStream(ctx, []byte(`{"model":"m","messages":[]}`)); err == nil {
		t.Fatal("ctx 已取消时应当报错")
	}
	if rec.hits > 1 {
		t.Fatalf("请求次数 = %d，ctx 已取消时不该重发", rec.hits)
	}
}

// TestRetryPicksUpTokenWhenMtimeUnchanged 文件系统的修改时间粒度可能粗到
// 一秒（部分 Windows/FAT 场景），桌面端在同一秒内改写登录态时，仅靠 mtime
// 比对认不出变化。401 重试必须强制重读，否则会一直拿着已作废的令牌。
func TestRetryPicksUpTokenWhenMtimeUnchanged(t *testing.T) {
	cred := newRotatingCredential(t, "old-token")
	// 把 mtime 固定成一个粗粒度时刻。必须在第一次 Headers() **之前**设好，
	// 否则缓存里记下的是真实当前时间，后面的 Chtimes 反而造成了差异。
	fixed := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := os.Chtimes(cred.path, fixed, fixed); err != nil {
		t.Fatalf("预设时间失败：%v", err)
	}

	rec := newCountingServer(t, func(attempt int, w http.ResponseWriter, _ *http.Request) {
		if attempt == 1 {
			cred.rotate(t, "new-token")
			// 轮换后把 mtime 拨回与上次 stat 完全相同的值。
			if err := os.Chtimes(cred.path, fixed, fixed); err != nil {
				t.Fatalf("设置时间失败：%v", err)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	client := NewClient(cred.Manager, rec.server.URL)
	resp, err := client.ChatStream(context.Background(), []byte(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatalf("mtime 未变时也应当恢复，实际报错：%v", err)
	}
	defer resp.Body.Close()
	if rec.hits != 2 {
		t.Fatalf("请求次数 = %d，期望 2", rec.hits)
	}
	if rec.tokens[1] != "Bearer new-token" {
		t.Fatalf("重试令牌 = %q，mtime 未变时必须靠强制重读拿到新令牌", rec.tokens[1])
	}
}
