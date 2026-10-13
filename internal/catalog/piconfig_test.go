package catalog

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// sampleEntries 造一组覆盖各种能力组合的模型，用来检查生成的配置。
func sampleEntries() []Entry {
	return []Entry{
		{
			Model: upstream.ModelInfo{
				ID: "glm-5.3", Name: "GLM-5.3", MaxInputTokens: 1000000,
				MaxOutputTokens: 64000, SupportsThinking: true, Efforts: []string{"low", "high"},
			},
			Label: Label{Credits: "0.79x", FreeUntil: "2026-10-31"},
		},
		{
			Model: upstream.ModelInfo{
				ID: "hy3", Name: "Hy3", MaxInputTokens: 192000,
				MaxOutputTokens: 64000, SupportsThinking: true,
				SupportsImages: true,
			},
			Label: Label{Free: true, Badge: "限时免费"},
		},
	}
}

// TestWritePiConfig 覆盖生成的配置结构。
func TestWritePiConfig(t *testing.T) {
	var out bytes.Buffer
	if err := WritePiConfig(&out, sampleEntries(), "http://127.0.0.1:8788/v1", ""); err != nil {
		t.Fatalf("生成失败：%v", err)
	}

	var payload struct {
		Schema    string `json:"$schema"`
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID            string   `json:"id"`
				Name          string   `json:"name"`
				Reasoning     bool     `json:"reasoning"`
				Input         []string `json:"input"`
				ContextWindow int      `json:"contextWindow"`
				MaxTokens     int      `json:"maxTokens"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("生成的不是合法 JSON：%v\n%s", err, out.String())
	}

	if payload.Schema != piSchemaURL {
		t.Errorf("$schema = %q，想要 %q", payload.Schema, piSchemaURL)
	}
	provider, found := payload.Providers[PiProviderName]
	if !found {
		t.Fatalf("没有 %q 这个 provider：%s", PiProviderName, out.String())
	}
	if provider.BaseURL != "http://127.0.0.1:8788/v1" {
		t.Errorf("baseUrl = %q", provider.BaseURL)
	}
	if provider.API != "openai-completions" {
		t.Errorf("api = %q，想要 openai-completions", provider.API)
	}
	// 没给密钥时要填占位值：空字符串会让 pi 直接拒绝加载配置。
	if provider.APIKey != PiPlaceholderKey {
		t.Errorf("apiKey = %q，想要占位值 %q", provider.APIKey, PiPlaceholderKey)
	}

	if len(provider.Models) != 2 {
		t.Fatalf("模型数 = %d，想要 2", len(provider.Models))
	}

	glm := provider.Models[0]
	if glm.ID != "glm-5.3" || glm.ContextWindow != 1000000 || glm.MaxTokens != 64000 {
		t.Errorf("glm-5.3 字段不对：%+v", glm)
	}
	if !glm.Reasoning {
		t.Error("glm-5.3 支持思考，reasoning 应为 true")
	}
	// 模型名要带上费用标记，pi 的选择器会原样展示。
	if !strings.Contains(glm.Name, "0.79x") {
		t.Errorf("glm-5.3 的 name 里没有倍率：%q", glm.Name)
	}

	hy3 := provider.Models[1]
	if lines := hy3.Input; len(lines) != 2 || lines[1] != "image" {
		t.Errorf("hy3 支持图片输入，input 应为 text+image：%v", lines)
	}
	if !strings.Contains(hy3.Name, "免费") {
		t.Errorf("hy3 的 name 里没有免费标记：%q", hy3.Name)
	}
}

// TestWritePiConfigUsesGivenKey 确认显式给了密钥时不覆盖成占位值。
func TestWritePiConfigUsesGivenKey(t *testing.T) {
	var out bytes.Buffer
	if err := WritePiConfig(&out, nil, "http://127.0.0.1:8788/v1", "real-key"); err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if !strings.Contains(out.String(), `"apiKey": "real-key"`) {
		t.Fatalf("没有使用给定的密钥：%s", out.String())
	}
}

// TestWritePiConfigEmpty 确认没有模型时输出的是空数组而不是 null——
// 前者 pi 能读，后者会报类型错。
func TestWritePiConfigEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := WritePiConfig(&out, nil, "http://127.0.0.1:8788/v1", ""); err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if !strings.Contains(out.String(), `"models": []`) {
		t.Fatalf("空目录应输出空数组：%s", out.String())
	}
}

// TestWritePiConfigEscapes 确认模型名里的中文与符号不被转义成 \u 序列：
// 这份文件是给人看的，转义之后 grep 找不到。
func TestWritePiConfigEscapes(t *testing.T) {
	entries := []Entry{{
		Model: upstream.ModelInfo{ID: "a<b>&c"},
		Label: Label{Free: true},
	}}
	var out bytes.Buffer
	if err := WritePiConfig(&out, entries, "http://x/v1", ""); err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	if strings.Contains(out.String(), `\u003c`) {
		t.Fatalf("尖括号被转义了：%s", out.String())
	}
	if !strings.Contains(out.String(), "a<b>&c") {
		t.Fatalf("模型 id 没有原样输出：%s", out.String())
	}
}
