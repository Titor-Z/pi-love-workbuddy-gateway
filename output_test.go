package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// TestPiGatewayBaseURL 由监听地址推出 pi 该填的地址。
// 只写端口号时也要能补出主机名，否则生成的配置没法用。
func TestPiGatewayBaseURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8788": "http://127.0.0.1:8788/v1",
		":8788":          "http://127.0.0.1:8788/v1",
		"0.0.0.0:9000":   "http://0.0.0.0:9000/v1",
	}
	for addr, want := range cases {
		if got := piGatewayBaseURL(&config.Config{ListenAddr: addr}); got != want {
			t.Errorf("监听 %s：got %q，期望 %q", addr, got, want)
		}
	}
}

// TestWritePiModels 生成的 models.json 要能被 pi 直接读，
// 且模型名带上费用标记、免费模型不冒出倍率。
func TestWritePiModels(t *testing.T) {
	entries := catalog.Build([]upstream.ModelInfo{
		{ID: "hy3", Name: "Hy3", Credits: "x0.00 credits",
			MaxInputTokens: 192000, MaxOutputTokens: 64000,
			SupportsImages: true, SupportsThinking: true},
		{ID: "glm-5.1", Name: "GLM-5.1", Credits: "x0.79 credits",
			MaxInputTokens: 200000, MaxOutputTokens: 48000,
			SupportsImages: false, SupportsThinking: true},
	}, nil)

	var out bytes.Buffer
	if err := catalog.WritePiConfig(&out, entries, "http://127.0.0.1:8788/v1", ""); err != nil {
		t.Fatalf("生成配置失败：%v", err)
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
		t.Fatalf("生成的配置不是合法 JSON：%v", err)
	}
	if payload.Schema == "" {
		t.Error("缺少 $schema")
	}

	provider, ok := payload.Providers[catalog.PiProviderName]
	if !ok {
		t.Fatalf("缺少 provider %s", catalog.PiProviderName)
	}
	if provider.API != "openai-completions" {
		t.Errorf("api = %q", provider.API)
	}
	if provider.APIKey != catalog.PiPlaceholderKey {
		t.Errorf("未指定密钥时应填占位值，实际 %q", provider.APIKey)
	}
	if provider.BaseURL != "http://127.0.0.1:8788/v1" {
		t.Errorf("baseUrl = %q", provider.BaseURL)
	}
	if len(provider.Models) != 2 {
		t.Fatalf("模型数 = %d，期望 2", len(provider.Models))
	}

	free := provider.Models[0]
	if free.Name != "Hy3 · 免费" {
		t.Errorf("免费模型展示名 = %q", free.Name)
	}
	if len(free.Input) != 2 || free.Input[1] != "image" {
		t.Errorf("支持图片的模型应声明 image 输入：%v", free.Input)
	}
	if free.ContextWindow != 192000 || free.MaxTokens != 64000 {
		t.Errorf("窗口参数不对：%d / %d", free.ContextWindow, free.MaxTokens)
	}

	paid := provider.Models[1]
	if paid.Name != "GLM-5.1 · 0.79x" {
		t.Errorf("收费模型展示名 = %q", paid.Name)
	}
	if len(paid.Input) != 1 || paid.Input[0] != "text" {
		t.Errorf("不支持图片的模型只该声明 text：%v", paid.Input)
	}
}

// TestHasVersionFlag 版本号开关的两种写法都要认。
func TestHasVersionFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{}, false},
		{[]string{"models"}, false},
		{[]string{"-version"}, true},
		{[]string{"--version"}, true},
		{[]string{"serve", "--version"}, true},
		{[]string{"-versioning"}, false},
	}
	for _, testCase := range cases {
		if got := hasVersionFlag(testCase.args); got != testCase.want {
			t.Errorf("hasVersionFlag(%v) = %v，期望 %v", testCase.args, got, testCase.want)
		}
	}
}

// TestResolvePromotionsPath 显式路径优先，否则回退到默认位置。
func TestResolvePromotionsPath(t *testing.T) {
	explicit := &config.Config{Promotions: "/tmp/custom.json"}
	if got := resolvePromotionsPath(explicit); got != "/tmp/custom.json" {
		t.Errorf("got %q，期望采用显式路径", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-root")
	if got, want := resolvePromotionsPath(&config.Config{}), "/tmp/xdg-root/piloveworkbuddy/promotions.json"; got != want {
		t.Errorf("got %q，期望 %q", got, want)
	}
}

// TestWriteTable 表格至少要包含每个模型，且不报错。
func TestWriteTable(t *testing.T) {
	entries := catalog.Build([]upstream.ModelInfo{
		{ID: "hy3", Name: "Hy3", Credits: "x0.00 credits", SupportsImages: true},
	}, nil)

	var out bytes.Buffer
	if err := writeTable(&out, entries); err != nil {
		t.Fatalf("打印表格失败：%v", err)
	}
	for _, want := range []string{"ID", "hy3", "Hy3 · 免费", "是"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("表格里缺少 %q：\n%s", want, out.String())
		}
	}
}
