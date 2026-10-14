// Package journal 记录结构化的运行事件，供 Web 面板统计。
//
// 格式选 JSONL（一行一个 JSON）而不是数据库：
//
//   - 单用户的数据量下（一天几百条、一年几 MB），全量扫描是毫秒级，
//     引 SQLite 需要第三方依赖或 cgo，会破坏「单二进制、零依赖」这个前提；
//   - 文件本身保持人类可读，`tail` 能看、`grep` 能搜、`jq` 能算；
//   - 追加写入不需要事务，进程被 kill 也不会损坏已有内容。
//
// 每条记录都带一个人类可读的 msg 字段，所以直接 tail 也不至于看不懂。
package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kind 是事件类型。
type Kind string

const (
	// KindStart 表示网关启动。
	KindStart Kind = "start"
	// KindStop 表示网关退出。
	KindStop Kind = "stop"
	// KindChat 表示一次对话请求，是面板统计的主要数据来源。
	KindChat Kind = "chat"
	// KindModels 表示一次模型目录拉取。
	KindModels Kind = "models"
	// KindRefresh 表示一次令牌续期。
	KindRefresh Kind = "refresh"
	// KindError 表示网关自身的异常，与单次请求无关。
	KindError Kind = "error"
)

// Record 是一条运行事件。
//
// 时间字段用 RFC3339 而不是 Unix 时间戳：JSONL 是会被人直接看的，
// 一个可读的时间比省下来的几个字节有价值。
type Record struct {
	Time time.Time `json:"t"`
	Kind Kind      `json:"kind"`

	// Message 是给人看的一行摘要，让 tail 也能读懂。
	Message string `json:"msg,omitempty"`

	// 以下字段只有对话事件才会填。
	Model  string  `json:"model,omitempty"`
	Credit float64 `json:"credit,omitempty"`
	In     int     `json:"in,omitempty"`
	Out    int     `json:"out,omitempty"`
	// Cached 是输入里命中缓存的 token 数。命中越多越便宜，所以单独留一列，
	// 面板可以用它回答「这次为什么比上次贵」。
	Cached int   `json:"cached,omitempty"`
	Millis int64 `json:"ms,omitempty"`
	Status int   `json:"status,omitempty"`
	Stream bool  `json:"stream,omitempty"`

	// Blocked 表示这次响应里出现了内容审核标记（确定信号：
	// 流式通道的 content_filter / 非空 refusal，或硬阻断通道的策略码）。
	// 它是面板上「拦截率」的唯一来源，务必如实记录。
	// 文本级的「礼貌拒答」猜测不计入这一栏（见 Refused），那只是疑似。
	Blocked bool `json:"blocked,omitempty"`

	// BlockedReason 是确定拦截的细分原因：content_filter / refusal / policy。
	// 未拦截时为空。面板用它按原因分组，日志也据此拼 msg。
	BlockedReason string `json:"blocked_reason,omitempty"`

	// Refused 表示模型疑似礼貌拒答（文本启发式，来源是 blocked 包的拒答短语表）。
	// 它与 Blocked 分栏：这是「猜的」，可能误报也可能漏报，绝不算进拦截率。
	Refused bool `json:"refused,omitempty"`

	// RefuseRule 是触发拒答猜测的规则（命中的短语），未命中时为空。
	RefuseRule string `json:"refuse_rule,omitempty"`

	// Upstream 是后端返回的错误信息（仅非 200 时填），结构比 msg 更利于统计。
	Upstream *UpstreamError `json:"upstream,omitempty"`

	// FinishReason 是流式通道最终的结束原因（stop / tool_calls / content_filter…）。
	FinishReason string `json:"finish,omitempty"`

	// Tools 是本次响应里调用过的工具名，仅用于展示。
	Tools []string `json:"tools,omitempty"`

	// Request 是请求侧的轻量摘要，不含任何对话内容。
	Request *RequestSummary `json:"req,omitempty"`

	// Error 记录失败原因，成功时为空。
	Error string `json:"err,omitempty"`

	// Version 只在启动事件里出现，方便对照「这个问题是哪个版本产生的」。
	Version string `json:"version,omitempty"`
}

// UpstreamError 是后端返回的结构化错误（非 200 时从 JSON 体里解出）。
// 只记 code 与 msg，不记原始错误页，避免把整页 HTML 塞进日志。
type UpstreamError struct {
	HTTP int    `json:"http"` // 后端 HTTP 状态码
	Code int    `json:"code"` // 后端业务码；非 JSON 或缺失时为 0
	Msg  string `json:"msg"`  // 后端错误文案
}

// RequestSummary 是请求侧的轻量摘要。
// 不记消息内容——只记「有几条消息、是否带工具、是否流式」，够排查就够了。
type RequestSummary struct {
	Messages   int  `json:"messages"`   // 消息条数
	HasTools   bool `json:"has_tools"` // 是否带 tools
	Stream     bool `json:"stream"`     // 是否要求流式
	MaxTokens  int  `json:"max_tokens,omitempty"`
}

// Journal 是只追加的事件文件。
type Journal struct {
	path string
	mu   sync.Mutex
	file *os.File
}

// Open 打开（不存在则创建）事件文件，父目录会自动建立。
func Open(path string) (*Journal, error) {
	if path == "" {
		return nil, fmt.Errorf("事件文件路径为空")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建事件目录失败：%w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开事件文件失败：%w", err)
	}
	return &Journal{path: path, file: file}, nil
}

// Path 返回事件文件路径。
func (j *Journal) Path() string { return j.path }

// Append 追加一条事件。Time 为零时自动补上当前时间。
// 写入失败不返回错误——记录日志失败不该让请求本身失败。
func (j *Journal) Append(record Record) {
	if record.Time.IsZero() {
		record.Time = time.Now()
	}
	// 不用 json.Marshal：它会把 < > & 转义成 \u003c 之类，
	// 而这些字符在模型名、错误信息里很常见，转义后 grep 与 jq 都难用。
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return
	}
	line := buffer.Bytes()

	j.mu.Lock()
	defer j.mu.Unlock()
	// 进程可能已经 Close 过（退出路径上会先停服务再关文件）。
	if j.file == nil {
		return
	}
	_, _ = j.file.Write(line)
}

// Close 关闭事件文件；重复调用是安全的。
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}

// ReadSince 读取指定时间之后的事件。
// since 为零值则读取全部。文件不存在时返回空结果而不是错误——
// 首次运行时这个文件本来就不存在。
func (j *Journal) ReadSince(since time.Time) ([]Record, error) {
	file, err := os.Open(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("打开事件文件失败：%w", err)
	}
	defer file.Close()

	records := make([]Record, 0, 256)
	scanner := bufio.NewScanner(file)
	// 单条事件可能带上较长的错误信息，默认 64KB 的上限留足余量。
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record Record
		if err := json.Unmarshal(line, &record); err != nil {
			// 单行损坏不该让整次统计失败（比如进程被 kill 时写了一半）。
			continue
		}
		if !since.IsZero() && record.Time.Before(since) {
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return records, fmt.Errorf("读取事件文件失败：%w", err)
	}
	return records, nil
}
