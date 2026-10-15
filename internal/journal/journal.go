// Package journal 记录结构化的运行事件，供 Web 面板统计。
//
// 存储是**按天分片的 JSONL**（<prefix>-YYYY-MM-DD.jsonl），不是数据库：
//
//   - 「保留 N 天」在分片下就是删掉几个文件（O(1)），不需要重写整个文件；
//     单文件要清理只能重写全量，删比写还贵；
//   - 读取只打开涉及的那几个分片，成本跟**查询天数**有关、跟历史总量无关——
//     单文件最容易出的问题就是「用得越久，面板读得越慢」；
//   - 不引 SQLite 就不需要第三方依赖或 cgo，守住「单二进制、零依赖」这个前提；
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
	"sort"
	"strings"
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

// Journal 是按天分片的事件目录。
//
// 每个分片是 <prefix>-YYYY-MM-DD.jsonl，写入时按记录时间选分片、
// 跨天自动切文件。这样「保留 N 天」就是删掉几个文件，
// 读取时也只打开涉及的那几个分片，不必把历史全量解析一遍。
// 它还顺带修掉了一个旧毛病：单文件里历史越长，一次读取越慢。
type Journal struct {
	dir    string
	prefix string

	mu      sync.Mutex
	file    *os.File
	fileDay string // 当前打开分片的日期（YYYY-MM-DD）；空表示还没打开
}

// dayLayout 是分片文件名里的日期格式。用连字符是为了让文件名排序即时间排序。
const dayLayout = "2006-01-02"

// Open 准备事件分片目录，父目录会自动建立。
func Open(dir, prefix string) (*Journal, error) {
	if dir == "" {
		return nil, fmt.Errorf("事件目录为空")
	}
	if prefix == "" {
		return nil, fmt.Errorf("事件文件前缀为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建事件目录失败：%w", err)
	}
	return &Journal{dir: dir, prefix: prefix}, nil
}

// Dir 返回分片目录。
func (j *Journal) Dir() string { return j.dir }

// Path 返回当前正在写的分片路径（面板上展示用）。
func (j *Journal) Path() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return filepath.Join(j.dir, j.shardName(time.Now()))
}

// shardName 是某一天的分片文件名。
func (j *Journal) shardName(at time.Time) string {
	return j.prefix + "-" + at.Format(dayLayout) + ".jsonl"
}

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
	line, err := encodeRecord(record)
	if err != nil {
		return
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	// 进程可能已经 Close 过（退出路径上会先停服务再关文件）。
	if err := j.openLocked(record.Time); err != nil {
		return
	}
	if j.file == nil {
		return
	}
	_, _ = j.file.Write(line)
}

