package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/overridessync"
)

// fakeSyncer 记录同步器被调了几次，并返回预设的状态。
type fakeSyncer struct {
	status     overridessync.Status
	conflicts  []overridessync.Conflict
	synced     int
	lastPrefer overridessync.Prefer
}

func (f *fakeSyncer) Status() overridessync.Status { return f.status }

func (f *fakeSyncer) Sync(_ context.Context, prefer overridessync.Prefer) (overridessync.Status, error) {
	f.synced++
	f.lastPrefer = prefer
	status := f.status
	// 用户选了一边之后就不再报冲突，与真实同步器的行为一致。
	if prefer == overridessync.PreferNone {
		status.Conflicts = f.conflicts
	}
	return status, nil
}

// TestOverridesSyncUnavailableOnGateway 网关模式没有可同步的对象，要明确说清。
func TestOverridesSyncUnavailableOnGateway(t *testing.T) {
	console, _, _ := newTestPanel(t)

	for _, target := range []string{"/api/overrides/sync"} {
		recorder := call(t, console, http.MethodGet, target, nil, nil)
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s 状态码 = %d，期望 501", target, recorder.Code)
		}
	}
}

// TestOverridesSyncStatusAndRun 客户端模式：状态不联网，同步把冲突如实带回。
func TestOverridesSyncStatusAndRun(t *testing.T) {
	console, _, _ := newTestPanel(t)
	syncer := &fakeSyncer{status: overridessync.Status{LocalRevision: 7, BaseRevision: 5, Dirty: 2}}
	console.deps.OverridesSyncer = syncer

	recorder := call(t, console, http.MethodGet, "/api/overrides/sync", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	var status overridessync.Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if status.Dirty != 2 || status.LocalRevision != 7 {
		t.Errorf("状态不对：%+v", status)
	}
	if syncer.synced != 0 {
		t.Error("查状态不该触发一次同步")
	}

	// 有冲突时返回 200 + 冲突清单：冲突是正常结果，不是失败。
	syncer.conflicts = []overridessync.Conflict{{Model: "glm-5.3"}}
	recorder = call(t, console, http.MethodPost, "/api/overrides/sync", map[string]any{}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("同步状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	if syncer.synced != 1 {
		t.Errorf("同步次数 = %d，期望 1", syncer.synced)
	}
	if !strings.Contains(recorder.Body.String(), "glm-5.3") {
		t.Errorf("冲突没带回来：%s", recorder.Body.String())
	}

	// 用户选了「以本机为准」：冲突应当被消解，且选择传到同步器。
	recorder = call(t, console, http.MethodPost, "/api/overrides/sync",
		map[string]any{"prefer": "local"}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("选边后状态码 = %d：%s", recorder.Code, recorder.Body.String())
	}
	if syncer.lastPrefer != overridessync.PreferLocal {
		t.Errorf("选择没传下去：%q", syncer.lastPrefer)
	}
	if strings.Contains(recorder.Body.String(), "glm-5.3") {
		t.Errorf("选边之后不该再报冲突：%s", recorder.Body.String())
	}

	// 乱填的一律拒绝。
	if recorder = call(t, console, http.MethodPost, "/api/overrides/sync",
		map[string]any{"prefer": "whatever"}, nil); recorder.Code != http.StatusBadRequest {
		t.Errorf("非法选择应当 400，实际 %d", recorder.Code)
	}
}

// TestOverridesStatusWithTypedNilSyncer 网关模式下传进来的是一个
// 「类型化的 nil」同步器（`(*overridessync.Syncer)(nil)`），不是接口的 nil。
//
// 这是一个真实发生过的 panic：判等判不出来，`Status()` 解引用 nil 指针，
// 于是**每次打开模型页**都在 HTTP 处理协程里 panic，返回空响应。
// 所以这里必须显式用类型化的 nil 来测，用接口的 nil 测不到它。
func TestOverridesStatusWithTypedNilSyncer(t *testing.T) {
	console, _ := newLivePanel(t)
	var syncer *overridessync.Syncer
	console.deps.OverridesSyncer = syncer
	if console.deps.OverridesSyncer == nil {
		t.Fatal("接口不该判成 nil——正是这个现象让当初的守卫失效")
	}

	for _, path := range []string{"/api/overrides/sync"} {
		recorder := call(t, console, http.MethodGet, path, nil, nil)
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("%s 状态码 = %d，想要 501：%s", path, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "权威") {
			t.Errorf("%s 没说清为什么不能同步：%s", path, recorder.Body.String())
		}
	}

	// 推送那条路径同样要挡住，否则点「同步」一样 panic。
	recorder := call(t, console, http.MethodPost, "/api/overrides/sync",
		strings.NewReader(`{}`), map[string]string{"Content-Type": "application/json"})
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("推送状态码 = %d，想要 501：%s", recorder.Code, recorder.Body.String())
	}
}
