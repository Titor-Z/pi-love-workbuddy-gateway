package gateway

import (
	"bytes"
	"io"
	"testing"
)

// chunkReader 按预设切片逐块返回数据，用来精确控制读取边界，
// 以便验证关键字被切断时的跨块匹配。
type chunkReader struct {
	chunks [][]byte
	index  int
}

// Read 返回下一块数据，读完后报 EOF。
func (r *chunkReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.index])
	r.index++
	return n, nil
}

// drain 读干净数据源并返回观察器。
func drain(t *testing.T, chunks ...[]byte) *streamObserver {
	t.Helper()
	watch := newStreamObserver(&chunkReader{chunks: chunks})
	if _, err := io.Copy(io.Discard, watch); err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	return watch
}

// TestFilterWatchReaderPassesBytesThrough 观察器绝不能改动数据流。
func TestFilterWatchReaderPassesBytesThrough(t *testing.T) {
	payload := []byte("data: {\"choices\":[]}\n\n")
	watch := newStreamObserver(&chunkReader{chunks: [][]byte{payload}})
	got, err := io.ReadAll(watch)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("数据被改动了：%q", got)
	}
}

// TestObserverFlagsContentFilter 后端明确告知「被安全策略拦下」时
// （finish_reason=content_filter）才算命中。这是结构信号，不是正文里有什么字。
func TestObserverFlagsContentFilter(t *testing.T) {
	if !drain(t, []byte(`data: {"choices":[{"delta":{},"finish_reason":"content_filter"}]}`+"\n")).Blocked() {
		t.Error("finish_reason=content_filter 应当命中")
	}
}

// TestObserverFlagsNonEmptyRefusal 后端在 delta 里给非空 refusal 时命中。
// 正常回复的 refusal 是空串，不算。
func TestObserverFlagsNonEmptyRefusal(t *testing.T) {
	if !drain(t, []byte(`data: {"choices":[{"delta":{"refusal":"我无法提供该内容"}}]}`+"\n")).Blocked() {
		t.Error("非空 refusal 应当命中")
	}
}

