package relay

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
)

// newOverrideStore 建一个落在临时目录里的真实 Store——
// 这里刻意不用替身：乐观并发的行为正是要测的那件事。
func newOverrideStore(t *testing.T) *catalog.Store {
	t.Helper()
	store, err := catalog.OpenStore(filepath.Join(t.TempDir(), "model-overrides.json"))
	if err != nil {
		t.Fatalf("建 Store 失败：%v", err)
	}
	return store
}

func newOverrideClient(t *testing.T, serverURL, fingerprint string) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BaseURL:     serverURL,
		PSK:         "topsecret",
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "笔记本B"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	return client
}

// TestOverridesRoundTrip 客户端拉取、推送、再拉取，内容要对得上；
// 推送者的设备身份要写进 updated_by。
func TestOverridesRoundTrip(t *testing.T) {
	store := newOverrideStore(t)
	_, fingerprint, server := newTestServer(t, Deps{
		PSK: "topsecret", Logger: discardLogger(), Overrides: store,
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
	})
	client := newOverrideClient(t, server.URL, fingerprint)

	// 一开始是空表，revision 0。
	initial, err := client.FetchModelOverrides(t.Context())
	if err != nil {
		t.Fatalf("拉取失败：%v", err)
	}
	if initial.Revision != 0 || len(initial.Models) != 0 {
		t.Fatalf("初始状态不对：%+v", initial)
	}

	saved, err := client.PushModelOverrides(t.Context(), 0, map[string]catalog.Override{
		"glm-5.3": {Alias: "主力"},
	})
	if err != nil {
		t.Fatalf("推送失败：%v", err)
	}
	if saved.Revision != 1 {
		t.Errorf("revision = %d，期望 1", saved.Revision)
	}
	if entry := saved.Models["glm-5.3"]; entry.Alias != "主力" || entry.UpdatedBy != "笔记本B" {
		t.Errorf("条目不对：%+v", entry)
	}

	// 再拉一次，网关那侧真的落盘了。
	again, err := client.FetchModelOverrides(t.Context())
	if err != nil {
		t.Fatalf("再次拉取失败：%v", err)
	}
	if again.Revision != 1 || again.Models["glm-5.3"].Alias != "主力" {
		t.Errorf("回读不对：%+v", again)
	}
}

// TestOverridesPushRejectsStaleRevision 基准不对时必须拒绝，
// 而且要能被调用方识别成「请重新合并」而不是网络故障。
func TestOverridesPushRejectsStaleRevision(t *testing.T) {
	store := newOverrideStore(t)
	_, fingerprint, server := newTestServer(t, Deps{
		PSK: "topsecret", Logger: discardLogger(), Overrides: store,
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
	})
	client := newOverrideClient(t, server.URL, fingerprint)

	if _, err := client.PushModelOverrides(t.Context(), 0, map[string]catalog.Override{
		"a": {Alias: "第一次"},
	}); err != nil {
		t.Fatalf("第一次推送失败：%v", err)
	}
	// 拿着过期的基准（0）再推一次。
	_, err := client.PushModelOverrides(t.Context(), 0, map[string]catalog.Override{
		"a": {Alias: "覆盖"},
	})
	if !errors.Is(err, catalog.ErrStaleRevision) {
		t.Fatalf("应当报基准过期，实际：%v", err)
	}
	// 被拒绝之后内容不能被改动。
	if got := store.Snapshot().Models["a"].Alias; got != "第一次" {
		t.Errorf("被拒绝的推送却改动了内容：%q", got)
	}
}

// TestOverridesPushValidatesLevelKeys 拼错的思考档位要被挡在门外。
func TestOverridesPushValidatesLevelKeys(t *testing.T) {
	store := newOverrideStore(t)
	_, fingerprint, server := newTestServer(t, Deps{
		PSK: "topsecret", Logger: discardLogger(), Overrides: store,
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
	})
	client := newOverrideClient(t, server.URL, fingerprint)

	level := "high"
	_, err := client.PushModelOverrides(context.Background(), 0, map[string]catalog.Override{
		"a": {ThinkingLevelMap: map[string]*string{"hgih": &level}},
	})
	if err == nil {
		t.Fatal("拼错的档位键应当被拒绝")
	}
}

// TestOverridesEndpointAbsentInClientMode 没有 Store 时端点明确说不行。
func TestOverridesEndpointAbsentInClientMode(t *testing.T) {
	_, fingerprint, server := newTestServer(t, Deps{
		PSK: "topsecret", Logger: discardLogger(),
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
	})
	client := newOverrideClient(t, server.URL, fingerprint)

	if _, err := client.FetchModelOverrides(context.Background()); err == nil {
		t.Fatal("没有覆盖表时拉取应当失败")
	} else if !strings.Contains(err.Error(), "没有可同步") {
		t.Errorf("错误信息没说清原因：%v", err)
	}
}

// fakeBilling 返回一份固定的余额，用来验证透传链路。
type fakeBilling struct{ fetched int }

func (f *fakeBilling) Fetch(context.Context) (billing.Snapshot, error) {
	f.fetched++
	cycleEnd := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return billing.Snapshot{
		FetchedAt: time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC),
		Packages: []billing.Package{
			{Name: "CodeBuddy个人体验版", Remain: 492.94, Size: 500, Use: 7.06,
				TotalRemain: 500, CycleEndTime: cycleEnd},
		},
	}, nil
}

// TestBillingPassthrough 客户端要能经 relay 拿到网关那侧的余额。
//
// 以前客户端控制台只会回一句「请到网关的控制台查看」，用户得在两台机器
// 之间来回跑。这里验证它真的透传了，且形状是网关算好的那一份。
func TestBillingPassthrough(t *testing.T) {
	billingSource := &fakeBilling{}
	_, fingerprint, server := newTestServer(t, Deps{
		PSK: "topsecret", Logger: discardLogger(), Billing: billingSource,
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
	})
	client := newOverrideClient(t, server.URL, fingerprint)

	report, err := client.FetchBillingReport(t.Context())
	if err != nil {
		t.Fatalf("拉取余额失败：%v", err)
	}
	if billingSource.fetched != 1 {
		t.Errorf("网关侧的账单接口被调用 %d 次，期望 1", billingSource.fetched)
	}
	if report.TotalRemain != 492.94 {
		t.Errorf("总剩余 = %v，期望 492.94", report.TotalRemain)
	}
	if len(report.Packages) != 1 || report.Packages[0].Name != "CodeBuddy个人体验版" {
		t.Fatalf("资源包不对：%+v", report.Packages)
	}
	if report.Packages[0].CycleEnd == nil {
		t.Error("周期结束时间应当带上")
	}
	// 结算延迟的提示由服务端给，免得前端再写一遍。
	if report.SettleHint == "" {
		t.Error("结算提示是空的")
	}
	// 来源必须如实标注：这是网关读的，不是客户端读的。
	if report.Source != "gateway" {
		t.Errorf("来源 = %q，想要 gateway", report.Source)
	}
}

// TestBillingAbsentReturnsClearError 没有余额来源时要说清是「没有」而不是网络故障。
func TestBillingAbsentReturnsClearError(t *testing.T) {
	_, fingerprint, server := newTestServer(t, Deps{
		PSK: "topsecret", Logger: discardLogger(),
		Backend: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
	})
	client := newOverrideClient(t, server.URL, fingerprint)

	_, err := client.FetchBillingReport(context.Background())
	if err == nil {
		t.Fatal("没有余额来源时应当失败")
	}
	if !strings.Contains(err.Error(), "没有可读取的余额") {
		t.Errorf("错误信息没说清原因：%v", err)
	}
}
