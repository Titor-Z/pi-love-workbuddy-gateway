package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/wbapi"
)

// expirySkew 是提前判定令牌过期的时间，避免请求在途中正好失效。
const expirySkew = 60 * time.Second

// refreshTimeout 是刷新令牌请求的超时时间。
const refreshTimeout = 15 * time.Second

// Reporter 由调用方注入，用于汇报令牌刷新事件。允许为 nil。
// 这样 credential 包不必依赖具体的日志实现。
type Reporter func(format string, args ...any)

// Manager 持有登录态文件，对外提供「始终有效」的后端请求头。
// 令牌临近过期时会自动刷新并原子回写文件，供 WorkBuddy 客户端继续复用。
type Manager struct {
	path   string         // 登录态文件路径
	cipher *atrest.Cipher // 字段级加解密器
	client *http.Client   // 刷新令牌用的 HTTP 客户端
	report Reporter       // 刷新事件汇报；为 nil 时静默

	mu    sync.Mutex // 串行化读取与刷新，避免并发重复刷新
	doc   map[string]any
	mtime time.Time
}

// NewManager 构造一个凭据管理器。report 允许为 nil。
func NewManager(path string, cipher *atrest.Cipher, report Reporter) *Manager {
	return &Manager{
		path:   path,
		cipher: cipher,
		client: &http.Client{Timeout: refreshTimeout},
		report: report,
	}
}

// Path 返回登录态文件路径，供日志展示。
func (m *Manager) Path() string { return m.path }

// Headers 返回携带最新访问令牌的后端请求头；必要时先刷新令牌。
func (m *Manager) Headers() (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, err := m.loadLocked()
	if err != nil {
		return nil, err
	}
	if isExpired(doc, time.Now()) {
		if err := m.refreshLocked(doc); err != nil {
			return nil, err
		}
	}
	accessToken, err := m.cipher.Decrypt(authSection(doc)["accessToken"])
	if err != nil {
		return nil, fmt.Errorf("解密访问令牌失败：%w", err)
	}
	if accessToken == "" {
		return nil, errors.New("登录态里没有访问令牌，请在 WorkBuddy 客户端重新登录")
	}
	return buildHeaders(doc, accessToken), nil
}

// Summary 返回脱敏后的账号摘要，供 /health 展示。
func (m *Manager) Summary() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()

	doc, err := m.loadLocked()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	auth := authSection(doc)
	account := section(doc, "account")
	expiresAt, _ := auth["expiresAt"].(float64)
	return map[string]any{
		"uid":              text(account, "uid"),
		"token_expires_at": int64(expiresAt),
		"token_expired":    isExpired(doc, time.Now()),
	}
}

// loadLocked 读取登录态文档，并在文件被外部改动时重新载入。调用方须已持锁。
func (m *Manager) loadLocked() (map[string]any, error) {
	info, err := os.Stat(m.path)
	if err != nil {
		if m.doc != nil {
			return m.doc, nil
		}
		return nil, fmt.Errorf("读取登录态文件失败：%w", err)
	}
	if m.doc != nil && info.ModTime().Equal(m.mtime) {
		return m.doc, nil
	}
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return nil, fmt.Errorf("读取登录态文件失败：%w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("登录态文件不是合法 JSON：%w", err)
	}
	m.doc = doc
	m.mtime = info.ModTime()
	return doc, nil
}

// refreshLocked 调用后端刷新访问令牌，并把结果回写登录态文件。调用方须已持锁。
func (m *Manager) refreshLocked(doc map[string]any) error {
	previous := authSection(doc)
	refreshToken, err := m.cipher.Decrypt(previous["refreshToken"])
	if err != nil {
		return fmt.Errorf("解密刷新令牌失败：%w", err)
	}
	if refreshToken == "" {
		return errors.New("登录态里没有刷新令牌，请在 WorkBuddy 客户端重新登录")
	}
	m.reportf("访问令牌已过期，正在刷新")

	headers := buildHeaders(doc, "")
	headers["X-Refresh-Token"] = refreshToken
	headers["X-Auth-Refresh-Source"] = "plugin"

	var payload struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := m.postJSON(wbapi.BaseURL+wbapi.RefreshPath, headers, &payload); err != nil {
		return err
	}
	if payload.Code != 0 || payload.Data == nil {
		return fmt.Errorf("刷新令牌被拒绝：%s", payload.Msg)
	}

	newAuth := payload.Data
	accessToken := text(newAuth, "accessToken")
	if accessToken == "" {
		return errors.New("刷新响应缺少访问令牌")
	}
	// 后端未必回传刷新令牌，缺失时沿用旧值，避免把明文再封一次。
	if text(newAuth, "refreshToken") == "" {
		newAuth["refreshToken"] = refreshToken
	}
	if text(newAuth, "domain") == "" {
		newAuth["domain"] = text(previous, "domain")
	}
	newAuth["lastRefreshTime"] = float64(time.Now().UnixMilli())
	fillExpiry(newAuth, "expiresAt", "expiresIn")
	fillExpiry(newAuth, "refreshExpiresAt", "refreshExpiresIn")

	doc["auth"] = newAuth
	if err := m.persistLocked(doc, previous); err != nil {
		return err
	}
	m.reportf("令牌刷新成功，新有效期 %s，已回写登录态",
		time.UnixMilli(int64(number(newAuth, "expiresAt"))).Format(time.RFC3339))
	return nil
}

