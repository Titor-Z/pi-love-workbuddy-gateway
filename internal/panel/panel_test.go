package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/settings"
)

// newTestPanel 造一个只依赖临时目录的面板。
// 上游相关字段留空：碰到它们的接口不在这些用例的覆盖范围内。
func newTestPanel(t *testing.T) (*Panel, *Control, *journal.Journal) {
	t.Helper()

	dir := t.TempDir()
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

	control := NewControl()
	console, err := New(Deps{
		Version:        "test",
		StartedAt:      time.Now(),
		AuthFile:       filepath.Join(dir, "auth.info"),
		ListenAddr:     "127.0.0.1:8788",
		LogPath:        filepath.Join(dir, "gateway.log"),
		JournalPath:    filepath.Join(dir, "events.jsonl"),
		PromotionsPath: filepath.Join(dir, "promotions.json"),
		SettingsPath:   filepath.Join(dir, "settings.json"),
		Promotions:     &catalog.Promotions{},
		Journal:        events,
		Settings:       preferences,
		Logger:         logger,
		Control:        control,
	})
	if err != nil {
		t.Fatalf("建面板失败：%v", err)
	}
	return console, control, events
}

// call 发一个带随机数请求头的请求。
func call(t *testing.T, console *Panel, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set(nonceHeader, console.nonce)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	console.Handler().ServeHTTP(recorder, request)
	return recorder
}

