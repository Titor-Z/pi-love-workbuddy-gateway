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
//
// 关于「对话正文」的记录契约（重要，v0.2.4 起生效）：
//   - 只有被判定为「确定拦截」或「疑似拒答」的对话事件，才会写入 request /
//     response 两个正文字段，用于复盘「到底发了什么被拦」。正常对话一律不存。
//   - 正文是明文、落在本机文件里，因此另有 30 天 TTL：超过 payloadTTL 的正文
//     在读取时（ReadSince）会被脱敏为空，不会无限期留存。
//   - 这是把「能看清 pi↔WorkBuddy 这个黑盒」与「隐私」之间取平衡后的边界，
//     不要为了调试方便把正文范围扩大到全部对话。
//
// 关于「限流」的说明：后端对模型有用量/频率限制，通常以 HTTP 429 回绝。
// 它不是内容拦截，因此不算入 blocked 判定，只作为普通上游错误记录在
// upstream{http,code,msg} 里——面板的抽屉能看清是哪次、哪个模型被限流。
package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
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

	// RateLimited 表示这次是被后端的用量/频率限制挡下（实测 code 6004）。
	// 它与 Blocked 是两回事：限流是排队问题，不是内容审核，绝不进拦截率。
	RateLimited bool `json:"rate_limited,omitempty"`

	// RateResetAt 是后端给出的限流重置时间原文（形如 "2026-10-14 23:13:09 UTC+8"）。
	// 这是用户最需要的信息——还要等多久。取不到时为空。
	RateResetAt string `json:"rate_reset_at,omitempty"`

	// RefuseRule 是触发拒答猜测的规则（命中的短语），未命中时为空。
	RefuseRule string `json:"refuse_rule,omitempty"`

	// Upstream 是后端返回的错误信息（仅非 200 时填），结构比 msg 更利于统计。
	Upstream *UpstreamError `json:"upstream,omitempty"`

	// FinishReason 是流式通道最终的结束原因（stop / tool_calls / content_filter…）。
	FinishReason string `json:"finish,omitempty"`

	// Tools 是本次响应里调用过的工具名，仅用于展示。
	Tools []string `json:"tools,omitempty"`

	// RequestText 是本次请求的对话正文（messages 序列化）。
	// 仅当本事件被判定为「确定拦截」或「疑似拒答」时才填充，用于复盘
	// 「到底发了什么被拦」——正常对话一律不存，守住隐私。
	// 注意：这是明文文本，落在本机日志文件里。
	RequestText string `json:"request,omitempty"`

	// ResponseText 是本次响应里助手回复的正文（delta.content 累积）。
	// 同样只在拦截/拒答时填充；正常回复不存。
	ResponseText string `json:"response,omitempty"`

	// Request 是请求侧的轻量摘要，不含任何对话内容。
	Request *RequestSummary `json:"req,omitempty"`

	// Error 记录失败原因，成功时为空。
	Error string `json:"err,omitempty"`

	// Version 只在启动事件里出现，方便对照「这个问题是哪个版本产生的」。
	Version string `json:"version,omitempty"`

	// Device 是发起本次请求的设备 ID；本机直连（pi 直接连网关）为空。
	//
	// 多设备共用一个账号之后，「哪个请求来自哪台机器」就成了必须能回答的问题，
	// 而这是回答它的唯一依据。名字不存进来：名字会被用户改，
	// 存下来的就是过期数据；展示时再按 ID 去设备表里查。
	Device string `json:"device,omitempty"`
}

// payloadLimit 是单条事件里对话正文的留存上限（64KB，按字节）。
//
// 这个上限不是「够不够用」的取舍，而是**读取器的硬约束**：
// 事件文件是 JSONL，一行就是一条记录。一行没有上限，读取器就得
// 无限缓冲；反过来给读取器设上限，超长的那一行就会让整次读取失败。
// 两头各设一道：写入时截断（这里），读取时跳过（readLine）。
const payloadLimit = 64 << 10

