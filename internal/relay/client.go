package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relaytls"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// Config 是客户端侧的转发配置。
type Config struct {
	// BaseURL 形如 https://192.168.1.5:8790，不含结尾斜杠。
	BaseURL string
	// PSK 是网关控制台给出的接入密钥。
	PSK string
	// Fingerprint 是已确认过的网关证书指纹；为空则拒绝连接。
	Fingerprint string
	// Device 是本设备身份，随每个请求一起送出用于审计。
	Device *identity.Device
	// Client 允许注入自定义的 http.Client，仅测试使用。
	Client *http.Client
}

// Client 把请求转发给网关。它实现 gateway.Backend，
// 因此 client 模式下的 gateway.Server 完全不知道自己手上没有凭证。
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient 构造转发客户端。三件事缺一不可：网关地址、密钥、已确认的指纹。
func NewClient(cfg Config) (*Client, error) {
	base, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	cfg.BaseURL = base
	if cfg.PSK == "" {
		return nil, fmt.Errorf("尚未填入网关密钥")
	}
	if cfg.Fingerprint == "" {
		// 为空意味着「谁都可以冒充网关」——与明文没有区别，不能放行。
		return nil, fmt.Errorf("尚未确认网关证书指纹")
	}
	if cfg.Device == nil || cfg.Device.ID == "" {
		return nil, fmt.Errorf("设备身份未初始化")
	}
	httpClient := cfg.Client
	if httpClient == nil {
		httpClient = &http.Client{
			// 不设 Timeout：流式响应要能一直开着，何时结束由 ctx 决定。
			// 拨号与握手的超时在 Transport 里单独设。
			Transport: relaytls.Transport(cfg.Fingerprint),
		}
	}
	return &Client{cfg: cfg, http: httpClient}, nil
}

// RedeemPairCode 用一次性配对码换回长密钥。
//
// 这一步发生在**配对之前**，所以它不能用 Client（Client 的构造就要求已有密钥）。
// 指纹仍然要钉：否则中途换个人冒充网关，用户就把长密钥交出去了。
func RedeemPairCode(ctx context.Context, baseURL, fingerprint, code string) (string, error) {
	base, err := normalizeBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	if fingerprint == "" {
		return "", fmt.Errorf("尚未确认网关证书指纹")
	}
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("请填入网关控制台显示的配对码")
	}
	payload, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+pairPath, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("构造配对请求失败：%w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{Transport: relaytls.Transport(fingerprint)}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("连接网关失败：%w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return "", fmt.Errorf("读取配对响应失败：%w", err)
	}
	if response.StatusCode != http.StatusOK {
		return "", relayErrorMessage(response.StatusCode, body)
	}
	var result struct {
		PSK string `json:"psk"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("配对响应不是合法 JSON：%w", err)
	}
	if result.PSK == "" {
		return "", fmt.Errorf("网关没有返回密钥")
	}
	return result.PSK, nil
}

// relayErrorMessage 把 relay 的错误响应还原成一句人话。
func relayErrorMessage(status int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Message != "" {
		return errors.New(payload.Error.Message)
	}
	return fmt.Errorf("网关返回 HTTP %d", status)
}

// ChatStream 转发一次对话补全，返回尚未读取的原始响应。
//
// 请求体**不做任何解析与改写**：改写（developer→system、字段白名单）已经在
// 客户端本地的 gateway.Server 里做过了，在这里再做一遍是重复且危险的。
func (c *Client) ChatStream(ctx context.Context, body []byte) (*http.Response, error) {
	request, err := c.newRequest(ctx, http.MethodPost, "/v1/chat/completions",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return nil, translateError(err)
	}
	if response.StatusCode != http.StatusOK {
		// 这里的错误体已经是网关写好的结构，原样放到上层去。
		return response, nil
	}
	return response, nil
}

// Models 拉取网关侧的实时模型目录。
func (c *Client) Models(ctx context.Context) ([]upstream.ModelInfo, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, translateError(err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	if err != nil {
		return nil, translateError(err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, errorsFromBody(response.StatusCode, raw)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
			// WorkBuddy 扩展对象里有倍率与促销信息，
			// 而 source 是未经加工的原始字段——客户端要靠它重建 ModelInfo。
			WorkBuddy struct {
				Source struct {
					Name          string   `json:"name"`
					Credits       string   `json:"credits"`
					Tags          []string `json:"tags"`
					ContextWindow int      `json:"context_window"`
					MaxTokens     int      `json:"max_tokens"`
					Images        bool     `json:"images"`
					Tools         bool     `json:"tools"`
					Thinking      bool     `json:"thinking"`
					Efforts       []string `json:"efforts"`
				} `json:"source"`
			} `json:"workbuddy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("网关返回的模型目录不是合法 JSON：%w", err)
	}
	models := make([]upstream.ModelInfo, 0, len(payload.Data))
	for _, item := range payload.Data {
		if item.ID == "" {
			continue
		}
		source := item.WorkBuddy.Source
		models = append(models, upstream.ModelInfo{
			ID:               item.ID,
			Name:             source.Name,
			Credits:          source.Credits,
			MaxInputTokens:   source.ContextWindow,
			MaxOutputTokens:  source.MaxTokens,
			SupportsImages:   source.Images,
			SupportsToolCall: source.Tools,
			SupportsThinking: source.Thinking,
			Efforts:          source.Efforts,
			Tags:             source.Tags,
		})
	}
	return models, nil
}