// TestPingNeedsNoNonce 确认探活接口不需要凭据——前端要靠它判断后端在不在，
// 如果它也要凭据，页面就永远停在「连不上」。
func TestPingNeedsNoNonce(t *testing.T) {
	console, _, _ := newTestPanel(t)

	request := httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	recorder := httptest.NewRecorder()
	console.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"ok":true`) {
		t.Fatalf("响应体不含 ok：%s", recorder.Body.String())
	}
}

// TestAPIRejectsMissingNonce 确认没有页内随机数就读不到任何账户数据。
func TestAPIRejectsMissingNonce(t *testing.T) {
	console, _, _ := newTestPanel(t)

	for _, target := range []string{"/api/status", "/api/billing", "/api/usage",
		"/api/models", "/api/events", "/api/promotions"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		recorder := httptest.NewRecorder()
		console.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s 状态码 = %d，想要 403", target, recorder.Code)
		}
		// 错误信息里不能夹带任何配置内容。
		if strings.Contains(recorder.Body.String(), "auth.info") {
			t.Errorf("%s 的响应泄露了文件路径：%s", target, recorder.Body.String())
		}
	}
}

// TestAPIRejectsForeignOriginOnWrite 确认跨站发起的写请求被拒。
func TestAPIRejectsForeignOriginOnWrite(t *testing.T) {
	console, _, _ := newTestPanel(t)

	recorder := call(t, console, http.MethodPut, "/api/promotions",
		map[string]any{"promotions": map[string]any{}},
		map[string]string{"Origin": "https://evil.example"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("跨站写请求状态码 = %d，想要 403", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "bad_origin") {
		t.Fatalf("响应体未说明原因：%s", recorder.Body.String())
	}
}

// TestHomeInjectsNonce 确认首页里带上了本次进程的随机数，且没有残留占位符。
func TestHomeInjectsNonce(t *testing.T) {
	console, _, _ := newTestPanel(t)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	console.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, console.nonce) {
		t.Fatal("首页里没有本次进程的随机数")
	}
	if strings.Contains(body, noncePlaceholder) {
		t.Fatal("首页里残留了随机数占位符")
	}
	// 首页必须禁缓存：缓存下来会让下次启动的随机数失效。
	if cache := recorder.Header().Get("Cache-Control"); !strings.Contains(cache, "no-store") {
		t.Fatalf("Cache-Control = %q，想要 no-store", cache)
	}
}

// TestUnknownPathServesHome 确认前端路由的深链接能落到首页。
func TestUnknownPathServesHome(t *testing.T) {
	console, _, _ := newTestPanel(t)

	for _, target := range []string{"/models", "/settings/deep/link", "/assets/nope.js"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		recorder := httptest.NewRecorder()
		console.Handler().ServeHTTP(recorder, request)

		if !strings.Contains(recorder.Body.String(), console.nonce) {
			t.Errorf("%s 没有回落到首页", target)
		}
	}
}

// TestBuildIndexInsertsMetaWhenPlaceholderMissing 确认占位符丢了也能注入：
// 用户换了自己构建的前端时不该直接白屏。
func TestBuildIndexInsertsMetaWhenPlaceholderMissing(t *testing.T) {
	raw := []byte("<html><head><title>x</title></head><body></body></html>")
	// 借用生产的注入逻辑，但绕过嵌入资源。
	injected := insertNonce(raw, "ABC123")

	if !bytes.Contains(injected, []byte(`content="ABC123"`)) {
		t.Fatalf("没有注入 meta：%s", injected)
	}
	if !bytes.Contains(injected, []byte("</head>")) {
		t.Fatalf("注入后结构被破坏：%s", injected)
	}
}

// TestUsageAggregate 覆盖聚合的核心规则。
func TestUsageAggregate(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.Local)
	from := startOfDay(now).AddDate(0, 0, -2) // 3 天窗口

	records := []journal.Record{
		{Time: from.Add(2 * time.Hour), Kind: journal.KindChat, Model: "glm-5.3",
			Credit: 1.12, In: 18, Out: 89, Cached: 17},
		{Time: from.Add(3 * time.Hour), Kind: journal.KindChat, Model: "glm-5.3", Credit: 0.25},
		{Time: from.AddDate(0, 0, 1).Add(time.Hour), Kind: journal.KindChat,
			Model: "minimax-m3", Credit: 0.72, Blocked: true},
		{Time: now, Kind: journal.KindChat, Model: "minimax-m3", Credit: 0.5},
		// 非对话事件必须被排除：它们不是用户发起的用量。
		{Time: now, Kind: journal.KindRefresh, Message: "续期"},
		{Time: now, Kind: journal.KindModels, Message: "拉取目录"},
		// 窗口之外的事件必须被忽略。
		{Time: from.AddDate(0, 0, -1), Kind: journal.KindChat, Model: "old", Credit: 9},
	}

	got := aggregate(records, from, now, 3)

	if len(got.Days) != 3 {
		t.Fatalf("天数 = %d，想要 3", len(got.Days))
	}
	if got.Days[0].Requests != 2 || got.Days[1].Requests != 1 || got.Days[2].Requests != 1 {
		t.Fatalf("每日请求数 = %d/%d/%d，想要 2/1/1",
			got.Days[0].Requests, got.Days[1].Requests, got.Days[2].Requests)
	}
	if got.Days[0].Credit != 1.37 {
		t.Fatalf("第一天消耗 = %v，想要 1.37", got.Days[0].Credit)
	}
	if got.Days[0].ByModel["glm-5.3"] != 1.37 {
		t.Fatalf("第一天 glm-5.3 消耗 = %v，想要 1.37", got.Days[0].ByModel["glm-5.3"])
	}
	if got.Days[1].Blocked != 1 {
		t.Fatalf("第二天拦截数 = %d，想要 1", got.Days[1].Blocked)
	}
	if got.Totals.Requests != 4 {
		t.Fatalf("总请求数 = %d，想要 4", got.Totals.Requests)
	}
	if got.Totals.Credit != 2.59 {
		t.Fatalf("总消耗 = %v，想要 2.59", got.Totals.Credit)
	}
	if got.Totals.Blocked != 1 {
		t.Fatalf("总拦截数 = %d，想要 1", got.Totals.Blocked)
	}
	if got.Totals.BlockRate != 0.25 {
		t.Fatalf("拦截率 = %v，想要 0.25", got.Totals.BlockRate)
	}
	// 只有第一条带了 token 数，所以命中率就是 17/18。
	if got.Totals.In != 18 || got.Totals.Out != 89 || got.Totals.Cached != 17 {
		t.Fatalf("token 汇总不对：in=%d out=%d cached=%d",
			got.Totals.In, got.Totals.Out, got.Totals.Cached)
	}
	if got.Totals.CacheRate != 0.9444 {
		t.Fatalf("缓存命中率 = %v，想要 0.9444", got.Totals.CacheRate)
	}
	// 没有 token 数据时不出现除零。
	if got.Days[2].Requests != 1 || got.Days[2].Cached != 0 {
		t.Fatalf("缺 token 的那天不该被算成有缓存：%+v", got.Days[2])
	}
	// 按模型汇总要按消耗降序，且 glm 在前。
	if len(got.Models) != 2 || got.Models[0].Model != "glm-5.3" {
		t.Fatalf("模型汇总顺序不对：%+v", got.Models)
	}
	if got.Models[0].Credit != 1.37 || got.Models[1].Credit != 1.22 {
		t.Fatalf("模型汇总数值不对：%+v", got.Models)
	}
	if got.Models[0].Cached != 17 || got.Models[0].In != 18 {
		t.Fatalf("模型维度的 token 没有汇总：%+v", got.Models[0])
	}
}

// TestUsageAggregateEmpty 确认没有数据时不出现除零或假数据。
func TestUsageAggregateEmpty(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.Local)
	from := startOfDay(now).AddDate(0, 0, -13)

	got := aggregate(nil, from, now, 14)

	if len(got.Days) != 14 {
		t.Fatalf("天数 = %d，想要 14", len(got.Days))
	}
	if got.Totals.Requests != 0 || got.Totals.Credit != 0 || got.Totals.BlockRate != 0 {
		t.Fatalf("空数据却给出了统计值：%+v", got.Totals)
	}
	if got.Models == nil {
		t.Fatal("模型列表是 nil，前端会读到 null 而不是空数组")
	}
}

// TestUsageEndpointReadsJournal 确认接口端到端能读到事件文件里的数据。
func TestUsageEndpointReadsJournal(t *testing.T) {
	console, _, events := newTestPanel(t)

	events.Append(journal.Record{Kind: journal.KindChat, Model: "glm-5.3", Credit: 1.12})

	recorder := call(t, console, http.MethodGet, "/api/usage?days=7", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200", recorder.Code)
	}

	var payload usageResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(payload.Days) != 7 {
		t.Fatalf("天数 = %d，想要 7", len(payload.Days))
	}
	if payload.Totals.Credit != 1.12 {
		t.Fatalf("总消耗 = %v，想要 1.12", payload.Totals.Credit)
	}
}

// TestPromotionsRoundTrip 覆盖读取、写入、以及写完之后立刻生效。
func TestPromotionsRoundTrip(t *testing.T) {
	console, _, _ := newTestPanel(t)

	// 文件还不存在时要给出空对象，而不是报错——首次使用就是这个状态。
	recorder := call(t, console, http.MethodGet, "/api/promotions", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("首次读取状态码 = %d，想要 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"promotions":{}`) {
		t.Fatalf("首次读取应返回空对象：%s", recorder.Body.String())
	}

	payload := map[string]any{"promotions": map[string]any{
		"glm-5.3": map[string]any{"free_until": "2026-10-31", "note": "限时免费"},
	}}
	recorder = call(t, console, http.MethodPut, "/api/promotions", payload, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("写入状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}

	// 面板内存里的配置要立刻更新，不能等重启。
	if _, found := console.promotions().Lookup("glm-5.3"); !found {
		t.Fatal("写完之后面板没有加载到新配置")
	}

	// 再读一次，确认确实落了盘。
	recorder = call(t, console, http.MethodGet, "/api/promotions", nil, nil)
	if !strings.Contains(recorder.Body.String(), "限时免费") {
		t.Fatalf("回读没有拿到刚写的内容：%s", recorder.Body.String())
	}
}

// TestPromotionsRejectsBadPayload 确认坏数据不会落盘。
func TestPromotionsRejectsBadPayload(t *testing.T) {
	console, _, _ := newTestPanel(t)

	cases := map[string]string{
		"不是对象":    `[]`,
		"多出未知字段":  `{"promotions":{},"extra":1}`,
		"促销值不是对象": `{"promotions":{"glm-5.3":"免费"}}`,
	}
	for name, body := range cases {
		request := httptest.NewRequest(http.MethodPut, "/api/promotions", strings.NewReader(body))
		request.Header.Set(nonceHeader, console.nonce)
		recorder := httptest.NewRecorder()
		console.Handler().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s：状态码 = %d，想要 400", name, recorder.Code)
		}
	}

	// 被拒绝之后，内存里的配置不应被改动。
	if _, found := console.promotions().Lookup("glm-5.3"); found {
		t.Fatal("非法请求污染了内存中的配置")
	}
}

