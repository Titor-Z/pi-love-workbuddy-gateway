package credential

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// touch 在指定目录里造一个文件，并指定修改时间。
func touch(t *testing.T, dir, name string, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("写入 %s 失败：%v", path, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("设置 %s 的时间失败：%v", path, err)
	}
	return path
}

// TestLocateUsesExplicitPath 显式指定的路径优先，且必须是真实存在的文件。
func TestLocateUsesExplicitPath(t *testing.T) {
	dir := t.TempDir()
	explicit := touch(t, dir, "custom.info", time.Now())

	got, err := Locate(explicit)
	if err != nil {
		t.Fatalf("应当采用显式路径：%v", err)
	}
	if got != explicit {
		t.Fatalf("got %q，期望 %q", got, explicit)
	}

	if _, err := Locate(filepath.Join(dir, "missing.info")); err == nil {
		t.Fatal("显式路径不存在时应当报错")
	}
}

// TestLocatePrefersCurrentFile 目录里同时有当前文件和历史文件时，
// 应当选无时间戳后缀的那个，而不是最新写入的。
func TestLocatePrefersCurrentFile(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	touch(t, dir, "workbuddy-desktop-20260101.info", time.Now())
	current := touch(t, dir, currentAuthFileName, old)

	t.Setenv(envAuthDir, dir)
	got, err := Locate("")
	if err != nil {
		t.Fatalf("定位失败：%v", err)
	}
	if got != current {
		t.Fatalf("got %q，期望当前文件 %q", got, current)
	}
}

// TestLocateFallsBackToNewest 没有当前文件时取修改时间最新的那个。
// 登录态文件在刷新时会换名字，这个兜底必须有。
func TestLocateFallsBackToNewest(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "workbuddy-desktop-20260101.info", time.Now().Add(-2*time.Hour))
	newest := touch(t, dir, "workbuddy-desktop-20260202.info", time.Now().Add(-time.Minute))

	t.Setenv(envAuthDir, dir)
	got, err := Locate("")
	if err != nil {
		t.Fatalf("定位失败：%v", err)
	}
	if got != newest {
		t.Fatalf("got %q，期望最新的 %q", got, newest)
	}
}

// TestLocateIgnoresNonInfoFiles 只认 *.info，避免误取同目录里的其它文件。
func TestLocateIgnoresNonInfoFiles(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "notes.txt", time.Now())

	t.Setenv(envAuthDir, dir)
	_, err := Locate("")
	if err == nil {
		t.Fatal("目录里没有 *.info 时应当报错")
	}
	// 报错要能指向解决办法，否则用户只能猜。
	if !strings.Contains(err.Error(), envAuthDir) {
		t.Errorf("错误信息应提示可用的环境变量，实际：%v", err)
	}
}

// TestLocateReportsMissingDirectory 目录不存在时也要给出可读的报错。
func TestLocateReportsMissingDirectory(t *testing.T) {
	t.Setenv(envAuthDir, filepath.Join(t.TempDir(), "not-created"))
	if _, err := Locate(""); err == nil {
		t.Fatal("目录不存在时应当报错")
	}
}

// TestSectionAndText 覆盖登录态文档的取值辅助函数。
func TestSectionAndText(t *testing.T) {
	doc := map[string]any{
		"auth":    map[string]any{"domain": "www.codebuddy.cn", "expiresAt": float64(1)},
		"account": "不是对象",
		"scalar":  "值",
	}
	if got := text(authSection(doc), "domain"); got != "www.codebuddy.cn" {
		t.Errorf("domain = %q", got)
	}
	// 类型不符或缺失都应退化成空值，而不是 panic。
	if got := text(authSection(doc), "expiresAt"); got != "" {
		t.Errorf("数值字段取文本应得空串，实际 %q", got)
	}
	if got := section(doc, "account"); len(got) != 0 {
		t.Errorf("类型不符应返回空对象，实际 %v", got)
	}
	if got := text(doc, "scalar"); got != "值" {
		t.Errorf("scalar = %q", got)
	}
	if got := number(authSection(doc), "expiresAt"); got != 1 {
		t.Errorf("number = %v，期望 1", got)
	}
	if got := number(authSection(doc), "missing"); got != 0 {
		t.Errorf("缺失数值应得 0，实际 %v", got)
	}
}

