package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/settings"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/workbuddy"
)

// fakeCheckin 是一个可控的签到源。
//
// 用假的而不是真的 workbuddy.Service：面板这一层要测的是「接口怎么回」，
// 不该连到官方接口上去。真链路的测试在 internal/workbuddy 里。
type fakeCheckin struct {
	status    workbuddy.Status
	statusErr error
	result    workbuddy.Result
	recent    []workbuddy.Record
	calls     int
}

func (f *fakeCheckin) CheckinStatus(context.Context) (workbuddy.Status, error) {
	return f.status, f.statusErr
}

func (f *fakeCheckin) CheckinNow(context.Context) (workbuddy.Result, error) {
	f.calls++
	return f.result, nil
}

func (f *fakeCheckin) CheckinRecent(limit int) []workbuddy.Record {
	return f.recent
}

// newCheckinPanel 造一个注入了签到源的面板。
func newCheckinPanel(t *testing.T, source CheckinSource) *Panel {
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
	console, err := New(Deps{
		Version:       "test",
		StartedAt:     time.Now(),
		ListenAddr:    "127.0.0.1:8788",
		LogPath:       filepath.Join(dir, "gateway.log"),
		JournalPath:   filepath.Join(dir, "events.jsonl"),
		OverridesPath: filepath.Join(dir, "model-overrides.json"),
		SettingsPath:  filepath.Join(dir, "settings.json"),
		Overrides:     mustStore(t, dir),
		Journal:       events,
		Settings:      preferences,
		Logger:        logger,
		Control:       NewControl(),
		Checkin:       source,
	})
	if err != nil {
		t.Fatalf("建面板失败：%v", err)
	}
	return console
}

// 查签到：状态与历史记录都要给。
func TestCheckinGetReturnsStatusAndHistory(t *testing.T) {
	console := newCheckinPanel(t, &fakeCheckin{
		status: workbuddy.Status{Active: true, CheckedInToday: true, StreakDays: 4, DailyCredit: 100},
		recent: []workbuddy.Record{{Date: "2026-10-17", Done: true, Credit: 100, StreakDays: 4}},
	})

	recorder := call(t, console, http.MethodGet, "/api/checkin", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	var payload checkinResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if !payload.Available || payload.Status == nil || payload.Status.StreakDays != 4 {
		t.Fatalf("状态没给全：%+v", payload)
	}
	if len(payload.Recent) != 1 {
		t.Fatalf("历史记录没给：%+v", payload.Recent)
	}
}

// 客户端模式（没有签到源）要明确说「当前模式不支持」，
// 而不是报错或静默返回空——用户要能分辨是模式问题还是坏了。
func TestCheckinGetWithoutSourceExplainsMode(t *testing.T) {
	console := newCheckinPanel(t, nil)

	recorder := call(t, console, http.MethodGet, "/api/checkin", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	var payload checkinResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if payload.Available {
		t.Fatalf("没有签到源时 Available 应当为假：%+v", payload)
	}
	if payload.Error == "" {
		t.Fatalf("应当说明为什么不可用：%+v", payload)
	}
}

// 首页提醒：开关开着 + 活动在期 + 今天没签时才提醒。
//
// 四个条件少一个都应当静默——宁可少提醒一次，
// 也不要拿一个「查不到的状态」或「已经签过的日子」去打扰用户。
func TestCheckinReminder(t *testing.T) {
	cases := []struct {
		name   string
		remind bool
		status workbuddy.Status
		want   bool
	}{
		{"开着且没签：提醒", true, workbuddy.Status{Active: true, CheckedInToday: false, StreakDays: 4}, true},
		{"开关关着：不提醒", false, workbuddy.Status{Active: true, CheckedInToday: false, StreakDays: 4}, false},
		{"今天已签：不提醒", true, workbuddy.Status{Active: true, CheckedInToday: true, StreakDays: 4}, false},
		{"活动不在期：不提醒", true, workbuddy.Status{Active: false, CheckedInToday: false, StreakDays: 4}, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			console := newCheckinPanel(t, &fakeCheckin{status: item.status})
			if err := console.deps.Settings.Update(settings.Settings{
				LogPageSize: settings.DefaultPageSize, LogRetentionDays: settings.DefaultRetentionDays,
				CheckinRemind: item.remind,
			}); err != nil {
				t.Fatalf("设置提醒开关失败：%v", err)
			}

			recorder := call(t, console, http.MethodGet, "/api/checkin", nil, nil)
			var payload checkinResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("解析响应失败：%v", err)
			}
			if got := payload.Reminder != ""; got != item.want {
				t.Fatalf("提醒应当为 %v，得到 %q（%+v）", item.want, payload.Reminder, payload)
			}
		})
	}
}

// 签到失败不是 HTTP 错误，是业务结果：给 200，让前端展示原因。
func TestCheckinRunFailureIsNotHTTPError(t *testing.T) {
	source := &fakeCheckin{result: workbuddy.Result{Done: false, Message: "活动已结束"}}
	console := newCheckinPanel(t, source)

	recorder := call(t, console, http.MethodPost, "/api/checkin", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if payload["ok"] != false {
		t.Fatalf("没签成时 ok 应当为 false：%+v", payload)
	}
	if source.calls != 1 {
		t.Fatalf("应当真的调用了一次签到，实际 %d 次", source.calls)
	}
}

// 签到成功要回 ok=true 与结果详情。
func TestCheckinRunSuccess(t *testing.T) {
	source := &fakeCheckin{result: workbuddy.Result{Done: true, Credit: 100, StreakDays: 5, Message: "签到成功"}}
	console := newCheckinPanel(t, source)

	recorder := call(t, console, http.MethodPost, "/api/checkin", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		OK     bool             `json:"ok"`
		Result workbuddy.Result `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if !payload.OK || payload.Result.StreakDays != 5 {
		t.Fatalf("成功结果不对：%+v", payload)
	}
}