// truncatePayload 把过长的正文截断并留下标记。
//
// 按字节截断可能切在多字节字符中间，所以要回退到最后一个完整字符的
// 边界上——否则截出来的尾巴是乱码，用户会以为文件坏了。
func truncatePayload(text string) string {
	if len(text) <= payloadLimit {
		return text
	}
	cut := payloadLimit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n…（正文过长，已截断）"
}

// payloadTTL 是带对话正文的事件保留时长。超过这个时长的拦截/拒答正文会被
// 读取时脱敏（置空），避免日志文件无限膨胀、也缩短明文对话落盘的暴露窗口。
// 正常对话不存正文，不受此限。
const payloadTTL = 30 * 24 * time.Hour

// RedactPayloads 返回去掉了过期正文的副本。超过 payloadTTL 的对话事件，
// 其 request/response 正文会被清空（其它统计字段原样保留）。
// 这是为了在「能复盘被拦内容」与「明文不长期落盘」之间取平衡。
func (r Record) RedactPayloads(now time.Time) Record {
	if r.RequestText == "" && r.ResponseText == "" {
		return r
	}
	if now.Sub(r.Time) <= payloadTTL {
		return r
	}
	cloned := r
	cloned.RequestText = ""
	cloned.ResponseText = ""
	return cloned
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
	Messages  int  `json:"messages"`  // 消息条数
	HasTools  bool `json:"has_tools"` // 是否带 tools
	Stream    bool `json:"stream"`    // 是否要求流式
	MaxTokens int  `json:"max_tokens,omitempty"`
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
	// 正文必须在**写入时**就有上限。它是给复盘用的，不需要全文；
	// 不设上限的话一次大请求就能写出一行几百 KB 的记录，
	// 之后整个面板都读不出事件——曾经就是这样炸的。
	record.RequestText = truncatePayload(record.RequestText)
	record.ResponseText = truncatePayload(record.ResponseText)
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
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, tooLong, err := readLine(reader)
		if tooLong {
			// 老版本写下的超长行。跳过它继续读后面的——
			// 读不出**一条**记录，不该让整个面板空白。
			if err != nil {
				break
			}
			continue
		}
		if err != nil {
			break
		}
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
		// 过期的对话正文脱敏后再返回，避免明文长期留在读取结果里。
		records = append(records, record.RedactPayloads(time.Now()))
	}
	return records, nil
}

// maxLineBytes 是单行事件的读取上限，只作兜底。
//
// 写入侧（truncatePayload）已经把正文截到 payloadLimit 以内，正常记录
// 远小于这个值。这个上限防的是**老版本写下的**超长行。
const maxLineBytes = 8 << 20

// readLine 读一整行，行为接近 bufio.Scanner 但不因超长而终止。
//
// 行超过 maxLineBytes 时返回 tooLong=true 并丢掉该行的剩余部分，
// 让调用方跳过这一行继续读下一行。
// 返回 io.EOF 表示读完了。
func readLine(reader *bufio.Reader) (line []byte, tooLong bool, err error) {
	var buffer []byte
	for {
		chunk, readErr := reader.ReadSlice('\n')
		if !tooLong {
			if len(buffer)+len(chunk) > maxLineBytes {
				// 这一行太长了，丢掉已读的部分，剩下的边读边丢。
				tooLong = true
				buffer = nil
			} else {
				buffer = append(buffer, chunk...)
			}
		}
		switch {
		case readErr == nil:
			return buffer, tooLong, nil
		case errors.Is(readErr, bufio.ErrBufferFull):
			continue
		case errors.Is(readErr, io.EOF):
			if len(buffer) == 0 && !tooLong {
				return nil, false, io.EOF
			}
			return buffer, tooLong, nil
		default:
			return buffer, tooLong, readErr
		}
	}
}