// FetchBillingReport 向网关要一份余额。
//
// 客户端模式下凭证在网关那台机器上，本机没有账单可读——这是唯一一条
// 拿到余额的路。返回的是网关算好的对外表示，本机不再映射一遍。
func (c *Client) FetchBillingReport(ctx context.Context) (billing.Report, error) {
	var report billing.Report
	request, err := c.newRequest(ctx, http.MethodGet, billingPath, nil)
	if err != nil {
		return report, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return report, translateError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	if err != nil {
		return report, translateError(err)
	}
	if response.StatusCode != http.StatusOK {
		return report, errorsFromBody(response.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return report, fmt.Errorf("网关返回的余额不是合法 JSON：%w", err)
	}
	return report, nil
}

// FetchModelOverrides 拉取网关权威那一份模型覆盖表。
func (c *Client) FetchModelOverrides(ctx context.Context) (*catalog.Overrides, error) {
	request, err := c.newRequest(ctx, http.MethodGet, overridesPath, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, translateError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOverridesBody))
	if err != nil {
		return nil, translateError(err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, errorsFromBody(response.StatusCode, raw)
	}
	table, err := catalog.LoadOverridesFromJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("网关返回的模型覆盖表不是合法 JSON：%w", err)
	}
	return table, nil
}

// PushModelOverrides 把合并后的覆盖表推给网关。
//
// baseRevision 是这次合并所依据的版本；网关那边的版本已经变了就返回
// catalog.ErrStaleRevision，调用方要重新拉取、重新合并。
func (c *Client) PushModelOverrides(ctx context.Context, baseRevision int64,
	models map[string]catalog.Override) (*catalog.Overrides, error) {

	payload, err := json.Marshal(map[string]any{
		"base_revision": baseRevision,
		"models":        models,
	})
	if err != nil {
		return nil, fmt.Errorf("编码模型覆盖表失败：%w", err)
	}
	request, err := c.newRequest(ctx, http.MethodPut, overridesPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, translateError(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOverridesBody))
	if err != nil {
		return nil, translateError(err)
	}
	if response.StatusCode == http.StatusConflict {
		// 409 是「你先重新拉一次」而不是网络故障，单独还原成可判断的错误。
		return nil, catalog.ErrStaleRevision
	}
	if response.StatusCode != http.StatusOK {
		return nil, errorsFromBody(response.StatusCode, raw)
	}
	table, err := catalog.LoadOverridesFromJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("网关返回的模型覆盖表不是合法 JSON：%w", err)
	}
	return table, nil
}

// Ping 确认网关是否可达且协议版本一致。UI 用它显示「网关在线」。
func (c *Client) Ping(ctx context.Context) error {
	request, err := c.newRequest(ctx, http.MethodGet, pingPath, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return translateError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		return errorsFromBody(response.StatusCode, raw)
	}
	return nil
}

