package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	overrides := mustOverrides(t, `{
		"models": {
			"hy3": {"free_until": "2026-10-31"},
			"hy4-preview": {"free_until": "2026-10-31", "note": "夜间"}
		}
	}`)

	want := map[string]string{
		"hy3":         "Hy3 · 免费 · 限时免费 · 至2026-10-31",
		"glm-5.3":     "GLM-5.3 · 0.79x",
		"hy4-preview": "Hy4 preview · 0.29x · 夜间免费 · 至2026-10-31",
		"no-credits":  "NoCredits",
	}
	entries := Build(models, overrides)
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

// TestAliasReplacesHead 别名只替换展示名的第一段，费用标记照旧拼在后面。
func TestAliasReplacesHead(t *testing.T) {
	overrides := mustOverrides(t, `{
		"models": {"glm-5.3": {"alias": "我的主力模型"}}
	}`)
	entries := Build([]upstream.ModelInfo{{ID: "glm-5.3", Name: "GLM-5.3", Credits: "x0.79"}}, overrides)
	if got, want := entries[0].DisplayName(), "我的主力模型 · 0.79x"; got != want {
		t.Errorf("展示名 = %q，期望 %q", got, want)
	}
	// 原名仍要留着：面板与排障都要看得到后端给的 id / 名字。
	if entries[0].Model.Name != "GLM-5.3" {
		t.Errorf("原始名不该被别名覆盖掉：%q", entries[0].Model.Name)
	}
}

// TestVisibleFiltersHidden Build 不过滤隐藏项（编辑页要看得见），
// Visible 才过滤——把两件事分开，否则用户没法把隐藏的模型放出来。
func TestVisibleFiltersHidden(t *testing.T) {
	overrides := mustOverrides(t, `{
		"models": {"hy3": {"hidden": true}}
	}`)
	entries := Build([]upstream.ModelInfo{
		{ID: "hy3", Name: "Hy3"},
		{ID: "glm-5.3", Name: "GLM-5.3"},
	}, overrides)
	if len(entries) != 2 {
		t.Fatalf("Build 不该过滤隐藏项，条目数 = %d", len(entries))
	}
	visible := Visible(entries)
	if len(visible) != 1 || visible[0].Model.ID != "glm-5.3" {
		t.Fatalf("Visible 的结果不对：%+v", visible)
	}
}

// TestOverridesValidate 档位键写错必须报错。
// 一个拼错的键不会让任何东西失败，只会静默失效——
// 用户在 pi 里选了 high、实际按默认档位跑，最难查。
func TestOverridesValidate(t *testing.T) {
	good := mustOverrides(t, `{"models": {"hy3": {"thinking_level_map": {"high": "high", "medium": null}}}}`)
	if err := good.Validate(); err != nil {
		t.Errorf("合法档位不该报错：%v", err)
	}
	bad := mustOverrides(t, `{"models": {"hy3": {"thinking_level_map": {"hgih": "high"}}}}`)
	if err := bad.Validate(); err == nil {
		t.Fatal("拼错的档位键应当报错")
	}
}

// TestTouchStampsOnlyChangedEntries Touch 只给真的变了的条目打时间戳，
// 没变的条目要保留原来的作者——否则三方合并时会把「谁改的」冲掉。
func TestTouchStampsOnlyChangedEntries(t *testing.T) {
	previous := mustOverrides(t, `{
		"revision": 3,
		"models": {
			"hy3": {"alias": "旧", "updated_at": "2026-01-01T00:00:00Z", "updated_by": "机器A"},
			"glm-5.3": {"alias": "没动", "updated_at": "2026-01-02T00:00:00Z", "updated_by": "机器A"}
		}
	}`)
	next := mustOverrides(t, `{
		"models": {
			"hy3": {"alias": "新"},
			"glm-5.3": {"alias": "没动"}
		}
	}`)
	Touch(previous, next, "机器B", time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC))

	if next.Revision != 4 {
		t.Errorf("revision = %d，期望 4", next.Revision)
	}
	changed := next.Models["hy3"]
	if changed.UpdatedBy != "机器B" || !strings.HasPrefix(changed.UpdatedAt, "2026-10-16") {
		t.Errorf("改过的条目没打上新作者：%+v", changed)
	}
	untouched := next.Models["glm-5.3"]
	if untouched.UpdatedBy != "机器A" || untouched.UpdatedAt != "2026-01-02T00:00:00Z" {
		t.Errorf("没变的条目不该被重写时间戳：%+v", untouched)
	}
}

