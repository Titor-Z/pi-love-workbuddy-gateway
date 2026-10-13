package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// TestCreditsValue 覆盖后端倍率原文的各种写法。
// 后端有时带 "credits" 后缀有时不带，解析必须都能吃下。
func TestCreditsValue(t *testing.T) {
	cases := []struct {
		raw      string
		want     float64
		wantFine bool
	}{
		{"x0.00 credits", 0, true},
		{"x0.29", 0.29, true},
		{"x2.20 credits", 2.2, true},
		{"x1", 1, true},
		{" x0.06 credits ", 0.06, true},
		{"", 0, false},
		{"credits", 0, false},
		{"x", 0, false},
		{"免费", 0, false},
	}
	for _, testCase := range cases {
		value, ok := creditsValue(testCase.raw)
		if ok != testCase.wantFine {
			t.Errorf("%q：解析成功标志 = %v，期望 %v", testCase.raw, ok, testCase.wantFine)
			continue
		}
		if ok && value != testCase.want {
			t.Errorf("%q：值 = %v，期望 %v", testCase.raw, value, testCase.want)
		}
	}
}

// TestFormatCredits 保证展示用的倍率字符串稳定，不受浮点尾数影响。
func TestFormatCredits(t *testing.T) {
	cases := map[float64]string{
		0.29: "0.29x",
		2.2:  "2.2x",
		1:    "1x",
		0.06: "0.06x",
		// 浮点乘除常见的尾数噪声要被抹平。
		0.1 + 0.2: "0.3x",
	}
	for value, want := range cases {
		if got := formatCredits(value); got != want {
			t.Errorf("formatCredits(%v) = %q，期望 %q", value, got, want)
		}
	}
}

// TestBadgeOf 覆盖促销徽章在 tags 里的各种形态。
func TestBadgeOf(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"标准徽章", []string{"craft", "badge:限时免费:#FF0000"}, "限时免费"},
		{"没有徽章", []string{"craft"}, ""},
		{"空 tags", nil, ""},
		{"徽章缺颜色", []string{"badge:独家限免"}, "独家限免"},
		{"徽章文案为空", []string{"badge::#FF0000"}, ""},
		{"多个徽章取第一个", []string{"badge:甲:#1", "badge:乙:#2"}, "甲"},
	}
	for _, testCase := range cases {
		if got := badgeOf(testCase.tags); got != testCase.want {
			t.Errorf("%s：badgeOf = %q，期望 %q", testCase.name, got, testCase.want)
		}
	}
}

// TestBuildAndDisplayName 是费用标记的主路径测试：
// 免费、收费、促销、截止日期四种信息如何拼进展示名。
func TestBuildAndDisplayName(t *testing.T) {
	models := []upstream.ModelInfo{
		{ID: "hy3", Name: "Hy3", Credits: "x0.00 credits", Tags: []string{"badge:限时免费:#FF0000"}},
		{ID: "glm-5.3", Name: "GLM-5.3", Credits: "x0.79"},
		{ID: "hy4-preview", Name: "Hy4 preview", Credits: "x0.29", Tags: []string{"badge:夜间免费:#FF0000"}},
		{ID: "no-credits", Name: "NoCredits"},
	}
	promotions, err := LoadPromotions(writePromotions(t, `{
		"hy3": {"free_until": "2026-10-31"},
		"hy4-preview": {"free_until": "2026-10-31", "note": "夜间"}
	}`))
	if err != nil {
		t.Fatalf("加载促销配置失败：%v", err)
	}

	want := map[string]string{
		"hy3":         "Hy3 · 免费 · 限时免费 · 至2026-10-31",
		"glm-5.3":     "GLM-5.3 · 0.79x",
		"hy4-preview": "Hy4 preview · 0.29x · 夜间免费 · 至2026-10-31",
		"no-credits":  "NoCredits",
	}
	entries := Build(models, promotions)
	if len(entries) != len(models) {
		t.Fatalf("条目数 = %d，期望 %d", len(entries), len(models))
	}
	for _, entry := range entries {
		if got := entry.DisplayName(); got != want[entry.Model.ID] {
			t.Errorf("%s：展示名 = %q，期望 %q", entry.Model.ID, got, want[entry.Model.ID])
		}
	}

	// 免费模型不该再显示倍率，否则会出现「免费 · 0x」这种重复信息。
	if entries[0].Label.Credits != "" {
		t.Errorf("免费模型的倍率应为空，实际 %q", entries[0].Label.Credits)
	}
	if !entries[0].Label.Free {
		t.Error("hy3 的倍率是 x0.00，应当判定为免费")
	}
	// 倍率缺失时不能误判成免费。
	if entries[3].Label.Free {
		t.Error("倍率解析失败时不应判定为免费")
	}
}

// TestLoadPromotionsMissingFile 是正常状态：没有本地配置也能跑。
func TestLoadPromotionsMissingFile(t *testing.T) {
	table, err := LoadPromotions(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("文件不存在不该报错：%v", err)
	}
	if _, ok := table.Lookup("hy3"); ok {
		t.Error("空配置不该查出任何条目")
	}
	if entries := Build([]upstream.ModelInfo{{ID: "hy3", Name: "Hy3"}}, table); entries[0].DisplayName() != "Hy3" {
		t.Errorf("没有配置时展示名应保持原样，实际 %q", entries[0].DisplayName())
	}
}

// TestLoadPromotionsRejectsMalformed 配置写坏了要明确报错，而不是静默忽略。
func TestLoadPromotionsRejectsMalformed(t *testing.T) {
	if _, err := LoadPromotions(writePromotions(t, `{not json`)); err == nil {
		t.Fatal("坏 JSON 应当报错")
	}
}

// TestDefaultPromotionsPathFollowsXDG 钉住配置目录的解析规则。
// macOS 上尤其重要：不能落到 ~/Library/Application Support，
// 否则三平台路径不一致。
func TestDefaultPromotionsPathFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test-root")
	if got, want := DefaultConfigDir(), "/tmp/xdg-test-root/"+configDirName; got != want {
		t.Errorf("DefaultConfigDir = %q，期望 %q", got, want)
	}
	if got, want := DefaultPromotionsPath(), "/tmp/xdg-test-root/"+configDirName+"/promotions.json"; got != want {
		t.Errorf("DefaultPromotionsPath = %q，期望 %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("拿不到用户主目录，跳过")
	}
	if got, want := DefaultConfigDir(), filepath.Join(home, ".config", configDirName); got != want {
		t.Errorf("未设 XDG_CONFIG_HOME 时 = %q，期望 %q", got, want)
	}
}

// writePromotions 把内容写进临时文件并返回路径。
func writePromotions(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "promotions.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时配置失败：%v", err)
	}
	return path
}
