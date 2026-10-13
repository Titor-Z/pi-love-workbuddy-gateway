package gateway

import (
	"bytes"
	"io"
)

// filterMarkers 是后端内容审核命中时会在响应里出现的特征串。
// 后端并不总是用 HTTP 错误表示拦截，很多时候是正常 200 + 一段特殊文案，
// 所以只能在字节流里观察。
var filterMarkers = [][]byte{
	[]byte("content-filter"),
	[]byte("敏感"),
	[]byte("审核"),
}

// filterWatchReader 在字节透传的同时观察是否出现内容审核标记。
// 它不改变数据流本身，只在流结束时汇报观察结果，命中率靠它统计。
type filterWatchReader struct {
	source  io.Reader
	tail    []byte // 上一块末尾的残留，用于匹配被块边界切断的关键字
	blocked bool   // 是否已命中标记
}

// newFilterWatchReader 包装一个数据源。
func newFilterWatchReader(source io.Reader) *filterWatchReader {
	return &filterWatchReader{source: source}
}

// Read 读取一块数据，顺带检查是否命中审核标记。
func (r *filterWatchReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if n > 0 {
		r.scan(p[:n])
	}
	return n, err
}

// Blocked 表示本次响应里出现了内容审核标记。
func (r *filterWatchReader) Blocked() bool { return r.blocked }

// scan 拼上上一块的尾部后匹配关键字，再保留新的尾部供下一块使用。
func (r *filterWatchReader) scan(chunk []byte) {
	if r.blocked {
		return
	}
	window := append(append([]byte{}, r.tail...), chunk...)
	for _, marker := range filterMarkers {
		if bytes.Contains(window, marker) {
			r.blocked = true
			break
		}
	}
	r.tail = trimmedTail(window, maxMarkerLength())
}

// trimmedTail 保留末尾若干字节，供下一块拼接比对。
func trimmedTail(window []byte, keep int) []byte {
	if len(window) <= keep {
		return window
	}
	return window[len(window)-keep:]
}

// maxMarkerLength 返回最长标记的字节数减一，作为跨块比对的窗口大小。
func maxMarkerLength() int {
	longest := 0
	for _, marker := range filterMarkers {
		if len(marker) > longest {
			longest = len(marker)
		}
	}
	return longest - 1
}
