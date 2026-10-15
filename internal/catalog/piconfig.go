package catalog

import (
	"encoding/json"
	"io"
)

const (
	// PiProviderName 是写进 pi 配置的 provider 名，pi 里用 <provider>/<model> 选中模型。
	PiProviderName = "workbuddy"
	// PiPlaceholderKey 是写进 pi 配置的占位密钥。后端不吃这个值，
	// 它只在网关启用了本地密钥校验时才需要换成真值。
	PiPlaceholderKey = "workbuddy"
	// piSchemaURL 是 pi 的 models.json 结构声明地址。
	piSchemaURL = "https://pi.dev/schemas/models.schema.json"
)

// piConfigFile 是生成给 pi 的 models.json 结构。
type piConfigFile struct {
	Schema    string                `json:"$schema"`
	Providers map[string]piProvider `json:"providers"`
}

// piProvider 是 pi 里的一个模型提供方。
type piProvider struct {
	BaseURL string    `json:"baseUrl"`
	API     string    `json:"api"`
	APIKey  string    `json:"apiKey"`
	Models  []piModel `json:"models"`
}

// piModel 是 pi 里的一个模型条目。name 里带着费用标记，
// pi 的模型选择器会原样展示，用户一眼能看出哪个免费。
type piModel struct {
	ID               string             `json:"id"`
	Name             string             `json:"name,omitempty"`
	Reasoning        bool               `json:"reasoning,omitempty"`
	Input            []string           `json:"input,omitempty"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap,omitempty"`
	ContextWindow    int                `json:"contextWindow,omitempty"`
	MaxTokens        int                `json:"maxTokens,omitempty"`
}

// WritePiConfig 输出一份可直接重定向到 ~/.pi/agent/models.json 的配置。
func WritePiConfig(out io.Writer, entries []Entry, baseURL, apiKey string) error {
	models := make([]piModel, 0, len(entries))
	for _, entry := range entries {
		models = append(models, piModel{
			ID:               entry.Model.ID,
			Name:             entry.DisplayName(),
			Reasoning:        entry.Model.SupportsThinking,
			Input:            piInputKinds(entry),
			ThinkingLevelMap: entry.Thinking,
			ContextWindow:    entry.Model.MaxInputTokens,
			MaxTokens:        entry.Model.MaxOutputTokens,
		})
	}
	if apiKey == "" {
		apiKey = PiPlaceholderKey
	}
	payload := piConfigFile{
		Schema: piSchemaURL,
		Providers: map[string]piProvider{
			PiProviderName: {BaseURL: baseURL, API: "openai-completions", APIKey: apiKey, Models: models},
		},
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(payload)
}

// piInputKinds 给出模型的输入类型列表。
func piInputKinds(entry Entry) []string {
	if entry.HasImageInput() {
		return []string{"text", "image"}
	}
	return []string{"text"}
}
