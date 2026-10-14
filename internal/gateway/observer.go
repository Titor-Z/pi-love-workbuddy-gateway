package gateway

import (
	"bytes"
	"encoding/json"
	"io"
)

// observerLineLimit 是单行 SSE 的字节上限。工具调用的参数可能很长，留足余量；
// 超过就丢弃这一行，避免一个畸形响应把内存吃光。
const observerLineLimit = 1 << 20

// blockedFinishReasons 是后端明确告知「这条内容被安全策略拦下」的结束原因。
// 普通的礼貌拒答（「抱歉我不能……」）后端给的是 "stop"，和正常回复一样，
// 没有结构上的区分信号——那种情况我们**无法**可靠识别，也不假装能。
var blockedFinishReasons = map[string]bool{
	"content_filter": true,
}

// usageInfo 是后端在 SSE 里回传的用量。
//
// credit 就是资源包的扣减量，与账单接口的口径一致（实测精确吻合到小数点后 8 位），
// 只是账单那边有 1～3 分钟延迟。面板的用量统计直接累加这个值，不做任何估算。
type usageInfo struct {
	Credit     float64 `json:"credit"`
	Prompt     int     `json:"prompt_tokens"`
	Completion int     `json:"completion_tokens"`
	Total      int     `json:"total_tokens"`

	// CacheHit 是输入里命中缓存的那部分 token 数。
	// 后端同时给了 prompt_cache_hit_tokens 与 prompt_tokens_details.cached_tokens，
	// 两个值一致；这里以前者为准，后者兜底。它直接回答「这次为什么便宜」。
	CacheHit  int `json:"prompt_cache_hit_tokens"`
	CacheMiss int `json:"prompt_cache_miss_tokens"`

	// Details 用于在 CacheHit 缺失时兜底取值。
	Details struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// Cached 返回命中缓存的 token 数，两个字段都缺时返回 0。
func (u usageInfo) Cached() int {
	if u.CacheHit > 0 {
		return u.CacheHit
	}
	return u.Details.Cached
}

// streamObserver 在把字节原样透传的同时，顺带观察两件事：
//
//  1. 响应是否被后端的内容安全策略拦下（依据 finish_reason=content_filter 或
//     refusal 非空——这是后端给出的结构信号，见 blockedFinishReasons 与 handleLine）；
//  2. SSE 里回传的用量与结束原因（面板统计用）。
//
// 它只读不写，绝不改动流的内容本身。
type streamObserver struct {
	source io.Reader

	blocked bool // 是否已命中内容审核结构信号（finish_reason=content_filter 或 refusal 非空）

	lineBuf      []byte // 尚未凑齐整行的 SSE 残留
	usage        *usageInfo
	finishReason string
	toolNames    []string
}

// newStreamObserver 包装一个数据源。
func newStreamObserver(source io.Reader) *streamObserver {
	return &streamObserver{source: source}
}

// Read 读取一块数据，透传的同时做观察。
func (o *streamObserver) Read(p []byte) (int, error) {
	n, err := o.source.Read(p)
	if n > 0 {
		o.scanLines(p[:n])
	}
	return n, err
}

// Blocked 表示本次响应里出现了内容审核标记。
func (o *streamObserver) Blocked() bool { return o.blocked }

// Usage 返回后端回传的用量；没有任何用量事件时返回零值。
func (o *streamObserver) Usage() usageInfo {
	if o.usage == nil {
		return usageInfo{}
	}
	return *o.usage
}

// FinishReason 返回流的结束原因，未收到时为 "stop"。
func (o *streamObserver) FinishReason() string {
	if o.finishReason == "" {
		return "stop"
	}
	return o.finishReason
}

// ToolNames 返回本次响应里调用过的工具名，仅用于展示。
func (o *streamObserver) ToolNames() []string { return o.toolNames }

// scanLines 把字节流按行切开，逐行交给 SSE 解析。
func (o *streamObserver) scanLines(chunk []byte) {
	if len(o.lineBuf)+len(chunk) > observerLineLimit {
		// 单行过长：丢掉已缓冲的内容，避免内存失控。
		o.lineBuf = o.lineBuf[:0]
		return
	}
	o.lineBuf = append(o.lineBuf, chunk...)
	for {
		index := bytes.IndexByte(o.lineBuf, '\n')
		if index < 0 {
			return
		}
		line := o.lineBuf[:index]
		o.lineBuf = o.lineBuf[index+1:]
		o.handleLine(line)
	}
}

// handleLine 解析一行 SSE。非 data 行、空载荷与结束标记都直接跳过。
func (o *streamObserver) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}

	var chunk struct {
		Usage   *usageInfo `json:"usage"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Delta        struct {
				// Refusal 是后端对「拒绝输出这部分内容」的结构标记。
				// 正常回复里它也会出现，但恒为空串；非空才表示真被拦。
				Refusal   string `json:"refusal"`
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return
	}
	if chunk.Usage != nil {
		o.usage = chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != "" {
			o.finishReason = choice.FinishReason
			if blockedFinishReasons[choice.FinishReason] {
				o.blocked = true
			}
		}
		if choice.Delta.Refusal != "" {
			o.blocked = true
		}
		for _, call := range choice.Delta.ToolCalls {
			if name := call.Function.Name; name != "" {
				o.toolNames = append(o.toolNames, name)
			}
		}
	}
}