// openLocked 确保当前打开的是给定日期那一片。
// 跨天时关掉昨天的、打开今天的——所以进程连跑一周也不会写错分片。
func (j *Journal) openLocked(at time.Time) error {
	day := at.Format(dayLayout)
	if j.file != nil && j.fileDay == day {
		return nil
	}
	if j.file != nil {
		_ = j.file.Close()
		j.file = nil
	}
	file, err := os.OpenFile(filepath.Join(j.dir, j.shardName(at)),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	j.file, j.fileDay = file, day
	return nil
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

// Read 读取指定时间之后的事件。since 为零值则读取全部分片。
// 目录不存在时返回空结果而不是错误——首次运行时它本来就不存在。
func (j *Journal) Read(since time.Time) ([]Record, error) {
	j.mu.Lock()
	days, err := j.shardDaysLocked()
	j.mu.Unlock()
	if err != nil {
		return nil, err
	}

	// 分片名是 YYYY-MM-DD，字典序即时间序，所以能直接用字符串比较跳过旧分片：
	// 这是按天分片白拿的一半收益——读取量只跟查询天数有关，跟历史总量无关。
	sinceDay := ""
	if !since.IsZero() {
		sinceDay = since.Format(dayLayout)
	}
	now := time.Now()
	records := make([]Record, 0, 256)
	for _, day := range days {
		if sinceDay != "" && day < sinceDay {
			continue
		}
		if err := j.readShard(filepath.Join(j.dir, j.shardPath(day)), since, &records); err != nil {
			return nil, err
		}
	}
	// 过期的对话正文脱敏后再返回，避免明文长期留在读取结果里。
	for index := range records {
		records[index] = records[index].RedactPayloads(now)
	}
	return records, nil
}

// readShard 读一个分片里的记录，追加到 out。since 为零值表示不限时间。
// 分片可能在读取途中被清理任务删掉，所以文件不存在直接跳过。
func (j *Journal) readShard(path string, since time.Time, out *[]Record) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("打开事件分片失败：%w", err)
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, tooLong, err := readLine(reader)
		if tooLong {
			// 老版本写下的超长行。跳过它继续读后面的——
			// 读不出**一条**记录，不该让整个面板空白。
			if err != nil {
				return nil
			}
			continue
		}
		if err != nil {
			return nil
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
		*out = append(*out, record)
	}
}

// shardPath 由分片日期拼出文件名。day 来自 shardDaysLocked，格式已校验过。
func (j *Journal) shardPath(day string) string {
	return j.prefix + "-" + day + ".jsonl"
}

// shardDaysLocked 列出目录里属于本前缀的分片日期，按升序返回。
// 只认文件名形状正确的项，别的东西（比如用户自己扔进来的笔记）一律忽略。
func (j *Journal) shardDaysLocked() ([]string, error) {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取事件目录失败：%w", err)
	}
	prefix := j.prefix + "-"
	days := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
		if _, err := time.Parse(dayLayout, day); err != nil {
			continue
		}
		days = append(days, day)
	}
	sort.Strings(days)
	return days, nil
}

// Prune 删除超过保留天数的分片，返回被删掉的文件名。
//
// 依据是**文件名里的日期**而不是 mtime：分片名就是它的时间，
// 搬目录、改过系统时间都不会让它误删或漏删。
// retentionDays <= 0 时不删任何东西（宁可占磁盘，也不误删用户数据）。
func (j *Journal) Prune(retentionDays int) ([]string, error) {
	if retentionDays <= 0 {
		return nil, nil
	}
	j.mu.Lock()
	days, err := j.shardDaysLocked()
	j.mu.Unlock()
	if err != nil {
		return nil, err
	}

	// 「保留 7 天」= 今天加前 6 天，所以 cutoff 要减 retentionDays-1。
	cutoff := time.Now().AddDate(0, 0, -(retentionDays - 1)).Format(dayLayout)
	removed := make([]string, 0, len(days))
	for _, day := range days {
		if day >= cutoff {
			continue
		}
		name := j.shardPath(day)
		if err := os.Remove(filepath.Join(j.dir, name)); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("删除旧事件分片失败：%w", err)
		}
		removed = append(removed, name)
	}
	return removed, nil
}

// ImportLegacy 把旧版单文件事件按记录时间拆进分片，返回导入的条数。
//
// 只读源文件、不删除：调用方在确认导入成功后再把源文件改名，
// 这样中途出错也不会把用户的日志弄丢，也不会下次重复导入。
func (j *Journal) ImportLegacy(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("打开旧事件文件失败：%w", err)
	}
	defer file.Close()

	count := 0
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, tooLong, err := readLine(reader)
		if tooLong {
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
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		if record.Time.IsZero() {
			record.Time = time.Now()
		}
		if err := j.appendToShard(record); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// appendToShard 把一条记录写进它所属日期的分片，不碰当前已打开的文件。
// 迁移期间与正常写入可能并发，所以走追加模式而不是复用 j.file。
func (j *Journal) appendToShard(record Record) error {
	record.RequestText = truncatePayload(record.RequestText)
	record.ResponseText = truncatePayload(record.ResponseText)
	line, err := encodeRecord(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(j.dir, j.shardName(record.Time)),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("打开事件分片失败：%w", err)
	}
	defer file.Close()
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("写入事件分片失败：%w", err)
	}
	return nil
}

// encodeRecord 把一条记录编码成一行 JSON。
// 不用 json.Marshal：它会把 < > & 转义成 \u003c 之类，
// 而这些字符在模型名、错误信息里很常见，转义后 grep 与 jq 都难用。
func encodeRecord(record Record) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
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