// TestLoadOverridesMissingFile 是正常状态：没有本地覆盖也能跑。
func TestLoadOverridesMissingFile(t *testing.T) {
	table, err := LoadOverrides(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("文件不存在不该报错：%v", err)
	}
	if _, ok := table.Lookup("hy3"); ok {
		t.Error("空表不该查出任何条目")
	}
	if entries := Build([]upstream.ModelInfo{{ID: "hy3", Name: "Hy3"}}, table); entries[0].DisplayName() != "Hy3" {
		t.Errorf("没有覆盖时展示名应保持原样，实际 %q", entries[0].DisplayName())
	}
}

// TestLoadOverridesRejectsMalformed 配置写坏了要明确报错，而不是静默忽略。
func TestLoadOverridesRejectsMalformed(t *testing.T) {
	if _, err := LoadOverrides(writeOverrides(t, `{not json`)); err == nil {
		t.Fatal("坏 JSON 应当报错")
	}
}

// TestSaveAndLoadOverrides 落盘再读回来要一模一样。
func TestSaveAndLoadOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model-overrides.json")
	table := NewOverrides()
	table.Models["hy3"] = Override{Alias: "Hy3 别名", FreeUntil: "2026-10-31"}
	table.Revision = 7
	if err := SaveOverrides(path, table); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	loaded, err := LoadOverrides(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if loaded.Revision != 7 || loaded.Models["hy3"].Alias != "Hy3 别名" {
		t.Errorf("读回来的内容不对：%+v", loaded)
	}
}

// TestMigratePromotions 旧文件升级：内容要搬过来，源文件要改名，
// 而且第二次运行不能再迁移一遍。
func TestMigratePromotions(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "promotions.json")
	target := filepath.Join(dir, "model-overrides.json")
	if err := os.WriteFile(legacy, []byte(`{"hy3": {"free_until": "2026-10-31", "note": "赠送"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := MigratePromotions(legacy, target)
	if err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	if !migrated {
		t.Fatal("应当报告已迁移")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("源文件应当已改名")
	}
	if _, err := os.Stat(legacy + ".migrated"); err != nil {
		t.Errorf("改名后的文件不见了：%v", err)
	}
	table, err := LoadOverrides(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := table.Models["hy3"]; got.FreeUntil != "2026-10-31" || got.Note != "赠送" {
		t.Errorf("迁移过来的内容不对：%+v", got)
	}
	// 幂等：第二次不该再动。
	if again, err := MigratePromotions(legacy, target); err != nil || again {
		t.Errorf("第二次迁移应当直接跳过：migrated=%v err=%v", again, err)
	}
}

// TestMigratePromotionsKeepsSourceOnFailure 坏文件不该被改名——
// 「宁可不迁移，也不丢用户写的东西」。
func TestMigratePromotionsKeepsSourceOnFailure(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "promotions.json")
	target := filepath.Join(dir, "model-overrides.json")
	if err := os.WriteFile(legacy, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigratePromotions(legacy, target); err == nil {
		t.Fatal("坏文件应当报错")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("失败时源文件必须原样保留：%v", err)
	}
}

// mustOverrides 解析一段覆盖表 JSON，加载失败直接判定用例失败。
func mustOverrides(t *testing.T, content string) *Overrides {
	t.Helper()
	path := writeOverrides(t, content)
	table, err := LoadOverrides(path)
	if err != nil {
		t.Fatalf("解析覆盖表失败：%v", err)
	}
	return table
}

// writeOverrides 把内容写进临时文件并返回路径。
func writeOverrides(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model-overrides.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时配置失败：%v", err)
	}
	return path
}
