package catalog

import (
	"encoding/json"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// TestThinkingLevelMapHasEveryKey 钉死一条 pi 的隐含语义：
// **省略的键等于"支持"**——pi-ai 的 getSupportedThinkingLevels 只看
// `map[level] === null` 才排除该档位。所以「只写支持的档位」是不够的，
// 省略掉的 off 仍是可用档位，用户选它时 pi 会什么都不发。
//
// 这一条是实测踩出来的：后端 `glm-5.1` 恰恰**不发 reasoning_effort 就 400**
// （code 11150），而 `pi --model glm-5.1 --thinking off` 当时就是这么挂的。
func TestThinkingLevelMapHasEveryKey(t *testing.T) {
	want := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	cases := []struct {
		name  string
		model upstream.ModelInfo
	}{
		{"声明了可选档位", upstream.ModelInfo{SupportsThinking: true, Efforts: []string{"low", "high", "max"}, DefaultEffort: "high"}},
		{"只有默认档位", upstream.ModelInfo{SupportsThinking: true, DefaultEffort: "medium"}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			mapped := ThinkingLevelMap(item.model, nil)
			if len(mapped) != len(want) {
				t.Fatalf("映射必须七个键齐备（省略等于支持），得到 %d 个：%v", len(mapped), mapped)
			}
			for _, level := range want {
				if _, ok := mapped[level]; !ok {
					t.Errorf("缺少档位 %q", level)
				}
			}
			// off / minimal 永远是 null：我们从不主动发这两个拼写。
			for _, level := range []string{"off", "minimal"} {
				if mapped[level] != nil {
					t.Errorf("%s 必须是 null，得到 %v", level, *mapped[level])
				}
			}
			// 至少有一个非 null，否则 pi 会 clamp 成 off 而什么都不发。
			if !hasAnyValue(mapped) {
				t.Error("映射不能全是 null，否则 pi 会不发 reasoning_effort")
			}
		})
	}
}

// TestThinkingLevelMapOnlyDeclared 只看后端声明过的档位才出现。
func TestThinkingLevelMapOnlyDeclared(t *testing.T) {
	mapped := ThinkingLevelMap(upstream.ModelInfo{
		SupportsThinking: true, Efforts: []string{"low", "high", "max"}, DefaultEffort: "high",
	}, nil)
	for level, want := range map[string]bool{"low": true, "high": true, "max": true, "medium": false, "xhigh": false} {
		got := mapped[level] != nil
		if got != want {
			t.Errorf("%s 出现与否应为 %v，得到 %v", level, want, got)
		}
	}
}

// TestThinkingLevelMapDefaultOnly 没有 supportedEfforts 时，
// 只映射默认档位——后端不校验，但要求你必须发一个。
func TestThinkingLevelMapDefaultOnly(t *testing.T) {
	mapped := ThinkingLevelMap(upstream.ModelInfo{SupportsThinking: true, DefaultEffort: "medium"}, nil)
	if mapped["medium"] == nil || *mapped["medium"] != "medium" {
		t.Fatalf("默认档位应映射成自身，得到 %v", mapped["medium"])
	}
	if mapped["high"] != nil {
		t.Errorf("未声明的档位应为 null，得到 %v", *mapped["high"])
	}
}

// TestThinkingLevelMapUnsupported 不支持思考的模型不给映射，交给 reasoning:false。
func TestThinkingLevelMapUnsupported(t *testing.T) {
	if mapped := ThinkingLevelMap(upstream.ModelInfo{}, nil); mapped != nil {
		t.Errorf("不支持思考时不该给映射：%v", mapped)
	}
}

// TestThinkingLevelMapOverrideWins 用户覆盖表优先，且是逐键合并。
func TestThinkingLevelMapOverrideWins(t *testing.T) {
	model := upstream.ModelInfo{SupportsThinking: true, Efforts: []string{"low", "high", "max"}, DefaultEffort: "high"}
	// 用户只想关掉 max，其余档位应继续用推导结果。
	off := "off"
	override := map[string]*string{"max": nil, "high": &off}
	mapped := ThinkingLevelMap(model, override)
	if mapped["max"] != nil {
		t.Error("用户关掉的 max 必须保持 null")
	}
	if mapped["high"] == nil || *mapped["high"] != "off" {
		t.Errorf("用户值必须生效，得到 %v", mapped["high"])
	}
	if mapped["low"] == nil {
		t.Error("用户没动的档位应继续用推导结果")
	}
}

// TestThinkingLevelMapAllNullFallsBack 用户把所有档位都关掉时退回推导结果——
// 全 null 会让 pi clamp 成 off 而什么都不发，正是我们要避免的那个 400。
func TestThinkingLevelMapAllNullFallsBack(t *testing.T) {
	model := upstream.ModelInfo{SupportsThinking: true, Efforts: []string{"low", "high"}, DefaultEffort: "high"}
	override := map[string]*string{}
	for _, level := range piThinkingLevels {
		override[level] = nil
	}
	mapped := ThinkingLevelMap(model, override)
	if !hasAnyValue(mapped) {
		t.Fatal("用户把档位全关掉时不该产生全 null 映射")
	}
	if mapped["high"] == nil {
		t.Error("应退回推导结果，high 应当可用")
	}
}

// TestWritePiConfigEmitsThinkingMap 生成的配置里每个支持思考的模型都带映射。
// 缺了它 pi 会走「off 就是不发送」那条路，后端 glm-5.1 直接 400。
func TestWritePiConfigEmitsThinkingMap(t *testing.T) {
	entries := Build([]upstream.ModelInfo{
		{ID: "glm-5.1", Name: "GLM-5.1", SupportsThinking: true, DefaultEffort: "medium", MaxInputTokens: 200000, MaxOutputTokens: 48000},
		{ID: "chat-only", Name: "Chat", MaxInputTokens: 8000, MaxOutputTokens: 1000},
	}, NewOverrides())

	var out bytesBuffer
	if err := WritePiConfig(&out, entries, "http://127.0.0.1:8788/v1", ""); err != nil {
		t.Fatalf("生成失败：%v", err)
	}

	var payload struct {
		Providers map[string]struct {
			Models []struct {
				ID               string             `json:"id"`
				Reasoning        bool               `json:"reasoning"`
				ThinkingLevelMap map[string]*string `json:"thinkingLevelMap"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("生成的配置不是合法 JSON：%v", err)
	}

	for _, model := range payload.Providers[PiProviderName].Models {
		switch model.ID {
		case "glm-5.1":
			if len(model.ThinkingLevelMap) != len(piThinkingLevels) {
				t.Fatalf("支持思考的模型必须带完整映射，得到 %v", model.ThinkingLevelMap)
			}
			if model.ThinkingLevelMap["medium"] == nil {
				t.Error("默认档位应当可用")
			}
		case "chat-only":
			if model.Reasoning || model.ThinkingLevelMap != nil {
				t.Error("不支持思考的模型不该带 reasoning 或映射")
			}
		}
	}
}

func hasAnyValue(mapped map[string]*string) bool {
	for _, value := range mapped {
		if value != nil {
			return true
		}
	}
	return false
}

// bytesBuffer 记录写入的字节，供测试断言。
type bytesBuffer struct{ data []byte }

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *bytesBuffer) Bytes() []byte { return b.data }
