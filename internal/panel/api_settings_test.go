package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/settings"
)

// TestSettingsGetReturnsDefaults 设置页要拿到当前值与允许区间，
// 区间由后端下发，免得前端自己写一份、两边慢慢漂移。
func TestSettingsGetReturnsDefaults(t *testing.T) {
	console, _, _ := newTestPanel(t)

	recorder := call(t, console, http.MethodGet, "/api/settings", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	var payload settingsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if payload.Settings != settings.Default() {
		t.Errorf("默认值不对：%+v", payload.Settings)
	}
	if payload.MinPageSize != settings.MinPageSize || payload.MaxRetentionDays != settings.MaxRetentionDays {
		t.Errorf("允许区间没给全：%+v", payload)
	}
	if payload.Path == "" {
		t.Error("偏好文件路径不该为空——设置页要把它显示给用户")
	}
}

// TestSettingsPutRejectsOutOfRange 填错要给 400 与原因，而不是 502 或悄悄改掉。
func TestSettingsPutRejectsOutOfRange(t *testing.T) {
	console, _, _ := newTestPanel(t)

	recorder := call(t, console, http.MethodPut, "/api/settings",
		map[string]any{"log_page_size": 0, "log_retention_days": 7}, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，想要 400：%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); !strings.Contains(got, "每页条数") {
		t.Errorf("错误信息没说清哪一项错了：%s", got)
	}

	// 被拒绝的提交不能改动已生效的值。
	recorder = call(t, console, http.MethodGet, "/api/settings", nil, nil)
	var payload settingsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if payload.Settings != settings.Default() {
		t.Errorf("被拒绝后值被改了：%+v", payload.Settings)
	}
}

// TestSettingsPutPersists 保存后要落盘，重开一份 store 能读到。
func TestSettingsPutPersists(t *testing.T) {
	console, _, _ := newTestPanel(t)

	next := settings.Settings{LogPageSize: 25, LogRetentionDays: 30}
	recorder := call(t, console, http.MethodPut, "/api/settings", next, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	var payload settingsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	// 没填的签到时刻会被补成默认值后再落盘，所以拿到的不是原样传进去的那份。
	expected := next
	expected.CheckinTime = settings.DefaultCheckinTime
	if payload.Settings != expected {
		t.Errorf("返回值不是刚保存的：%+v", payload.Settings)
	}

	reopened, err := settings.Open(payload.Path)
	if err != nil {
		t.Fatalf("重开偏好失败：%v", err)
	}
	if reopened.Get() != expected {
		t.Errorf("落盘的不是刚保存的值：%+v", reopened.Get())
	}
}

// TestSettingsPutPrunesImmediately 把保留期改小之后旧日志要立刻消失，
// 而不是等下一次六小时检查——用户改这一项就是想现在清掉。
func TestSettingsPutPrunesImmediately(t *testing.T) {
	console, _, events := newTestPanel(t)

	// 铺一个早就过期的分片。
	oldDay := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	oldPath := filepath.Join(events.Dir(), "gateway-"+oldDay+".jsonl")
	if err := os.WriteFile(oldPath, []byte(`{"kind":"chat","msg":"很久以前"}`+"\n"), 0o600); err != nil {
		t.Fatalf("写旧分片失败：%v", err)
	}
	// 再留一个最新的分片，它不该被删。
	freshPath := filepath.Join(events.Dir(), "gateway-"+time.Now().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(freshPath, []byte(`{"kind":"chat","msg":"今天"}`+"\n"), 0o600); err != nil {
		t.Fatalf("写新分片失败：%v", err)
	}

	recorder := call(t, console, http.MethodPut, "/api/settings",
		settings.Settings{LogPageSize: 10, LogRetentionDays: 1}, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("过期分片没有被删掉：%v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("最新分片被误删了：%v", err)
	}
}

// TestEventsEndpointRespectsRetention 事件接口只读保留期内的分片：
// 更早的已被删，限定范围也让读取量不随历史增长。
func TestEventsEndpointRespectsRetention(t *testing.T) {
	console, _, events := newTestPanel(t)

	// 两条记录：一条很久以前（超出默认 7 天），一条刚才。
	lines := `{"t":"` + time.Now().AddDate(0, 0, -30).Format(time.RFC3339) +
		`","kind":"chat","msg":"很久以前"}` + "\n" +
		`{"t":"` + time.Now().Format(time.RFC3339) + `","kind":"chat","msg":"刚才"}` + "\n"
	path := filepath.Join(events.Dir(), "gateway-"+time.Now().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatalf("写测试分片失败：%v", err)
	}

	recorder := call(t, console, http.MethodGet, "/api/events?limit=100", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，想要 200：%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Events []journal.Record `json:"events"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	if len(payload.Events) != 1 {
		t.Fatalf("条数 = %d，期望 1（超期的那条不该出现）：%+v", len(payload.Events), payload.Events)
	}
	if payload.Events[0].Message != "刚才" {
		t.Errorf("留下的不是最新那条：%q", payload.Events[0].Message)
	}
}
