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
	if got[0].ResetAt != future {
		t.Errorf("恢复时间应取最近一条 %q，得到 %q", future, got[0].ResetAt)
	}
}

// TestCollectActiveLimitsEmpty 没有任何限流时返回空，不返回 nil 以外的怪东西。
func TestCollectActiveLimitsEmpty(t *testing.T) {
	if got := collectActiveLimits(nil, time.Now()); got != nil {
		t.Errorf("无记录应返回 nil，得到 %+v", got)
	}
}
