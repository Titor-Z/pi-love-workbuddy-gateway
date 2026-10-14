package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
)

// testSecret 是一个固定的 at-rest 根密钥，仅供测试派生解密器。
// 真实运行时密钥来自环境变量或 WorkBuddy 原生层，测试不碰它们。
const testSecret = "c2VjcmV0LXNoYXJlZC1rZXktZm9yLXRlc3RzLW9ubHk="


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

// newTestManager 造一个指向临时文件的管理器。文件内容由调用方写入。
func newTestManager(t *testing.T, path string, report Reporter) *Manager {
	t.Helper()
	cipher, err := atrest.NewCipher(testSecret)
	if err != nil {
		t.Fatalf("构造解密器失败：%v", err)
	}
	return NewManager(path, cipher, report, "")
}

// writeAuthFile 写一份登录态文档，accessToken 为给定明文，
// expiresAt 由调用方指定（毫秒）。
func writeAuthFile(t *testing.T, path, token string, expiresAt int64) {
	t.Helper()
	doc := map[string]any{
		"auth":    map[string]any{"accessToken": token, "expiresAt": float64(expiresAt)},
		"account": map[string]any{"uid": "u1", "enterpriseId": "e1"},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
}

// TestLoadPicksUpExternalRewrite 桌面端改写登录态后，网关必须改用文件里的
// 新令牌，而不是继续用自己缓存的旧令牌——旧令牌已被后端轮换掉，
// 拿它请求可能被判定为凭据冲突。
func TestLoadPicksUpExternalRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workbuddy-desktop.info")
	future := time.Now().Add(time.Hour).UnixMilli()
	writeAuthFile(t, path, "old-token", future)

	var notes []string
	manager := newTestManager(t, path, func(format string, args ...any) {
		notes = append(notes, fmt.Sprintf(format, args...))
	})

	first, err := manager.Headers()
	if err != nil {
		t.Fatalf("首次取请求头失败：%v", err)
	}
	if first["Authorization"] != "Bearer old-token" {
		t.Fatalf("首次令牌 = %q", first["Authorization"])
	}

	// 模拟桌面端刷新：换一枚新令牌。mtime 会变，但内容才是判据。
	time.Sleep(10 * time.Millisecond)
	writeAuthFile(t, path, "new-token", future)

	second, err := manager.Headers()
	if err != nil {
		t.Fatalf("文件改写后取请求头失败：%v", err)
	}
	if second["Authorization"] != "Bearer new-token" {
		t.Fatalf("文件改写后仍用旧令牌 = %q，这会导致凭据冲突", second["Authorization"])
	}
	if len(notes) == 0 {
		t.Error("令牌被轮换时应当汇报一条重新载入")
	}
}

// TestLoadDoesNotReportWhenTokenUnchanged 文件 mtime 变了但令牌没变时，
// 不该刷「已重新载入」的噪音——桌面端改别的字段很常见。
func TestLoadDoesNotReportWhenTokenUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workbuddy-desktop.info")
	future := time.Now().Add(time.Hour).UnixMilli()
	writeAuthFile(t, path, "same-token", future)

	var notes []string
	manager := newTestManager(t, path, func(format string, args ...any) {
		notes = append(notes, fmt.Sprintf(format, args...))
	})
	if _, err := manager.Headers(); err != nil {
		t.Fatalf("首次失败：%v", err)
	}
	notes = nil

	time.Sleep(10 * time.Millisecond)
	writeAuthFile(t, path, "same-token", future)

	if _, err := manager.Headers(); err != nil {
		t.Fatalf("二次失败：%v", err)
	}
	if len(notes) != 0 {
		t.Errorf("令牌未变时不该汇报重新载入，实际：%v", notes)
	}
}

// TestLoadFallsBackToCacheOnTransientFailure 文件被瞬时锁住或写坏时，
// 应退回缓存而不是把请求打失败——桌面端写文件存在中间态。
func TestLoadFallsBackToCacheOnTransientFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workbuddy-desktop.info")
	future := time.Now().Add(time.Hour).UnixMilli()
	writeAuthFile(t, path, "cached-token", future)

	manager := newTestManager(t, path, nil)
	if _, err := manager.Headers(); err != nil {
		t.Fatalf("预热失败：%v", err)
	}

	// 写坏文件：此时应继续用缓存里的好令牌。
	if err := os.WriteFile(path, []byte("{ 不是 JSON"), 0o600); err != nil {
		t.Fatalf("写坏文件失败：%v", err)
	}
	headers, err := manager.Headers()
	if err != nil {
		t.Fatalf("文件写坏时应当退回缓存，实际报错：%v", err)
	}
	if headers["Authorization"] != "Bearer cached-token" {
		t.Fatalf("退回的令牌不对 = %q", headers["Authorization"])
	}
}