// TestControlStop 确认停止请求会被主循环收到。
func TestControlStop(t *testing.T) {
	console, control, _ := newTestPanel(t)

	recorder := call(t, console, http.MethodPost, "/api/control/stop", nil, nil)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("状态码 = %d，想要 202", recorder.Code)
	}
	if got := <-control.Actions(); got != ActionStop {
		t.Fatalf("收到的动作 = %v，想要 ActionStop", got)
	}
}

// TestControlRestart 确认重启请求会被主循环收到。
func TestControlRestart(t *testing.T) {
	console, control, _ := newTestPanel(t)

	recorder := call(t, console, http.MethodPost, "/api/control/restart", nil, nil)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("状态码 = %d，想要 202", recorder.Code)
	}
	if got := <-control.Actions(); got != ActionRestart {
		t.Fatalf("收到的动作 = %v，想要 ActionRestart", got)
	}
}

// TestControlDropsDuplicates 确认连点按钮不会堆积动作。
func TestControlDropsDuplicates(t *testing.T) {
	control := NewControl()
	for i := 0; i < 5; i++ {
		control.Stop()
	}
	if got := <-control.Actions(); got != ActionStop {
		t.Fatalf("收到的动作 = %v，想要 ActionStop", got)
	}
	select {
	case extra := <-control.Actions():
		t.Fatalf("堆积了多余动作：%v", extra)
	case <-time.After(20 * time.Millisecond):
	}
}

// TestRandomNonceIsUnique 确认每次启动的随机数不同——
// 相同就等于把跨站防护白送出去。
func TestRandomNonceIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		value, err := randomNonce()
		if err != nil {
			t.Fatalf("生成随机数失败：%v", err)
		}
		if len(value) != 64 {
			t.Fatalf("随机数长度 = %d，想要 64", len(value))
		}
		if seen[value] {
			t.Fatal("随机数重复了")
		}
		seen[value] = true
	}
}

// TestStaticRejectsPost 确认静态资源不接受写方法。
func TestStaticRejectsPost(t *testing.T) {
	console, _, _ := newTestPanel(t)

	request := httptest.NewRequest(http.MethodPost, "/index.html", strings.NewReader("x"))
	recorder := httptest.NewRecorder()
	console.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d，想要 405", recorder.Code)
	}
}
