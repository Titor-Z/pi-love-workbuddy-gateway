package panel

import (
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
)

// TestCollectActiveLimits 只收「仍然生效」的限流：解析不出时间的一律排除，
// 已经过了恢复时间的也要排除——宁可少显示，也不要告诉用户一个错的恢复时间。
func TestCollectActiveLimits(t *testing.T) {
	now := time.Date(2026, 10, 14, 21, 0, 0, 0, time.Local)
	future := now.Add(2 * time.Hour).Format("2006-01-02 15:04:05 MST")
	past := now.Add(-1 * time.Hour).Format("2006-01-02 15:04:05 MST")

	records := []journal.Record{
		{Kind: journal.KindChat, Model: "hy3", RateLimited: true,
			RateResetAt: future, Time: now.Add(-10 * time.Minute)},
		// 同一个模型的旧记录应被去重，只保留最近一条
		{Kind: journal.KindChat, Model: "hy3", RateLimited: true,
			RateResetAt: past, Time: now.Add(-2 * time.Hour)},
		// 已过恢复时间，不该出现
		{Kind: journal.KindChat, Model: "glm-5.3", RateLimited: true,
			RateResetAt: past, Time: now.Add(-30 * time.Minute)},
		// 解析不出恢复时间，宁可不显示
		{Kind: journal.KindChat, Model: "deepseek-v4-flash", RateLimited: true,
			RateResetAt: "", Time: now.Add(-5 * time.Minute)},
	}

	got := collectActiveLimits(records, now)
	if len(got) != 1 {
		t.Fatalf("应只有 1 个仍然生效的限流，得到 %d：%+v", len(got), got)
	}
	if got[0].Model != "hy3" {
		t.Errorf("模型应为 hy3，得到 %q", got[0].Model)
	}
	// 前端要能直接比较大小，所以下发的是 RFC3339，不是后端原文。
	if want := now.Add(2 * time.Hour).Format(time.RFC3339); got[0].ResetAt != want {
		t.Errorf("恢复时间应为 RFC3339 的 %q，得到 %q", want, got[0].ResetAt)
	}
}

// TestCollectActiveLimitsParsesBackendOffset 是那个让限流卡片
// 永远显示「正在频率限制中」的 bug 的回归测试。
//
// 后端给的恢复时间是 "2026-10-14 23:13:09 UTC+8"——「本地时间 + 时区缩写 +
// 数字偏移」。time.Parse 的 MST 只认 "UTC"，认不出紧跟的 "+8"，会返回
// "extra text"。解析失败时旧代码把记录当成「仍然生效」，于是恢复时间
// 早就过去了的限流也一直挂在概览页上。
func TestCollectActiveLimitsParsesBackendOffset(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)

	// 已过期的：必须被剔除。
	expired := "2026-10-14 23:13:09 UTC+8"
	if got := collectActiveLimits([]journal.Record{
		{Kind: journal.KindChat, Model: "hy3", RateLimited: true,
			RateResetAt: expired, Time: now.Add(-40 * time.Hour)},
	}, now); len(got) != 0 {
		t.Errorf("恢复时间已过的限流不该出现，得到 %+v", got)
	}

	// 仍然生效的：解析得出、且时间正确。
	active := now.Add(2 * time.Hour).Format("2006-01-02 15:04:05") + " UTC+8"
	got := collectActiveLimits([]journal.Record{
		{Kind: journal.KindChat, Model: "hy3", RateLimited: true,
			RateResetAt: active, Time: now.Add(-5 * time.Minute)},
	}, now)
	if len(got) != 1 {
		t.Fatalf("仍在生效的限流应被收下，得到 %+v", got)
	}
	if want := now.Add(2 * time.Hour).Format(time.RFC3339); got[0].ResetAt != want {
		t.Errorf("恢复时间应为 %q，得到 %q", want, got[0].ResetAt)
	}
}

// TestCollectActiveLimitsEmpty 没有任何限流时返回空，不返回 nil 以外的怪东西。
func TestCollectActiveLimitsEmpty(t *testing.T) {
	if got := collectActiveLimits(nil, time.Now()); got != nil {
		t.Errorf("无记录应返回 nil，得到 %+v", got)
	}
}
