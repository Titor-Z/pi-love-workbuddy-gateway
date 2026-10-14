package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// TestGateSerializesOverflow 上限为 1 时第二个请求必须等到第一个结束。
//
// 这是 Gate 的全部意义：与其让请求飞到后端被随机 429 拒绝，
// 不如在这里排队——排队的结果可预期，也可向用户解释。
func TestGateSerializesOverflow(t *testing.T) {
	gate := NewGate(1)

	release, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatalf("首个请求不该被拒：%v", err)
	}

	queued := make(chan error, 1)
	go func() {
		_, err := gate.Acquire(t.Context())
		queued <- err
	}()

	select {
	case err := <-queued:
		t.Fatalf("第二个请求本应排队，实际立刻返回：%v", err)
	case <-time.After(80 * time.Millisecond):
		// 还在排队，符合预期
	}

	release()
	select {
	case err := <-queued:
		if err != nil {
			t.Fatalf("第一个结束后第二个应该进得去，实际：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("释放名额后第二个请求依然没进来")
	}
}

// TestGateUnlimitedNeverBlocks 0 表示不限：不该引入任何等待，
// 也不该为此保留一个超大容量的信号量。
func TestGateUnlimitedNeverBlocks(t *testing.T) {
	gate := NewGate(0)
	if !gate.unlimited() {
		t.Fatal("0 应当表示不限")
	}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			release, err := gate.Acquire(t.Context())
			if err == nil {
				release()
			}
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Errorf("不限模式下不该出错：%v", err)
		}
	}
	if snapshot := gate.snapshot(); snapshot.Limit != 0 {
		t.Errorf("不限模式的快照 = %+v，期望 Limit 为 0", snapshot)
	}
}

// TestGateReleaseIsIdempotent release 被调用两次不该凭空多放出一个名额，
// 否则闸门上限会被悄悄抬高到失效。
func TestGateReleaseIsIdempotent(t *testing.T) {
	gate := NewGate(1)
	release, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatalf("获取失败：%v", err)
	}
	release()
	release() // 第二次必须什么也不做

	second, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatalf("释放后应当还能取到：%v", err)
	}
	defer second()

	// 名额已被 second 占住；此时再取应当只能排队。
	// 若 release 被重复生效，这里会立刻通过——那就说明它能凭空造名额。
	third, cancelThird := context.WithCancel(t.Context())
	cancelThird()
	if _, err := gate.Acquire(third); err == nil {
		t.Fatal("重复释放后第三个请求竟然也能进来——名额被多放出了一个")
	}
}

// TestGateRejectsWhenQueueCannotProceed 排太久要放弃而不是让用户干等。
// ctx 已取消时 Acquire 必须立刻报错（队列超时是同一条分支）。
func TestGateRejectsWhenQueueCannotProceed(t *testing.T) {
	gate := NewGate(1)
	release, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatalf("获取失败：%v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := gate.Acquire(ctx); err == nil {
		t.Fatal("ctx 已取消时应当报错")
	}
	if snapshot := gate.snapshot(); snapshot.Rejected == 0 {
		t.Error("被拒次数应当累加进快照")
	}
}

// TestGateSnapshot 面板要看的四个数字必须自洽。
func TestGateSnapshot(t *testing.T) {
	gate := NewGate(2)
	release, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatalf("获取失败：%v", err)
	}
	if snapshot := gate.snapshot(); snapshot.Limit != 2 || snapshot.Active != 1 {
		t.Errorf("快照 = %+v，期望 Limit=2 Active=1", snapshot)
	}
	release()
	if snapshot := gate.snapshot(); snapshot.Active != 0 {
		t.Errorf("释放后 Active = %d，期望 0", snapshot.Active)
	}
}