// TestSameToken 比较两份登录态的访问令牌，加密与非加密形态都要能判。
// 判错的后果是「令牌被轮换了却当成没变」，网关会继续拿旧令牌请求。
func TestSameToken(t *testing.T) {
	// 每个用例直接给完整的登录态文档，令牌嵌在 auth 段里。
	doc := func(token any) map[string]any {
		return map[string]any{"auth": map[string]any{"accessToken": token}}
	}
	sealed := doc(map[string]any{"$wbEncrypted": float64(1), "envelope": "abc"})
	otherSealed := doc(map[string]any{"$wbEncrypted": float64(1), "envelope": "def"})
	plain := doc("abc")
	missing := doc(nil)

	cases := []struct {
		name  string
		left  map[string]any
		right map[string]any
		want  bool
	}{
		{"同一信封", sealed, sealed, true},
		{"不同信封", sealed, otherSealed, false},
		{"同一明文", plain, plain, true},
		{"不同明文", plain, doc("def"), false},
		{"明文与信封不同形态", plain, sealed, false},
		{"一侧没有令牌", missing, sealed, false},
		{"两侧都没有令牌", missing, missing, true},
	}
	for _, testCase := range cases {
		if got := sameToken(testCase.left, testCase.right); got != testCase.want {
			t.Errorf("%s：sameToken = %v，期望 %v", testCase.name, got, testCase.want)
		}
	}
}

// TestLoadFallsBackToCacheWhenFileVanishes 文件在 stat 成功之后、
// 真正读取之前被删掉（桌面端偶尔会这样做），应退回缓存。
func TestLoadFallsBackToCacheWhenFileVanishes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workbuddy-desktop.info")
	future := time.Now().Add(time.Hour).UnixMilli()
	writeAuthFile(t, path, "kept-token", future)

	manager := newTestManager(t, path, nil)
	if _, err := manager.Headers(); err != nil {
		t.Fatalf("预热失败：%v", err)
	}

	// 用一个已存在的目录占住路径，读它必然失败（EISDIR），
	// 比 os.Remove 更能稳定复现「读不到」而非「不存在」。
	if err := os.Remove(path); err != nil {
		t.Fatalf("删除文件失败：%v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	headers, err := manager.Headers()
	if err != nil {
		t.Fatalf("文件读不到时应当退回缓存，实际报错：%v", err)
	}
	if headers["Authorization"] != "Bearer kept-token" {
		t.Fatalf("退回的令牌不对 = %q", headers["Authorization"])
	}
}

// TestLoadReportsErrorWithoutCache 从未载入过时文件不可读，
// 必须报错而不是返回空请求头——那会让后端收到没有令牌的请求。
func TestLoadReportsErrorWithoutCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.info")
	manager := newTestManager(t, path, nil)
	if _, err := manager.Headers(); err == nil {
		t.Fatal("没有缓存且文件不存在时应当报错")
	}
}

// TestRetryAfterUnauthorizedForcesReload 401 重试必须强制重读文件。
// 桌面端在同一秒内改写登录态时 mtime 可能与上次相同，仅靠 mtime
// 比对认不出变化，会一直拿着已作废的令牌。
func TestRetryAfterUnauthorizedForcesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workbuddy-desktop.info")
	future := time.Now().Add(time.Hour).UnixMilli()
	// 先把 mtime 固定住，再预热，让缓存里记下的就是这个固定值。
	fixed := time.Now().Add(-time.Minute).Truncate(time.Second)
	writeAuthFile(t, path, "old-token", future)
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatalf("固定时间失败：%v", err)
	}

	manager := newTestManager(t, path, nil)
	first, err := manager.Headers()
	if err != nil {
		t.Fatalf("预热失败：%v", err)
	}

	// 换成新令牌，但保持 mtime 不变：内容变了、时间戳没变。
	writeAuthFile(t, path, "new-token", future)
	if err := os.Chtimes(path, fixed, fixed); err != nil {
		t.Fatalf("固定时间失败：%v", err)
	}

	retried, err := manager.RetryAfterUnauthorized(
		strings.TrimPrefix(first["Authorization"], "Bearer "))
	if err != nil {
		t.Fatalf("强制重读失败：%v", err)
	}
	if retried["Authorization"] != "Bearer new-token" {
		t.Fatalf("重试令牌 = %q，期望新令牌", retried["Authorization"])
	}
}

// TestRetryAfterUnauthorizedRejectsUnchangedToken 重读后令牌没变，
// 说明不是竞态而是真的失效。必须报错，否则调用方可能无限重试。
func TestRetryAfterUnauthorizedRejectsUnchangedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workbuddy-desktop.info")
	writeAuthFile(t, path, "unchanged-token", time.Now().Add(time.Hour).UnixMilli())

	manager := newTestManager(t, path, nil)
	first, err := manager.Headers()
	if err != nil {
		t.Fatalf("预热失败：%v", err)
	}

	_, err = manager.RetryAfterUnauthorized(
		strings.TrimPrefix(first["Authorization"], "Bearer "))
	if err == nil {
		t.Fatal("令牌未变化时应当报错")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("错误应包裹 ErrUnauthorized，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "失效") {
		t.Errorf("错误信息应说明令牌可能已失效，实际：%v", err)
	}
}