// TestObserverIgnoresNormalResponse 正常回复（无论有没有敏感词）都不该误报——
// 否则「拦截率」统计毫无意义。后端对礼貌拒答给的是 finish_reason=stop，
// 和正常回复一样，没有结构区分，我们不假装能识别。
func TestObserverIgnoresNormalResponse(t *testing.T) {
	cases := []string{
		`{"model":"glm-5.3","choices":[{"delta":{"content":"你好，今天天气不错"}}]}`,
		`{"choices":[{"delta":{"content":"这个话题有点敏感"}}]}`,
		`{"choices":[{"delta":{"content":"内容审核未通过"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"敏感"}}]}`,
		`{"choices":[{"delta":{"refusal":""}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	for i, payload := range cases {
		if drain(t, []byte(payload+"\n")).Blocked() {
			t.Errorf("用例 %d 被误判为拦截：%s", i, payload)
		}
	}
}

// TestObserverBlockedSpansChunks 命中信号被读取边界切断时不能漏检。
// SSE 分片完全可能在任意字节处断开，这不是理论情况。
func TestObserverBlockedSpansChunks(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
	}{
		{"finish_reason 跨块", []string{`data: {"choices":[{"delta":{},"finish_rea`, `son":"content_filter"}]}`+"\n"}},
		{"refusal 跨块", []string{`data: {"choices":[{"delta":{"re`, `fusal":"我无法"}}]}`+"\n"}},
	}
	for _, testCase := range cases {
		chunks := make([][]byte, 0, len(testCase.chunks))
		for _, part := range testCase.chunks {
			chunks = append(chunks, []byte(part))
		}
		if !drain(t, chunks...).Blocked() {
			t.Errorf("%s：跨块的信号漏检了", testCase.name)
		}
	}
}

// TestObserverStopsAfterFirstHit 命中之后不再重复判断，结果稳定可重复读。
func TestObserverStopsAfterFirstHit(t *testing.T) {
	watch := drain(t, []byte(`data: {"choices":[{"delta":{},"finish_reason":"content_filter"}]}`+"\n"))
	if !watch.Blocked() || !watch.Blocked() {
		t.Error("命中后应持续返回 true")
	}
}

// ---------- 用量与结束原因的解析 ----------

// TestObserverExtractsUsage 后端把 credit 放在 SSE 的 usage 事件里，
// 面板的用量统计全靠它——实测它与账单接口的口径一致。
func TestObserverExtractsUsage(t *testing.T) {
	observer := drain(t,
		[]byte(`data: {"model":"glm-5.3","choices":[{"delta":{"content":"你"}}]}`+"\n"),
		[]byte(`data: {"choices":[],"usage":{"prompt_tokens":37,"completion_tokens":200,"total_tokens":237,"credit":0.12}}`+"\n"),
	)

	usage := observer.Usage()
	if usage.Credit != 0.12 {
		t.Errorf("credit = %v，期望 0.12", usage.Credit)
	}
	if usage.Prompt != 37 || usage.Completion != 200 || usage.Total != 237 {
		t.Errorf("token 数不对：%+v", usage)
	}
}

// TestObserverUsageSpansChunks 一条 SSE 事件被拆到多个读取块里也要能解析。
// SSE 分片完全可能在任意字节处断开，这不是理论情况。
func TestObserverUsageSpansChunks(t *testing.T) {
	observer := drain(t,
		[]byte(`data: {"choices":[],"usage":{"prompt_to`),
		[]byte(`kens":37,"completion_tokens":200,"credit":0.12}}`+"\n"),
	)
	if got := observer.Usage(); got.Credit != 0.12 || got.Prompt != 37 {
		t.Errorf("跨块的事件没解析出来：%+v", got)
	}
}

// TestObserverUsageIsLastOneWins 后端在流末尾会再发一次用量，以最后一次为准。
func TestObserverUsageIsLastOneWins(t *testing.T) {
	observer := drain(t,
		[]byte(`data: {"choices":[],"usage":{"credit":0.01}}`+"\n"),
		[]byte(`data: {"choices":[],"usage":{"credit":0.99}}`+"\n"),
	)
	if got := observer.Usage().Credit; got != 0.99 {
		t.Errorf("credit = %v，期望取最后一次 0.99", got)
	}
}

// TestObserverDefaultFinishReason 没收到结束原因时给 stop，
// 免得面板上出现空白的结束状态。
func TestObserverDefaultFinishReason(t *testing.T) {
	if got := drain(t, []byte(`data: {"choices":[]}`+"\n")).FinishReason(); got != "stop" {
		t.Errorf("finish reason = %q，期望 stop", got)
	}
	observer := drain(t, []byte(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n"))
	if got := observer.FinishReason(); got != "tool_calls" {
		t.Errorf("finish reason = %q，期望 tool_calls", got)
	}
}

// TestObserverCollectsToolNames 工具名用于展示「这次用了哪些工具」。
func TestObserverCollectsToolNames(t *testing.T) {
	observer := drain(t,
		[]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read"}}]}}]}`+"\n"),
		[]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}},{"index":1,"function":{"name":"bash"}}]}}]}`+"\n"),
	)
	names := observer.ToolNames()
	if len(names) != 2 || names[0] != "read" || names[1] != "bash" {
		t.Errorf("工具名 = %v，期望 [read bash]", names)
	}
}

// TestObserverBadLineDoesNotPoisonUsageScan 畸形行不能影响用量解析，也不能 panic。
func TestObserverBadLineDoesNotPoisonUsageScan(t *testing.T) {
	observer := drain(t,
		[]byte(`: 注释行`+"\n"),
		[]byte(`data: {不是 JSON`+"\n"),
		[]byte(`data:`+"\n"),
		[]byte(`data: [DONE]`+"\n"),
		[]byte(`data: {"choices":[],"usage":{"credit":0.5}}`+"\n"),
	)
	if observer.Usage().Credit != 0.5 {
		t.Errorf("坏行之后的数据丢了：%+v", observer.Usage())
	}
}

// TestObserverExtractsCacheHit 缓存命中直接决定这次贵不贵，必须取到。
func TestObserverExtractsCacheHit(t *testing.T) {
	reader := newStreamObserver(bytes.NewReader([]byte(
		`data: {"choices":[],"usage":{"prompt_tokens":18,"completion_tokens":89,` +
			`"prompt_cache_hit_tokens":17,"prompt_cache_miss_tokens":1,"credit":0.05}}` + "\n")))

	_, _ = io.ReadAll(reader)
	if got := reader.Usage().Cached(); got != 17 {
		t.Errorf("Cached() = %d，期望 17", got)
	}
}

// TestObserverCacheHitFallsBackToDetails 后端有时只给 details.cached_tokens，
// 这时不能记成 0——记 0 会让面板上凭空多出一次「没有命中缓存」。
func TestObserverCacheHitFallsBackToDetails(t *testing.T) {
	reader := newStreamObserver(bytes.NewReader([]byte(
		`data: {"choices":[],"usage":{"prompt_tokens":100,"credit":0.2,` +
			`"prompt_tokens_details":{"cached_tokens":64}}}` + "\n")))

	_, _ = io.ReadAll(reader)
	if got := reader.Usage().Cached(); got != 64 {
		t.Errorf("Cached() = %d，期望回退到 details 的 64", got)
	}
}

// TestObserverCacheHitAbsent 两个字段都没有时返回 0，而不是把 Prompt 当成命中。
func TestObserverCacheHitAbsent(t *testing.T) {
	reader := newStreamObserver(bytes.NewReader([]byte(
		`data: {"choices":[],"usage":{"prompt_tokens":100,"credit":0.2}}` + "\n")))

	_, _ = io.ReadAll(reader)
	if got := reader.Usage().Cached(); got != 0 {
		t.Errorf("Cached() = %d，期望 0", got)
	}
}