// reportf 向注入的汇报器发一条消息；未注入时静默。
func (m *Manager) reportf(format string, args ...any) {
	if m.report != nil {
		m.report(format, args...)
	}
}

// postJSON 发一个 JSON 为空对象的 POST，并把响应解析进 out。
func (m *Manager) postJSON(url string, headers map[string]string, out any) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return fmt.Errorf("构造刷新请求失败：%w", err)
	}
	for name, value := range headers {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("刷新令牌网络失败：%w", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("刷新响应不是合法 JSON（HTTP %d）：%w", resp.StatusCode, err)
	}
	return nil
}

// persistLocked 原子回写登录态文件。若原文件是加密格式，则先把令牌重新封装，
// 否则 WorkBuddy 客户端读到明文会判定完整性错误、可能重置登录态。
func (m *Manager) persistLocked(doc map[string]any, previous map[string]any) error {
	newAuth := authSection(doc)
	if atrest.IsEncrypted(previous["accessToken"]) {
		for _, key := range []string{"accessToken", "refreshToken"} {
			plain, err := m.cipher.Decrypt(newAuth[key])
			if err != nil {
				return fmt.Errorf("回写前解密 %s 失败：%w", key, err)
			}
			if plain == "" {
				continue
			}
			sealed, err := m.cipher.Encrypt(plain)
			if err != nil {
				return fmt.Errorf("回写前加密 %s 失败：%w", key, err)
			}
			newAuth[key] = sealed
		}
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化登录态失败：%w", err)
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入临时登录态失败：%w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("替换登录态文件失败：%w", err)
	}
	if info, err := os.Stat(m.path); err == nil {
		m.mtime = info.ModTime()
	}
	return nil
}

// buildHeaders 组装后端请求头。accessToken 为空时只用于刷新请求。
func buildHeaders(doc map[string]any, accessToken string) map[string]string {
	auth := authSection(doc)
	account := section(doc, "account")
	domain := text(auth, "domain")
	if domain == "" {
		domain = wbapi.DefaultDomain
	}
	enterprise := text(account, "enterpriseId")
	headers := map[string]string{
		"Content-Type":    "application/json",
		"Accept":          "application/json",
		"X-User-Id":       text(account, "uid"),
		"X-Enterprise-Id": enterprise,
		"X-Tenant-Id":     enterprise,
		"X-Domain":        domain,
		"User-Agent":      wbapi.UserAgent,
	}
	if accessToken != "" {
		headers["Authorization"] = "Bearer " + accessToken
	}
	return headers
}

// isExpired 判断访问令牌是否已过期（含提前量）。expiresAt 单位是毫秒。
func isExpired(doc map[string]any, now time.Time) bool {
	expiresAt, _ := authSection(doc)["expiresAt"].(float64)
	if expiresAt == 0 {
		return false
	}
	deadline := time.UnixMilli(int64(expiresAt)).Add(-expirySkew)
	return !now.Before(deadline)
}

// fillExpiry 在后端只给了相对秒数时补出绝对毫秒时间戳。
func fillExpiry(auth map[string]any, absoluteKey, relativeKey string) {
	if _, ok := auth[absoluteKey].(float64); ok {
		return
	}
	seconds, ok := auth[relativeKey].(float64)
	if !ok {
		return
	}
	auth[absoluteKey] = float64(time.Now().UnixMilli()) + seconds*1000
}

// authSection 取出登录态里的 auth 段。
func authSection(doc map[string]any) map[string]any { return section(doc, "auth") }

// section 取出文档里的一段对象；缺失或类型不符时返回空对象，避免到处判空。
func section(doc map[string]any, key string) map[string]any {
	if obj, ok := doc[key].(map[string]any); ok {
		return obj
	}
	return map[string]any{}
}

// text 取出对象里的字符串字段；缺失或类型不符时返回空串。
func text(obj map[string]any, key string) string {
	if value, ok := obj[key].(string); ok {
		return value
	}
	return ""
}

// number 取出对象里的数值字段；缺失或类型不符时返回 0。
func number(obj map[string]any, key string) float64 {
	if value, ok := obj[key].(float64); ok {
		return value
	}
	return 0
}