// newRequest 构造带鉴权头的请求。
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("构造转发请求失败：%w", err)
	}
	request.Header.Set(HeaderPSK, c.cfg.PSK)
	request.Header.Set(HeaderDevice, c.cfg.Device.ID)
	if name := strings.TrimSpace(c.cfg.Device.Name); name != "" {
		request.Header.Set(HeaderDeviceName, name)
	}
	return request, nil
}

// translateError 把传输层的错误翻译成用户能看懂的话。
// 区分「网关没回应」和「网关不接受我们」，这两种情况的处理方式完全不同。
func translateError(err error) error {
	message := err.Error()
	switch {
	case strings.Contains(message, "网关证书已变化"):
		return fmt.Errorf("%s", message)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("网关没有响应（等待超时）：%w", err)
	default:
		return fmt.Errorf("无法连接网关：%w", err)
	}
}

// errorsFromBody 把网关的错误响应翻译成本地错误。
func errorsFromBody(code int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Error.Message == "" {
		return fmt.Errorf("网关返回 HTTP %d：%s", code, truncate(string(body)))
	}
	return fmt.Errorf("%s", payload.Error.Message)
}

// maxErrorBody 是读取错误响应体的上限，避免异常响应撑爆内存。
const maxErrorBody = 64 << 10

// maxOverridesBody 是覆盖表响应的上限。覆盖表是人工维护的小文件，
// 1MB 已经远远够用；超出说明对方不是我们认识的网关。
const maxOverridesBody = 1 << 20

// truncate 截断过长的错误正文。
func truncate(text string) string {
	const limit = 300
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// deadline 是客户端侧给网关的响应上限。
// 只用于判断是否算「网关没回应」，与流式时长无关——流式由调用方的 ctx 控制。
const deadline = 30 * time.Second

// normalizeBaseURL 规整网关地址：补 https://、去掉结尾斜杠。
//
// 用户的输入可能带也可能不带协议前缀，补一是省掉一条没意义的报错；
// 但绝不默认 http：请求正文会经过局域网，明文等于把账号与对话内容一起播出去。
func normalizeBaseURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("尚未指定网关地址")
	}
	if !strings.Contains(value, "://") {
		// 没有协议前缀时只允许「主机:端口」这一种写法。
		// 带 / 或 ? 的裸输入会被 https:// 前缀曲解成另一个地址
		// （h:8790/x 会变成 https://h:8790/x，用户以为的路径其实是主机的一部分）。
		// 宁可让他写全，也不要猜。
		if strings.ContainsAny(value, "/?") {
			return "", fmt.Errorf("请写完整的网关地址，形如 https://192.168.1.5:8790（当前是 %q）", raw)
		}
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("网关地址无法解析：%w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("网关地址 %q 里没有主机部分", raw)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("网关地址必须是 https（当前是 %s）："+
			"请求正文会经过局域网，明文等于把账号与对话内容一起播出去", parsed.Scheme)
	}
	return strings.TrimSuffix(parsed.Scheme+"://"+parsed.Host+parsed.Path, "/"), nil
}

// FetchCredential 拉一份网关凭证。它实现 credentialsync.Source，
// 因此同步器拿到的就是这条 TLS 通道。
//
// 网关未开放凭证同步时返回 403，错误信息由网关给足——
// 那是用户唯一需要照着做的一步（去控制台开开关）。
func (c *Client) Fetch(ctx context.Context) (credentialsync.Envelope, error) {
	request, err := c.newRequest(ctx, http.MethodGet, credentialPath, nil)
	if err != nil {
		return credentialsync.Envelope{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return credentialsync.Envelope{}, translateError(err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	if err != nil {
		return credentialsync.Envelope{}, translateError(err)
	}
	if response.StatusCode == http.StatusForbidden {
		return credentialsync.Envelope{}, errorsFromBody(response.StatusCode, raw)
	}
	if response.StatusCode != http.StatusOK {
		return credentialsync.Envelope{}, errorsFromBody(response.StatusCode, raw)
	}
	var envelope credentialsync.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return credentialsync.Envelope{}, fmt.Errorf("网关返回的凭证不是合法 JSON：%w", err)
	}
	return envelope, nil
}
