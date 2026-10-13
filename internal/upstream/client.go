// Package upstream 封装对 WorkBuddy 国内版后端的调用。
//
// 只覆盖本项目需要的三个接口：对话补全、访问令牌刷新（由 credential 包发起）、
// 以及实时模型目录。所有请求头都由 credential.Manager 提供，
// 因此调用方不需要关心令牌从哪来、是否过期。
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/wbapi"
)

// metaTimeout 是拉取模型目录等元信息接口的超时时间。
const metaTimeout = 30 * time.Second

// nonChatTags 标记非对话模型，列出模型时会被过滤掉。
var nonChatTags = map[string]bool{
	"text-to-image":  true,
	"image-to-image": true,
	"text-to-video":  true,
	"image-to-video": true,
}

// Client 是后端客户端。对话接口用于流式透传，因此不设整体超时，由调用方的 ctx 控制。
type Client struct {
	streamHTTP *http.Client
	metaHTTP   *http.Client
	cred       *credential.Manager
	baseURL    string // 后端根地址，留空表示用官方地址
}

// NewClient 构造后端客户端。baseURL 留空时指向官方后端；
// 传入自定义地址可以让测试或自建代理接管。
func NewClient(cred *credential.Manager, baseURL string) *Client {
	if baseURL == "" {
		baseURL = wbapi.BaseURL
	}
	return &Client{
		streamHTTP: &http.Client{},
		metaHTTP:   &http.Client{Timeout: metaTimeout},
		cred:       cred,
		baseURL:    strings.TrimSuffix(baseURL, "/"),
	}
}

// ChatStream 发起对话补全请求，返回尚未读取的原始响应。
// 由调用方决定是原样透传给客户端，还是聚合成非流式响应。
func (c *Client) ChatStream(ctx context.Context, body []byte) (*http.Response, error) {
	headers, err := c.cred.Headers()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+wbapi.ChatPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造补全请求失败：%w", err)
	}
	applyHeaders(req, headers)
	return c.streamHTTP.Do(req)
}

// ModelInfo 是模型目录里我们关心的字段。
type ModelInfo struct {
	ID               string   // 模型标识，即请求体里 model 字段该填的值
	Name             string   // 展示名
	Credits          string   // 计费倍率，形如 "x0.29"
	MaxInputTokens   int      // 上下文窗口
	MaxOutputTokens  int      // 单次最大输出
	SupportsImages   bool     // 是否支持图片输入
	SupportsToolCall bool     // 是否支持工具调用
	SupportsThinking bool     // 是否支持思考（推理）模式
	Efforts          []string // 可选的思考档位
	Tags             []string // 后端打的标签，含促销标记
}

// Models 拉取实时模型目录。这个接口返回的目录比 WorkBuddy 客户端本地的
// product.json 新得多，是本项目唯一的模型来源。
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	headers, err := c.cred.Headers()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+wbapi.ModelsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("构造模型目录请求失败：%w", err)
	}
	applyHeaders(req, headers)

	resp, err := c.metaHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取模型目录失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取模型目录返回 HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Models []rawModel `json:"models"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("模型目录不是合法 JSON：%w", err)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("模型目录被拒绝：%s", payload.Msg)
	}

	models := make([]ModelInfo, 0, len(payload.Data.Models))
	for _, item := range payload.Data.Models {
		if item.ID == "" || item.isNonChat() {
			continue
		}
		models = append(models, item.toInfo())
	}
	return models, nil
}

// applyHeaders 把请求头写入请求；空值跳过，避免覆盖 http 包自己的默认值。
func applyHeaders(req *http.Request, headers map[string]string) {
	for name, value := range headers {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
}

// rawModel 是模型目录接口的原始行结构。
type rawModel struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Credits          string          `json:"credits"`
	MaxInputTokens   int             `json:"maxInputTokens"`
	MaxOutputTokens  int             `json:"maxOutputTokens"`
	SupportsImages   bool            `json:"supportsImages"`
	SupportsToolCall bool            `json:"supportsToolCall"`
	SupportsThinking bool            `json:"supportsReasoning"`
	Tags             []string        `json:"tags"`
	Reasoning        json.RawMessage `json:"reasoning"`
}

// isNonChat 判断该行是否为图片、视频等非对话模型。
func (m rawModel) isNonChat() bool {
	for _, tag := range m.Tags {
		if nonChatTags[tag] {
			return true
		}
	}
	return false
}

// toInfo 把原始行转换成本项目使用的模型信息。
func (m rawModel) toInfo() ModelInfo {
	return ModelInfo{
		ID:               m.ID,
		Name:             m.Name,
		Credits:          m.Credits,
		MaxInputTokens:   m.MaxInputTokens,
		MaxOutputTokens:  m.MaxOutputTokens,
		SupportsImages:   m.SupportsImages,
		SupportsToolCall: m.SupportsToolCall,
		SupportsThinking: m.SupportsThinking,
		Efforts:          parseEfforts(m.Reasoning),
		Tags:             m.Tags,
	}
}

// parseEfforts 从 reasoning 字段里取出可选思考档位。
// 后端有两种写法：直接给字符串（默认档位），或给对象（含 supportedEfforts 数组）。
func parseEfforts(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var asText string
	if err := json.Unmarshal(raw, &asText); err == nil {
		return nil // 只有默认档位，不构成可选集合
	}
	var asObject struct {
		SupportedEfforts []string `json:"supportedEfforts"`
	}
	if err := json.Unmarshal(raw, &asObject); err != nil {
		return nil
	}
	return asObject.SupportedEfforts
}