// TestGateCoversBothEntries 两个入口必须抢同一个名额。
//
// 这条回答的是「relay 会不会绕过并发限制」。relay 与本机 API 挂的是同一个
// Handler、同一个 Server、同一个 Gate，所以结构上不可能绕过。
func TestGateCoversBothEntries(t *testing.T) {
	holding := make(chan struct{})
	releaseBackend := make(chan struct{})
	backend := &hangingBackend{holding: holding, release: releaseBackend}

	httpServer := httptest.NewServer(gatedMux(backend, 1))
	defer httpServer.Close()

	go func() { _, _ = postChat(t, httpServer.URL+"/api/v1/chat/completions") }()
	<-holding // 本机入口已占住唯一的名额

	done := make(chan int, 1)
	go func() {
		code, err := postChat(t, httpServer.URL+"/relay/v1/chat/completions")
		if err != nil {
			done <- 0
			return
		}
		done <- code
	}()

	// 关键断言：relay 的请求在闸门外面排着，而不是已经打到后端去了。
	// 光看「响应还没回来」不够——那条 volunteer 也过不了 photography。
	select {
	case code := <-done:
		t.Fatalf("relay 请求立刻返回了 %d——它绕过了并发闸门", code)
	case <-time.After(time.Second):
	}
	if reached := backend.calls(); reached != 1 {
		t.Fatalf("后端被调用了 %d 次，期望只有本机入口那 1 次——"+
			"relay 请求在闸门外面就已经被放过去了", reached)
	}

	close(releaseBackend)
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Errorf("名额释放后 relay 请求应当正常完成，实际 %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("名额释放后 relay 请求仍没完成")
	}
	if reached := backend.calls(); reached != 2 {
		t.Errorf("后端最终被调用了 %d 次，期望 2", reached)
	}
}

// TestBusyResponseCarriesRetryAfter 闸门满了要给客户端一个可执行的建议，
// 而不是只甩一个 503。
func TestBusyResponseCarriesRetryAfter(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeBusy(recorder)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("状态码 = %d，期望 503", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got == "" {
		t.Error("缺少 Retry-After 头")
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("并发已满")) {
		t.Errorf("错误信息没说明原因：%s", recorder.Body.String())
	}
}

// gatedMux 把同一个 Handler 挂到两条路径上，模拟本机 API 与 relay 两个入口。
func gatedMux(backend Backend, limit int) http.Handler {
	logger, err := logging.New("", false)
	if err != nil {
		panic(err)
	}
	server := NewServer(&config.Config{}, nil, backend, nil, logger, nil, limit)
	handler := server.Handler()
	mux := http.NewServeMux()
	mux.Handle("/api/v1/chat/completions", rewriteTo("/v1/chat/completions", handler))
	mux.Handle("/relay/v1/chat/completions", rewriteTo("/v1/chat/completions", handler))
	return mux
}

// rewriteTo 把挂载路径还原成网关认得的路径，
// 这样同一个 Handler 实例可以在两条前缀下都跑起来——生产里就是这么挂的。
func rewriteTo(target string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		clone.URL.Path = target
		next.ServeHTTP(w, clone)
	})
}

// hangingBackend 是一个占住名额不放的后端，直到 release 被关闭才返回。
// 它同时统计「被调用过几次」，用来证明闸门外的请求确实没打到后端。
type hangingBackend struct {
	holding chan struct{}
	release chan struct{}

	mu    sync.Mutex
	count int
}

// calls 返回后端被调用的次数。
func (h *hangingBackend) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func (h *hangingBackend) ChatStream(ctx context.Context, _ []byte) (*http.Response, error) {
	h.mu.Lock()
	h.count++
	h.mu.Unlock()

	select {
	case h.holding <- struct{}{}:
	default:
	}
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return streamingResponse(), nil
}

func (h *hangingBackend) Models(context.Context) ([]upstream.ModelInfo, error) {
	return nil, nil
}

func streamingResponse() *http.Response {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/event-stream")
	recorder.WriteHeader(http.StatusOK)
	_, _ = recorder.WriteString("data: {}\n\n")
	return recorder.Result()
}

func postChat(t *testing.T, url string) (int, error) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    "auto",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("构造请求体失败：%v", err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}
