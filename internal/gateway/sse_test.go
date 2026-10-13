package gateway

import (
	"strings"
	"testing"
)

// TestAggregateStreamText 把若干文本分片拼成一条完整回复。
func TestAggregateStreamText(t *testing.T) {
	result, err := aggregateStream(strings.NewReader(sse(
		chunk(`{"model":"glm-5.3","choices":[{"delta":{"content":"你"}}]}`),
		chunk(`{"model":"glm-5.3","choices":[{"delta":{"content":"好"}}]}`),
		chunk(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		"data: [DONE]",
		": 这是注释行，应当被忽略",
		"",
	)))
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	if got := message(t, result)["content"]; got != "你好" {
		t.Errorf("content = %v，期望 你好", got)
	}
	if got := result["model"]; got != "glm-5.3" {
		t.Errorf("model = %v", got)
	}
	if got := choice(t, result)["finish_reason"]; got != "stop" {
		t.Errorf("finish_reason = %v，期望 stop", got)
	}
}

// TestAggregateStreamToolCalls 工具调用的参数是分片下发的，
// 必须按索引拼回一个完整调用。这是 agent 场景能不能用的关键。
func TestAggregateStreamToolCalls(t *testing.T) {
	result, err := aggregateStream(strings.NewReader(sse(
		chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":"{\"ci"}}]}}]}`),
		chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"北京\"}"}}]}}]}`),
		chunk(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]",
	)))
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}

	calls, ok := message(t, result)["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls 结构不对：%v", message(t, result)["tool_calls"])
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call_1" || call["type"] != "function" {
		t.Errorf("调用元信息不对：%v", call)
	}
	function := call["function"].(map[string]any)
	if function["name"] != "get_weather" {
		t.Errorf("函数名 = %v", function["name"])
	}
	if function["arguments"] != `{"city":"北京"}` {
		t.Errorf("参数分片没拼对：%v", function["arguments"])
	}
}

// TestAggregateStreamToolCallsKeepsIndexOrder 多个工具调用要按索引顺序输出，
// 不能受 map 遍历顺序影响。
func TestAggregateStreamToolCallsKeepsIndexOrder(t *testing.T) {
	result, err := aggregateStream(strings.NewReader(sse(
		chunk(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"second"}}]}}]}`),
		chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"first"}}]}}]}`),
	)))
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	calls := message(t, result)["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("调用数 = %d，期望 2", len(calls))
	}
	first := calls[0].(map[string]any)["function"].(map[string]any)["name"]
	if first != "first" {
		t.Errorf("第一个调用应是 first，实际 %v", first)
	}
}

// TestAggregateStreamInfersToolCallsFinish 后端没给 finish_reason 时，
// 只要有工具调用就应推断为 tool_calls，否则客户端不会去执行工具。
func TestAggregateStreamInfersToolCallsFinish(t *testing.T) {
	result, err := aggregateStream(strings.NewReader(sse(
		chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"f"}}]}}]}`),
	)))
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	if got := choice(t, result)["finish_reason"]; got != "tool_calls" {
		t.Errorf("finish_reason = %v，期望 tool_calls", got)
	}
}

// TestAggregateStreamUsage 用量取最后出现的那一份；完全没有时补零，
// 避免客户端解析 null 出错。
func TestAggregateStreamUsage(t *testing.T) {
	result, err := aggregateStream(strings.NewReader(sse(
		chunk(`{"choices":[],"usage":{"total_tokens":10}}`),
		chunk(`{"choices":[],"usage":{"total_tokens":42}}`),
	)))
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	usage := result["usage"].(map[string]any)
	if usage["total_tokens"] != float64(42) {
		t.Errorf("total_tokens = %v，期望 42", usage["total_tokens"])
	}

	result, err = aggregateStream(strings.NewReader(sse(chunk(`{"choices":[]}`))))
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	usage = result["usage"].(map[string]any)
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if _, ok := usage[key]; !ok {
			t.Errorf("缺少用量字段 %s", key)
		}
	}
}

// TestAggregateStreamEmptyContentIsNull 没有正文时 content 应为 null，
// 与后端对「无正文」的表示保持一致。
func TestAggregateStreamEmptyContentIsNull(t *testing.T) {
	result, err := aggregateStream(strings.NewReader(sse(
		chunk(`{"choices":[{"delta":{"content":""}}]}`),
	)))
	if err != nil {
		t.Fatalf("聚合失败：%v", err)
	}
	if got := message(t, result)["content"]; got != nil {
		t.Errorf("content = %v，期望 null", got)
	}
}

// TestAggregateStreamToleratesGarbage 坏行不能让整个请求失败，
// 否则一个无关的畸形分片就会毁掉整次对话。
func TestAggregateStreamToleratesGarbage(t *testing.T) {
	result, err := aggregateStream(strings.NewReader(sse(
		"data: {这不是 JSON",
		chunk(`{"choices":[{"delta":{"content":"ok"}}]}`),
		"data:",
	)))
	if err != nil {
		t.Fatalf("坏行不该导致失败：%v", err)
	}
	if got := message(t, result)["content"]; got != "ok" {
		t.Errorf("content = %v，期望 ok", got)
	}
}

// TestAggregateStreamEmpty 完全没有数据时也要返回结构完整的对象。
func TestAggregateStreamEmpty(t *testing.T) {
	result, err := aggregateStream(strings.NewReader(""))
	if err != nil {
		t.Fatalf("空输入不该报错：%v", err)
	}
	if result["object"] != "chat.completion" {
		t.Errorf("object = %v", result["object"])
	}
	if result["model"] != "unknown" {
		t.Errorf("model = %v，期望 unknown", result["model"])
	}
	if !strings.HasPrefix(result["id"].(string), "chatcmpl-") {
		t.Errorf("id = %v，应以 chatcmpl- 开头", result["id"])
	}
	if got := choice(t, result)["finish_reason"]; got != "stop" {
		t.Errorf("finish_reason = %v，期望 stop", got)
	}
}

// --- 测试辅助 ---

// sse 把若干行拼成 SSE 文本，每行后补一个换行。
func sse(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// chunk 把 JSON 包成一条 SSE 数据行。
func chunk(payload string) string {
	return "data: " + payload
}

// choice 取出第一个候选。
func choice(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("choices 结构不对：%v", result["choices"])
	}
	return choices[0].(map[string]any)
}

// message 取出第一个候选里的消息。
func message(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	value, ok := choice(t, result)["message"].(map[string]any)
	if !ok {
		t.Fatalf("message 结构不对：%v", choice(t, result)["message"])
	}
	return value
}
