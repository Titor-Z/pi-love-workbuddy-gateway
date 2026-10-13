package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
)

// piPlaceholderKey 是写进 pi 配置的占位密钥。后端不吃这个值，
// 它只在网关启用了本地密钥校验时才需要换成真值。
const piPlaceholderKey = "workbuddy"

// piProviderName 是写进 pi 配置的 provider 名，pi 里用 <provider>/<model> 选中模型。
const piProviderName = "workbuddy"

// piSchemaURL 是 pi 的 models.json 结构声明地址。
const piSchemaURL = "https://pi.dev/schemas/models.schema.json"

// piModelsFile 是生成给 pi 的 models.json 结构。
type piModelsFile struct {
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

// piModel 是 pi 里的一个模型条目。
type piModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Reasoning     bool     `json:"reasoning,omitempty"`
	Input         []string `json:"input,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	MaxTokens     int      `json:"maxTokens,omitempty"`
}

// writeTable 打印给人看的模型清单。
func writeTable(out io.Writer, entries []catalog.Entry) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\t名称（含费用标记）\t上下文\t最大输出\t思考\t图片")
	for _, entry := range entries {
		fmt.Fprintf(writer, "%s\t%s\t%d\t%d\t%s\t%s\n",
			entry.Model.ID, entry.DisplayName(),
			entry.Model.MaxInputTokens, entry.Model.MaxOutputTokens,
			yesNo(entry.Model.SupportsThinking), yesNo(entry.HasImageInput()))
	}
	return writer.Flush()
}

// writePiModels 输出一份完整的 pi models.json。
// 模型名里带上费用标记，pi 的模型选择器会原样展示。
func writePiModels(out io.Writer, entries []catalog.Entry, baseURL, apiKey string) error {
	models := make([]piModel, 0, len(entries))
	for _, entry := range entries {
		models = append(models, piModel{
			ID:            entry.Model.ID,
			Name:          entry.DisplayName(),
			Reasoning:     entry.Model.SupportsThinking,
			Input:         inputKinds(entry),
			ContextWindow: entry.Model.MaxInputTokens,
			MaxTokens:     entry.Model.MaxOutputTokens,
		})
	}
	if apiKey == "" {
		apiKey = piPlaceholderKey
	}
	payload := piModelsFile{
		Schema: piSchemaURL,
		Providers: map[string]piProvider{
			piProviderName: {BaseURL: baseURL, API: "openai-completions", APIKey: apiKey, Models: models},
		},
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(payload)
}

// inputKinds 给出模型的输入类型列表。
func inputKinds(entry catalog.Entry) []string {
	if entry.HasImageInput() {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// yesNo 把布尔值渲染成中文标记，仅用于表格输出。
func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "-"
}

// piGatewayBaseURL 由监听地址推出 pi 该填的 baseUrl。
func piGatewayBaseURL(cfg *config.Config) string {
	addr := cfg.ListenAddr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr + "/v1"
}
