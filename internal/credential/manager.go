package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
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
//
// 网关与 WorkBuddy 桌面端共用同一份登录态，而桌面端刷新令牌时会轮换
// refreshToken，网关若继续持旧令牌请求，可能被后端判定为凭据冲突。
// 因此每次取请求头前都会重新 stat 文件，内容变了就重新载入；
// 若载入后发现令牌已不在有效区间，则用文件里的新令牌，而不是自己的旧副本。
type Manager struct {
	path   string         // 登录态文件路径
	cipher *atrest.Cipher // 字段级加解密器
	client *http.Client   // 刷新令牌用的 HTTP 客户端
	report Reporter       // 刷新事件汇报；为 nil 时静默
	base   string         // 后端根地址，用于刷新令牌

	mu    sync.Mutex // 串行化读取与刷新，避免并发重复刷新
	doc   map[string]any
	mtime time.Time
}

// NewManager 构造一个凭据管理器。report 允许为 nil；
// baseURL 留空时指向官方后端，传入自定义地址可以接管刷新请求。
func NewManager(path string, cipher *atrest.Cipher, report Reporter, baseURL string) *Manager {
	if baseURL == "" {
		baseURL = wbapi.BaseURL
	}
	return &Manager{
		path:   path,
		cipher: cipher,
		client: &http.Client{Timeout: refreshTimeout},
		report: report,
		base:   strings.TrimSuffix(baseURL, "/"),
	}
}

// ErrUnauthorized 表示后端以 401 拒绝了请求：令牌已被轮换或吊销。
// 调用方（upstream）捕获它时应重新取一次请求头再重试，只重试一次。
var ErrUnauthorized = errors.New("后端返回 401，访问令牌可能已被轮换")

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
	return m.headersLocked(doc)
}

// RetryAfterUnauthorized 在一次 401 之后重新取请求头。
//
// 网关与 WorkBuddy 桌面端共用同一份登录态，桌面端刷新令牌时会轮换
// refreshToken。存在这样一个竞态：网关在桌面端轮换的**同一瞬间**发出请求，
// 带上的是刚被作废的令牌，后端回 401。此时文件里已经是新令牌，
// 重新取一次请求头即可恢复，不需要用户干预。
//
// 这里强制丢弃内存缓存重新读文件：万一桌面端改写文件的 mtime 与
// 上一次相同（同秒内写入），仅靠 mtime 比对会认不出变化。
// 若新取到的令牌与刚才那枚相同，说明不是竞态而是真的失效，直接报错，
// 避免调用方无限重试。
func (m *Manager) RetryAfterUnauthorized(previousToken string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.doc = nil
	m.mtime = time.Time{}
	doc, err := m.loadLocked()
	if err != nil {
		return nil, err
	}
	headers, err := m.headersLocked(doc)
	if err != nil {
		return nil, err
	}
	if headers["Authorization"] == "Bearer "+previousToken {
		return nil, fmt.Errorf("%w：重新载入登录态后令牌未变化，可能已失效，"+
			"请打开 WorkBuddy 桌面端确认登录状态", ErrUnauthorized)
	}
	return headers, nil
}

// headersLocked 取文档里的访问令牌并组装请求头；过期则先刷新。调用方须已持锁。
func (m *Manager) headersLocked(doc map[string]any) (map[string]string, error) {
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
//
// 文件被外部（WorkBuddy 桌面端）改写是常态：桌面端也会刷新令牌并轮换
// refreshToken。若沿用内存里的旧文档继续请求，后端看到的是一枚已被轮换掉的
// 令牌，可能判定为凭据冲突甚至强制下线。所以这里以文件为准，而不是以缓存为准。
func (m *Manager) loadLocked() (map[string]any, error) {
	info, err := os.Stat(m.path)
	if err != nil {
		if m.doc != nil {
			// 文件暂时读不到（权限抖动、被杀毒软件锁住等）时退回缓存，
			// 不让一次瞬时故障把整个网关打停。
			return m.doc, nil
		}
		return nil, fmt.Errorf("读取登录态文件失败：%w", err)
	}
	if m.doc != nil && info.ModTime().Equal(m.mtime) {
		return m.doc, nil
	}
	raw, err := os.ReadFile(m.path)
	if err != nil {
		if m.doc != nil {
			return m.doc, nil
		}
		return nil, fmt.Errorf("读取登录态文件失败：%w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		// 文件被写坏的窗口期同样退回缓存；解析不出东西时才报错。
		if m.doc != nil {
			return m.doc, nil
		}
		return nil, fmt.Errorf("登录态文件不是合法 JSON：%w", err)
	}
	if m.doc == nil || !sameToken(m.doc, doc) {
		m.reportf("检测到登录态文件变化，已重新载入")
	}
	m.doc = doc
	m.mtime = info.ModTime()
	return doc, nil
}

// sameToken 判断两份登录态文档的访问令牌是否相同。用于识别
// 「文件变了但令牌没变」（例如桌面端只更新了别的字段）与
// 「令牌被轮换了」这两种情况，避免每次 stat 都报一条噪音日志。
//
// 令牌可能是密文信封，也可能是明文；两种形态必须能区分开，
// 否则「桌面端把明文令牌换成了信封」会被误判成没变。
func sameToken(old, fresh map[string]any) bool {
	if old == nil || fresh == nil {
		return false
	}
	left, right := authSection(old)["accessToken"], authSection(fresh)["accessToken"]
	if atrest.IsEncrypted(left) != atrest.IsEncrypted(right) {
		return false
	}
	if !atrest.IsEncrypted(left) {
		// 明文形态直接按字符串比。
		leftText, _ := left.(string)
		rightText, _ := right.(string)
		return leftText == rightText
	}
	return envelopeOf(left) == envelopeOf(right)
}

// envelopeOf 取出加密信封里的 envelope 字段；不是信封时返回空串。
func envelopeOf(value any) string {
	if obj, ok := value.(map[string]any); ok {
		if envelope, ok := obj["envelope"].(string); ok {
			return envelope
		}
	}
	return ""
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
	if err := m.postJSON(m.base+wbapi.RefreshPath, headers, &payload); err != nil {
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
