package gateway

import (
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
func drain(t *testing.T, chunks ...[]byte) *filterWatchReader {
	t.Helper()
	watch := newFilterWatchReader(&chunkReader{chunks: chunks})
	if _, err := io.Copy(io.Discard, watch); err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	return watch
}

// TestFilterWatchReaderPassesBytesThrough 观察器绝不能改动数据流。
func TestFilterWatchReaderPassesBytesThrough(t *testing.T) {
	payload := []byte("data: {\"choices\":[]}\n\n")
	watch := newFilterWatchReader(&chunkReader{chunks: [][]byte{payload}})
	got, err := io.ReadAll(watch)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("数据被改动了：%q", got)
	}
}

// TestFilterWatchReaderDetectsMarkers 三种特征串都要能认出来。
func TestFilterWatchReaderDetectsMarkers(t *testing.T) {
	cases := map[string]string{
		"content-filter 标记": `{"finish_reason":"content-filter"}`,
		"中文敏感字样":            `{"content":"这个话题有点敏感"}`,
		"中文审核字样":            `{"content":"内容审核未通过"}`,
	}
	for name, payload := range cases {
		if !drain(t, []byte(payload)).Blocked() {
			t.Errorf("%s：应当命中", name)
		}
	}
}

// TestFilterWatchReaderIgnoresNormalResponse 正常回复不该误报，
// 否则拦截率统计毫无意义。
func TestFilterWatchReaderIgnoresNormalResponse(t *testing.T) {
	payload := []byte(`{"model":"glm-5.3","choices":[{"delta":{"content":"你好，今天天气不错"}}]}`)
	if drain(t, []byte(payload)).Blocked() {
		t.Error("正常回复被误判为拦截")
	}
}

// TestFilterWatchReaderMatchesAcrossChunks 关键字被读取边界切断时不能漏检。
// 这是本观察器唯一真正棘手的点：SSE 分片完全可能在任意字节处断开。
func TestFilterWatchReaderMatchesAcrossChunks(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
	}{
		{"ASCII 关键字一分为二", []string{`{"finish_reason":"content-`, `filter"}`}},
		{"ASCII 关键字逐字节", []string{"c", "o", "n", "t", "e", "n", "t", "-", "f", "i", "l", "t", "e", "r"}},
		// "敏感" 的 UTF-8 是 e6 95 8f e6 84 9f，在中间切断。
		{"中文关键字跨块", []string{"有点\u654f", "\u611f的样子"}},
		{"中文关键字逐字节", []string{"\xe6", "\x95", "\x8f", "\xe6", "\x84", "\x9f"}},
	}
	for _, testCase := range cases {
		chunks := make([][]byte, 0, len(testCase.chunks))
		for _, part := range testCase.chunks {
			chunks = append(chunks, []byte(part))
		}
		if !drain(t, chunks...).Blocked() {
			t.Errorf("%s：跨块的关键字漏检了", testCase.name)
		}
	}
}

// TestFilterWatchReaderStopsAfterFirstHit 命中之后不再重复判断，
// 结果稳定可重复读。
func TestFilterWatchReaderStopsAfterFirstHit(t *testing.T) {
	watch := drain(t, []byte("content-filter"), []byte("无关内容"))
	if !watch.Blocked() || !watch.Blocked() {
		t.Error("命中后应持续返回 true")
	}
}
