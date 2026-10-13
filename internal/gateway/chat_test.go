package gateway

import (
	"encoding/json"
	"testing"
)

// TestBuildUpstreamBodyFiltersUnknownFields 只放后端认得的字段过去。
// 客户端塞进来的未知参数可能触发后端安全策略，必须挡住。
func TestBuildUpstreamBodyFiltersUnknownFields(t *testing.T) {
	body := buildUpstreamBody(map[string]any{
		"model":           "glm-5.3",
		"messages":        []any{},
		"temperature":     json.Number("0.5"),
		"logprobs":        true, // 不在白名单
		"some_future_api": "x",  // 不在白名单
	})

	if _, ok := body["logprobs"]; ok {
		t.Error("logprobs 不在白名单，不该透传")
	}
	if _, ok := body["some_future_api"]; ok {
		t.Error("未知字段不该透传")
	}
	if got := body["model"]; got != "glm-5.3" {
		t.Errorf("model = %v，期望 glm-5.3", got)
	}
	if _, ok := body["temperature"]; !ok {
		t.Error("temperature 在白名单里，应当透传")
	}
}

// TestBuildUpstreamBodyForcesStream 后端只支持流式，
// 无论客户端要不要流式，发往后端的请求都必须是 stream=true。
func TestBuildUpstreamBodyForcesStream(t *testing.T) {
	for _, clientStream := range []any{false, true, nil} {
		body := buildUpstreamBody(map[string]any{"stream": clientStream})
		if body["stream"] != true {
			t.Errorf("客户端 stream=%v 时，上游 stream = %v，期望 true", clientStream, body["stream"])
		}
	}
}

// TestBuildUpstreamBodyStreamOptions 默认补上用量统计；
// 客户端自己指定了就不覆盖。
func TestBuildUpstreamBodyStreamOptions(t *testing.T) {
	body := buildUpstreamBody(map[string]any{"model": "auto"})
	options, ok := body["stream_options"].(map[string]any)
	if !ok || options["include_usage"] != true {
		t.Fatalf("应默认补上 stream_options.include_usage，实际 %v", body["stream_options"])
	}

	custom := map[string]any{"stream_options": map[string]any{"include_usage": false}}
	body = buildUpstreamBody(custom)
	options, _ = body["stream_options"].(map[string]any)
	if options["include_usage"] != false {
		t.Errorf("客户端指定的 stream_options 不该被覆盖，实际 %v", body["stream_options"])
	}
}

// TestBuildUpstreamBodyDefaultModel 客户端没给模型时兜底为 auto。
func TestBuildUpstreamBodyDefaultModel(t *testing.T) {
	if got := buildUpstreamBody(map[string]any{})["model"]; got != defaultModel {
		t.Errorf("model = %v，期望 %v", got, defaultModel)
	}
}

// TestBuildUpstreamBodyMapsDeveloperRole 后端不认 developer 角色，
// 遇到会触发安全策略拦截，必须映射成 system。
func TestBuildUpstreamBodyMapsDeveloperRole(t *testing.T) {
	original := map[string]any{
		"messages": []any{
			map[string]any{"role": "developer", "content": "规则"},
			map[string]any{"role": "user", "content": "问题"},
		},
	}
	body := buildUpstreamBody(original)

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages 结构不对：%v", body["messages"])
	}
	if role := messages[0].(map[string]any)["role"]; role != "system" {
		t.Errorf("developer 应被改写成 system，实际 %v", role)
	}
	if role := messages[1].(map[string]any)["role"]; role != "user" {
		t.Errorf("user 不该被动，实际 %v", role)
	}
	// 原请求体不能被就地改动，否则重试时会带上已被改写的历史。
	if role := original["messages"].([]any)[0].(map[string]any)["role"]; role != "developer" {
		t.Errorf("输入请求体被就地改动了，role = %v", role)
	}
}

// TestNormalizeRolesIgnoresMalformed 非对象的消息项要原样保留，
// 交给后端去报错，而不是让网关 panic。
func TestNormalizeRolesIgnoresMalformed(t *testing.T) {
	got := normalizeRoles([]any{"裸字符串", nil, map[string]any{"role": "developer"}})
	if len(got) != 3 {
		t.Fatalf("条目数 = %d，期望 3", len(got))
	}
	if got[0] != "裸字符串" || got[1] != nil {
		t.Error("非对象项应原样保留")
	}
	if got[2].(map[string]any)["role"] != "system" {
		t.Error("裸 developer 对象应被改写成 system")
	}
}

// TestHasMessages 空数组与缺字段都不算有消息。
func TestHasMessages(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want bool
	}{
		{"正常", map[string]any{"messages": []any{map[string]any{}}}, true},
		{"空数组", map[string]any{"messages": []any{}}, false},
		{"缺字段", map[string]any{}, false},
		{"类型不对", map[string]any{"messages": "字符串"}, false},
	}
	for _, testCase := range cases {
		if got := hasMessages(testCase.body); got != testCase.want {
			t.Errorf("%s：hasMessages = %v，期望 %v", testCase.name, got, testCase.want)
		}
	}
}

// TestWantsStream 只有明确的布尔 true 才算要流式。
func TestWantsStream(t *testing.T) {
	cases := []struct {
		body map[string]any
		want bool
	}{
		{map[string]any{"stream": true}, true},
		{map[string]any{"stream": false}, false},
		{map[string]any{}, false},
		{map[string]any{"stream": "true"}, false},
	}
	for _, testCase := range cases {
		if got := wantsStream(testCase.body); got != testCase.want {
			t.Errorf("wantsStream(%v) = %v，期望 %v", testCase.body, got, testCase.want)
		}
	}
}

// TestStringField 类型不符时返回空串而不是 panic。
func TestStringField(t *testing.T) {
	body := map[string]any{"a": "文本", "b": 1, "c": nil}
	if got := stringField(body, "a"); got != "文本" {
		t.Errorf("a = %q", got)
	}
	for _, key := range []string{"b", "c", "missing"} {
		if got := stringField(body, key); got != "" {
			t.Errorf("%s = %q，期望空串", key, got)
		}
	}
}