// TestIsExpired 覆盖续期判定，含提前 60 秒的边界。
func TestIsExpired(t *testing.T) {
	now := time.Now()
	doc := func(expiresAt time.Time) map[string]any {
		return map[string]any{"auth": map[string]any{"expiresAt": float64(expiresAt.UnixMilli())}}
	}
	cases := []struct {
		name string
		doc  map[string]any
		want bool
	}{
		{"还早", doc(now.Add(24 * time.Hour)), false},
		{"已过期", doc(now.Add(-time.Hour)), true},
		{"落在提前量内", doc(now.Add(30 * time.Second)), true},
		{"刚好越过提前量", doc(now.Add(2 * time.Minute)), false},
		// 没有 expiresAt 时不能一律当作过期，否则每次请求都会去续期。
		{"没有有效期字段", map[string]any{"auth": map[string]any{}}, false},
	}
	for _, testCase := range cases {
		if got := isExpired(testCase.doc, now); got != testCase.want {
			t.Errorf("%s：isExpired = %v，期望 %v", testCase.name, got, testCase.want)
		}
	}
}

// TestBuildHeaders 组装后端请求头：企业字段要同时填两个头，
// 令牌为空时不带 Authorization（刷新请求就是这种形态）。
func TestBuildHeaders(t *testing.T) {
	doc := map[string]any{
		"auth":    map[string]any{"domain": "example.com"},
		"account": map[string]any{"uid": "u1", "enterpriseId": "e1"},
	}
	headers := buildHeaders(doc, "token")
	if headers["Authorization"] != "Bearer token" {
		t.Errorf("Authorization = %q", headers["Authorization"])
	}
	if headers["X-Domain"] != "example.com" || headers["X-User-Id"] != "u1" {
		t.Errorf("基础头不对：%v", headers)
	}
	if headers["X-Enterprise-Id"] != "e1" || headers["X-Tenant-Id"] != "e1" {
		t.Errorf("企业字段应同时填两个头：%v", headers)
	}
	if _, ok := buildHeaders(doc, "")["Authorization"]; ok {
		t.Error("令牌为空时不该带 Authorization")
	}

	// 登录态里没写 domain 时用兜底域名。
	fallback := buildHeaders(map[string]any{}, "t")
	if fallback["X-Domain"] == "" {
		t.Error("domain 缺失时应有兜底值")
	}
}

// TestFillExpiry 后端只给相对秒数时要补出绝对毫秒时间戳。
func TestFillExpiry(t *testing.T) {
	before := time.Now().UnixMilli()

	auth := map[string]any{"expiresIn": float64(3600)}
	fillExpiry(auth, "expiresAt", "expiresIn")
	after := time.Now().UnixMilli()
	expiresAt, ok := auth["expiresAt"].(float64)
	if !ok {
		t.Fatalf("没有补出 expiresAt：%v", auth)
	}
	if int64(expiresAt) < before+3600_000 || int64(expiresAt) > after+3600_000 {
		t.Errorf("expiresAt 换算不对：%v", int64(expiresAt))
	}

	// 已有绝对时间戳时不能覆盖。
	existing := map[string]any{"expiresAt": float64(123), "expiresIn": float64(3600)}
	fillExpiry(existing, "expiresAt", "expiresIn")
	if existing["expiresAt"] != float64(123) {
		t.Errorf("已有的 expiresAt 被覆盖了：%v", existing["expiresAt"])
	}

	// 两个字段都没有时应该什么都不做，而不是塞一个 0。
	empty := map[string]any{}
	fillExpiry(empty, "expiresAt", "expiresIn")
	if len(empty) != 0 {
		t.Errorf("缺字段时不该写入任何东西：%v", empty)
	}
}
