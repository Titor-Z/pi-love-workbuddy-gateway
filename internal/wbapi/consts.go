// Package wbapi 汇总 WorkBuddy 国内版后端的固定常量，供各模块共用，
// 避免调用方各自硬编码地址而出现不一致。
package wbapi

const (
	// BaseURL 是 WorkBuddy 国内版后端根地址。
	BaseURL = "https://copilot.tencent.com"
	// DefaultDomain 是登录态里没写 domain 时的兜底域名。
	DefaultDomain = "www.codebuddy.cn"
	// UserAgent 沿用参考实现使用的标识，避免更换后后端行为发生变化。
	UserAgent = "codebuddy2openai/2.0"

	// ChatPath 是对话补全路径。后端本身就是标准 OpenAI SSE，可直接字节透传。
	ChatPath = "/v2/chat/completions"
	// RefreshPath 是访问令牌刷新路径。
	RefreshPath = "/v2/plugin/auth/token/refresh"
	// ModelsPath 是模型目录路径。后端返回的是实时目录（含倍率与促销标记），
	// 比 WorkBuddy 客户端本地的 product.json 新得多。
	ModelsPath = "/console/enterprises/personal/models"
)
