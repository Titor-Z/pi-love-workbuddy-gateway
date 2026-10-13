package gateway

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"
)

// sseScanLimit 是单行 SSE 的最大长度。工具调用参数可能很长，留足余量。
const sseScanLimit = 1 << 20

// aggregateStream 消费后端的 OpenAI SSE 流，聚合成一个非流式补全对象。
// 后端只支持流式，所以客户端的非流式请求必须在这里补齐。
func aggregateStream(body io.Reader) (map[string]any, error) {
	collector := newStreamCollector()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), sseScanLimit)
	for scanner.Scan() {
		collector.feed(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return collector.result(), nil
}

// toolCallAccumulator 累积分片的工具调用：同一索引的多个分片要拼成一个完整调用。
type toolCallAccumulator struct {
	id        string
	name      string
	arguments strings.Builder
}

// streamCollector 逐行解析 SSE 并累积内容。
type streamCollector struct {
	content      strings.Builder
	toolCalls    map[int]*toolCallAccumulator
	model        string
	finishReason string
	usage        map[string]any
}

// newStreamCollector 构造累积器。
func newStreamCollector() *streamCollector {
	return &streamCollector{toolCalls: map[int]*toolCallAccumulator{}}
}

// feed 处理一行 SSE。非 data 行与结束标记直接忽略。
func (c *streamCollector) feed(line string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	var chunk struct {
		Model   string `json:"model"`
		Usage   map[string]any
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Delta        struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return
	}
	if chunk.Model != "" {
		c.model = chunk.Model
	}
	if chunk.Usage != nil {
		c.usage = chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != "" {
			c.finishReason = choice.FinishReason
		}
		c.content.WriteString(choice.Delta.Content)
		for _, call := range choice.Delta.ToolCalls {
			c.absorb(call.Index, call.ID, call.Function.Name, call.Function.Arguments)
		}
	}
}

// absorb 把一个工具调用分片并入累积器。
func (c *streamCollector) absorb(index int, id, name, arguments string) {
	slot, ok := c.toolCalls[index]
	if !ok {
		slot = &toolCallAccumulator{}
		c.toolCalls[index] = slot
	}
	if id != "" {
		slot.id = id
	}
	if name != "" {
		slot.name = name
	}
	slot.arguments.WriteString(arguments)
}

// result 产出最终的非流式补全对象。
func (c *streamCollector) result() map[string]any {
	message := map[string]any{
		"role":    "assistant",
		"content": nullableText(c.content.String()),
	}
	finishReason := c.finishReason
	if calls := c.toolCallList(); len(calls) > 0 {
		message["tool_calls"] = calls
		if finishReason == "" {
			finishReason = "tool_calls"
		}
	}
	return map[string]any{
		"id":      "chatcmpl-" + randomSuffix(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   defaultIfEmpty(c.model, "unknown"),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": defaultIfEmpty(finishReason, "stop"),
		}},
		"usage": c.usageOrDefault(),
	}
}

// toolCallList 按索引顺序还原工具调用列表。
func (c *streamCollector) toolCallList() []any {
	if len(c.toolCalls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(c.toolCalls))
	for index := range c.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	calls := make([]any, 0, len(indexes))
	for _, index := range indexes {
		slot := c.toolCalls[index]
		calls = append(calls, map[string]any{
			"id":   slot.id,
			"type": "function",
			"function": map[string]any{
				"name":      slot.name,
				"arguments": slot.arguments.String(),
			},
		})
	}
	return calls
}

// usageOrDefault 取后端给的用量；缺失时补零，避免客户端解析失败。
func (c *streamCollector) usageOrDefault() map[string]any {
	if c.usage != nil {
		return c.usage
	}
	return map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
}

// nullableText 把空串转成 null，与后端对「无正文」的表示保持一致。
func nullableText(text string) any {
	if text == "" {
		return nil
	}
	return text
}

// defaultIfEmpty 为空时返回兜底值。
func defaultIfEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// randomSuffix 生成补全 ID 的随机后缀。
func randomSuffix() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(buf)
}
