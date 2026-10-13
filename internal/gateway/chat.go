package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
)

// defaultModel 是客户端没指定模型时的兜底值。
const defaultModel = "auto"

// passthroughKeys 是允许透传给后端的请求字段白名单。
// 只放后端认得的字段，避免客户端塞进来的未知参数触发后端安全策略。
var passthroughKeys = []string{
	"model", "messages", "tools", "tool_choice",
	"temperature", "max_tokens", "max_completion_tokens", "top_p",
	"stream", "stream_options", "stop",
	"presence_penalty", "frequency_penalty", "n",
	"response_format", "seed", "user",
	"reasoning_effort", "verbosity", "reasoning_summary",
}

// handleChat 处理对话补全：流式请求字节透传，非流式请求聚合后返回。
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	clientBody, err := decodeJSONBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if !hasMessages(clientBody) {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "messages is required")
		return
	}

	upstreamBody, err := json.Marshal(buildUpstreamBody(clientBody))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	modelName := defaultIfEmpty(stringField(clientBody, "model"), defaultModel)
	streaming := wantsStream(clientBody)
	started := time.Now()
	s.logger.Printf("▶ %s | stream=%v", modelName, streaming)

	resp, err := s.up.ChatStream(r.Context(), upstreamBody)
	if err != nil {
		s.logger.Printf("✗ 后端请求失败 | %s | %v", modelName, err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		message := fmt.Sprintf("✗ HTTP %d | %s | %s", resp.StatusCode, modelName, truncate(string(raw), 300))
		s.logger.Printf("%s", message)
		s.recordChat(journal.Record{
			Kind: journal.KindChat, Model: modelName, Stream: streaming,
			Status: resp.StatusCode, Millis: time.Since(started).Milliseconds(),
			Message: message, Error: truncate(string(raw), 300),
		})
		writeUpstreamError(w, resp.StatusCode, raw)
		return
	}

	observer := newStreamObserver(resp.Body)
	if streaming {
		s.relayStream(w, observer)
		s.finishChat(modelName, observer, started, true, http.StatusOK, "")
		return
	}
	result, err := aggregateStream(observer)
	if err != nil {
		message := fmt.Sprintf("✗ 聚合流式响应失败 | %s | %v", modelName, err)
		s.logger.Printf("%s", message)
		s.recordChat(journal.Record{
			Kind: journal.KindChat, Model: modelName,
			Status: http.StatusBadGateway, Millis: time.Since(started).Milliseconds(),
			Message: message, Error: err.Error(),
		})
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	s.finishChat(modelName, observer, started, false, http.StatusOK, "")
	writeJSON(w, http.StatusOK, result)
}

// finishChat 在请求结束时记一条事件，并补一条人类可读的日志。
// 流式与非流式共用同一条记录路径，避免两边统计口径不一致。
func (s *Server) finishChat(modelName string, observer *streamObserver,
	started time.Time, streaming bool, status int, failure string) {

	cost := time.Since(started).Round(time.Millisecond)
	usage := observer.Usage()

	// 用量全为零时说明后端没发用量事件，这时不该在面板上凭空多出一次零消耗调用，
	// 但请求本身确实发生了，所以照记，只是记为不计费。
	mode := "非流式"
	if streaming {
		mode = "流式"
	}
	cacheTag := ""
	if cached := usage.Cached(); cached > 0 {
		cacheTag = fmt.Sprintf(" | 缓存命中 %d/%d", cached, usage.Prompt)
	}
	blockedTag := ""
	if observer.Blocked() {
		blockedTag = " | ⚠️疑似内容审核拦截"
	}
	message := fmt.Sprintf("◀ %s | %s | %s完成 | credit=%g%s%s",
		modelName, cost, mode, usage.Credit, cacheTag, blockedTag)
	if failure == "" {
		s.logger.Printf("%s", message)
	} else {
		s.logger.Printf("✗ %s", message)
	}

	s.recordChat(journal.Record{
		Kind:    journal.KindChat,
		Message: message,
		Model:   modelName,
		Credit:  usage.Credit,
		In:      usage.Prompt,
		Out:     usage.Completion,
		Cached:  usage.Cached(),
		Millis:  cost.Milliseconds(),
		Status:  status,
		Stream:  streaming,
		Blocked: observer.Blocked(),
		Error:   failure,
	})
}

// recordChat 把事件写入 journal。没接 journal 时静默跳过，便于测试单独跑网关。
func (s *Server) recordChat(record journal.Record) {
	if s.journal != nil {
		s.journal.Append(record)
	}
}

// buildUpstreamBody 从客户端请求体里筛出后端认得的字段，并做两处必要改写。
func buildUpstreamBody(clientBody map[string]any) map[string]any {
	body := make(map[string]any, len(passthroughKeys)+2)
	for _, key := range passthroughKeys {
		if value, ok := clientBody[key]; ok {
			body[key] = value
		}
	}
	if _, ok := body["model"]; !ok {
		body["model"] = defaultModel
	}
	// 后端只支持流式，非流式响应由本网关聚合 SSE 得到。
	body["stream"] = true
	if _, ok := body["stream_options"]; !ok {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	// 后端不认 developer 角色，遇到会触发安全策略拦截，统一映射为 system。
	if messages, ok := body["messages"].([]any); ok {
		body["messages"] = normalizeRoles(messages)
	}
	return body
}

// normalizeRoles 把后端不支持的 developer 角色改写成 system。
func normalizeRoles(messages []any) []any {
	normalized := make([]any, 0, len(messages))
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok || stringField(message, "role") != "developer" {
			normalized = append(normalized, item)
			continue
		}
		rewritten := make(map[string]any, len(message))
		for key, value := range message {
			rewritten[key] = value
		}
		rewritten["role"] = "system"
		normalized = append(normalized, rewritten)
	}
	return normalized
}

// relayStream 把后端 SSE 原样透传，并在每次写入后立即刷出，避免客户端空等。
func (s *Server) relayStream(w http.ResponseWriter, body io.Reader) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	if _, err := io.Copy(&flushWriter{writer: w, flusher: flusher}, body); err != nil &&
		!errors.Is(err, context.Canceled) {
		// 客户端主动中断连接是正常行为（比如已经拿到工具调用就取消），不进日志。
		s.logger.Printf("✗ 流式透传中断：%v", err)
	}
}

// flushWriter 每次写入后立刻刷新，保证 SSE 事件即时到达客户端。
type flushWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

// Write 写入并刷新。
func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.writer.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

// hasMessages 判断请求体里是否有非空的 messages 数组。
func hasMessages(body map[string]any) bool {
	messages, ok := body["messages"].([]any)
	return ok && len(messages) > 0
}

// wantsStream 判断客户端是否要求流式响应。
func wantsStream(body map[string]any) bool {
	flag, ok := body["stream"].(bool)
	return ok && flag
}

// stringField 取出对象里的字符串字段；缺失或类型不符时返回空串。
func stringField(obj map[string]any, key string) string {
	if value, ok := obj[key].(string); ok {
		return value
	}
	return ""
}

// truncate 截断过长文本，只用于日志。
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// writeUpstreamError 把后端错误原样转给客户端；不是 JSON 时包装成标准错误结构。
func writeUpstreamError(w http.ResponseWriter, status int, raw []byte) {
	if json.Valid(raw) && len(raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return
	}
	writeError(w, status, "upstream_error", truncate(string(raw), 500))
}
