package upstream

import (
	"encoding/json"
	"testing"
)

// TestParseEfforts reasoning 字段有两种写法：
// 字符串（只有默认档位）和对象（含可选档位集合）。搞混会导致
// pi 里凭空多出或丢失思考档位选择。
func TestParseEfforts(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"字符串写法没有可选集合", `"high"`, nil},
		{"对象写法取 supportedEfforts",
			`{"defaultEffort":"high","supportedEfforts":["low","high","max"]}`,
			[]string{"low", "high", "max"}},
		{"对象但没有 supportedEfforts", `{"defaultEffort":"medium"}`, nil},
		{"supportedEfforts 为空", `{"supportedEfforts":[]}`, nil},
		{"空字段", ``, nil},
		{"垃圾内容", `12345`, nil},
	}
	for _, testCase := range cases {
		got := parseEfforts(json.RawMessage(testCase.raw))
		if len(got) != len(testCase.want) {
			t.Errorf("%s：got %v，期望 %v", testCase.name, got, testCase.want)
			continue
		}
		for i := range got {
			if got[i] != testCase.want[i] {
				t.Errorf("%s：got %v，期望 %v", testCase.name, got, testCase.want)
				break
			}
		}
	}
}

// TestRawModelIsNonChat 图片、视频模型必须被过滤掉，
// 否则 pi 的模型列表里会混进根本没法对话的条目。
func TestRawModelIsNonChat(t *testing.T) {
	cases := []struct {
		name  string
		tags  []string
		nonCh bool
	}{
		{"对话模型", []string{"craft"}, false},
		{"没有标签", nil, false},
		{"文生图", []string{"text-to-image"}, true},
		{"图生图", []string{"image-to-image"}, true},
		{"文生视频", []string{"text-to-video"}, true},
		{"图生视频", []string{"image-to-video"}, true},
		{"混合标签", []string{"craft", "text-to-video"}, true},
	}
	for _, testCase := range cases {
		model := rawModel{ID: "m", Tags: testCase.tags}
		if got := model.isNonChat(); got != testCase.nonCh {
			t.Errorf("%s：isNonChat = %v，期望 %v", testCase.name, got, testCase.nonCh)
		}
	}
}

// TestRawModelToInfo 字段映射要一一对上，尤其别把
// supportsReasoning 与 reasoning 搞混（前者是布尔，后者是档位集合）。
func TestRawModelToInfo(t *testing.T) {
	model := rawModel{
		ID:               "glm-5.3",
		Name:             "GLM-5.3",
		Credits:          "x0.79",
		MaxInputTokens:   1000000,
		MaxOutputTokens:  64000,
		SupportsImages:   true,
		SupportsToolCall: true,
		SupportsThinking: true,
		Tags:             []string{"craft", "badge:夜间免费:#FF0000"},
		Reasoning:        json.RawMessage(`{"supportedEfforts":["low","high","max"]}`),
	}
	info := model.toInfo()

	if info.ID != "glm-5.3" || info.Name != "GLM-5.3" || info.Credits != "x0.79" {
		t.Errorf("基础字段不对：%+v", info)
	}
	if info.MaxInputTokens != 1000000 || info.MaxOutputTokens != 64000 {
		t.Errorf("窗口参数不对：%+v", info)
	}
	if !info.SupportsImages || !info.SupportsToolCall || !info.SupportsThinking {
		t.Errorf("能力标志不对：%+v", info)
	}
	if len(info.Efforts) != 3 || len(info.Tags) != 2 {
		t.Errorf("集合字段不对：%+v", info)
	}
}

// TestParseEffortsIsIndependentOfThinkingFlag 没有 supportedEfforts 时，
// 即便模型支持思考，也不该凭空造出档位。
func TestParseEffortsIsIndependentOfThinkingFlag(t *testing.T) {
	model := rawModel{
		ID:               "glm-5.2",
		SupportsThinking: true,
		Reasoning:        json.RawMessage(`{"defaultEffort":"medium"}`),
	}
	info := model.toInfo()
	if !info.SupportsThinking {
		t.Error("SupportsThinking 应当为真")
	}
	if info.Efforts != nil {
		t.Errorf("没有声明可选档位时不该造出档位：%v", info.Efforts)
	}
}
