package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// newTestJournal 建一个临时分片目录并打开它。
// 返回 dir 是为了让测试能直接检查落盘的分片文件。
func newTestJournal(t *testing.T) (*Journal, string) {
	t.Helper()
	dir := t.TempDir()
	instance, err := Open(dir, "gateway")
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance, dir
}

// shardPathOf 是测试里拼分片路径的辅助，避免到处重复文件名格式。
func shardPathOf(dir, day string) string {
	return filepath.Join(dir, "gateway-"+day+".jsonl")
}

func dayOffset(days int) string {
	return time.Now().AddDate(0, 0, days).Format(dayLayout)
}

// TestOpenCreatesDirectory 首次运行时目录还不存在，要能自动建出来。
func TestOpenCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "journal")
	instance, err := Open(dir, "gateway")
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer instance.Close()

	instance.Append(Record{Kind: KindStart, Message: "跑起来了"})
	if _, err := os.Stat(instance.Path()); err != nil {
		t.Fatalf("事件分片没建出来：%v", err)
	}
	if instance.Dir() != dir {
		t.Errorf("Dir = %q，期望 %q", instance.Dir(), dir)
	}
}

// TestOpenRejectsBadPath 目录建不出来时要报错，而不是静默丢弃事件。
func TestOpenRejectsBadPath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备测试介质失败：%v", err)
	}
	if _, err := Open(filepath.Join(blocker, "journal"), "gateway"); err == nil {
		t.Fatal("父路径不是目录时应当报错")
	}
	if _, err := Open("", "gateway"); err == nil {
		t.Fatal("空目录应当报错")
	}
	if _, err := Open(t.TempDir(), ""); err == nil {
		t.Fatal("空前缀应当报错")
	}
}

// TestAppendAndRead 写入的事件要能读回来，字段一个不少。
func TestAppendAndRead(t *testing.T) {
	instance, _ := newTestJournal(t)

	instance.Append(Record{
		Kind: KindChat, Message: "◀ glm-5.3", Model: "glm-5.3",
		Credit: 0.12, In: 37, Out: 200, Millis: 2013, Status: 200, Stream: true,
	})
	instance.Append(Record{Kind: KindChat, Model: "hy3", Blocked: true})

	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(records) != 2 {
		t.Fatalf("条数 = %d，期望 2", len(records))
	}
	first := records[0]
	if first.Kind != KindChat || first.Model != "glm-5.3" || first.Credit != 0.12 {
		t.Errorf("第一条不对：%+v", first)
	}
	if first.In != 37 || first.Out != 200 || first.Millis != 2013 || !first.Stream {
		t.Errorf("用量字段不对：%+v", first)
	}
	if !records[1].Blocked {
		t.Error("拦截标记丢了")
	}
	// 时间没给时要自动补，否则面板的时间轴会出现零值。
	if first.Time.IsZero() {
		t.Error("时间没有被自动补上")
	}
}

// TestAppendSplitsByDay 跨天写入要落到不同分片，否则「保留 N 天」无从删起。
func TestAppendSplitsByDay(t *testing.T) {
	instance, dir := newTestJournal(t)

	base := time.Now()
	instance.Append(Record{Time: base.AddDate(0, 0, -2), Kind: KindChat, Message: "前天"})
	instance.Append(Record{Time: base.AddDate(0, 0, -1), Kind: KindChat, Message: "昨天"})
	instance.Append(Record{Time: base, Kind: KindChat, Message: "今天"})

	// 三个日期各一个文件，不多不少。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录失败：%v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("分片数 = %d，期望 3", len(entries))
	}
	for _, offset := range []int{-2, -1, 0} {
		path := shardPathOf(dir, dayOffset(offset))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("缺少 %d 天前的分片：%v", -offset, err)
		}
	}

	// 读取要把三片都串起来，且按时间升序。
	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(records) != 3 {
		t.Fatalf("条数 = %d，期望 3", len(records))
	}
	if records[0].Message != "前天" || records[2].Message != "今天" {
		t.Errorf("跨分片顺序不对：%q … %q", records[0].Message, records[2].Message)
	}
}

// TestAppendIsValidJSONL 每行都必须是独立可解析的 JSON——
// 面板与 jq 都依赖这一点。
func TestAppendIsValidJSONL(t *testing.T) {
	instance, _ := newTestJournal(t)
	// 消息里带上换行与引号，验证转义不会破坏「一行一条」。
	instance.Append(Record{Kind: KindChat, Message: "含\"引号\"\n与换行"})
	instance.Append(Record{Kind: KindChat, Message: "第二条"})
	path := instance.Path()
	instance.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d，期望 2（换行必须被转义）", len(lines))
	}
	for i, line := range lines {
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("第 %d 行不是合法 JSON：%v", i+1, err)
		}
	}
}

// TestReadFiltersByTime 按时间过滤，面板只需要最近几天的数据。
func TestReadFiltersByTime(t *testing.T) {
	instance, _ := newTestJournal(t)

	base := time.Now()
	instance.Append(Record{Time: base.AddDate(0, 0, -10), Kind: KindChat})
	instance.Append(Record{Time: base.Add(-1 * time.Hour), Kind: KindChat})
	instance.Append(Record{Time: base, Kind: KindChat})

	records, err := instance.Read(base.Add(-2 * time.Hour))
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(records) != 2 {
		t.Fatalf("条数 = %d，期望 2", len(records))
	}
}

// TestReadSkipsShardsBeforeSince 分片名即日期，比 since 早的整片直接跳过——
// 这是按天分片白拿的收益：读取量跟查询天数有关，跟历史总量无关。
func TestReadSkipsShardsBeforeSince(t *testing.T) {
	instance, dir := newTestJournal(t)

	// 对齐到秒：分片里存的是 RFC3339（秒精度），带纳秒的基准会让
	// 边界那条记录在解析后被截断成「比 since 早」，从而被行级过滤掉。
	base := time.Now().Truncate(time.Second)
	for offset := -4; offset <= 0; offset++ {
		path := shardPathOf(dir, dayOffset(offset))
		line := `{"t":"` + base.AddDate(0, 0, offset).Format(time.RFC3339) + `","kind":"chat"}` + "\n"
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatalf("写测试分片失败：%v", err)
		}
	}

	// 只看最近 3 天（今天与前两天）。
	records, err := instance.Read(base.AddDate(0, 0, -2))
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(records) != 3 {
		t.Fatalf("条数 = %d，期望 3（更早的分片应被跳过）", len(records))
	}
}

// TestReadMissingDirectory 目录还没生成时返回空结果，不是错误。
func TestReadMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	instance, err := Open(dir, "gateway")
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer instance.Close()
	os.RemoveAll(dir)

	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("目录不存在不该报错：%v", err)
	}
	if len(records) != 0 {
		t.Errorf("条数 = %d，期望 0", len(records))
	}
}

// TestReadSkipsCorruptedLines 进程被 kill 时可能写了一半，
// 一行坏数据不该让整次统计失败。
func TestReadSkipsCorruptedLines(t *testing.T) {
	instance, dir := newTestJournal(t)
	content := `{"t":"2026-10-14T04:00:00+08:00","kind":"chat","model":"glm-5.3"}
{"t":"2026-10-14T04:00:01+08:00","kin
{"t":"2026-10-14T04:00:02+08:00","kind":"chat","model":"hy3"}
`
	if err := os.WriteFile(shardPathOf(dir, "2026-10-14"), []byte(content), 0o600); err != nil {
		t.Fatalf("写入测试数据失败：%v", err)
	}

	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("坏行不该导致报错：%v", err)
	}
	if len(records) != 2 {
		t.Fatalf("条数 = %d，期望 2（跳过坏行）", len(records))
	}
	if records[1].Model != "hy3" {
		t.Errorf("坏行之后的数据丢了：%+v", records[1])
	}
}

// TestReadIgnoresForeignFiles 目录里混进别的东西时不能把读取搞崩。
// 用户完全可能把自己的笔记丢进这个目录。
func TestReadIgnoresForeignFiles(t *testing.T) {
	instance, dir := newTestJournal(t)
	instance.Append(Record{Kind: KindChat, Message: "正常一条"})

	for _, name := range []string{"notes.txt", "gateway-not-a-date.jsonl", "client-2026-10-14.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("杂项\n"), 0o600); err != nil {
			t.Fatalf("写杂项文件失败：%v", err)
		}
	}

	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("杂项文件不该让读取失败：%v", err)
	}
	if len(records) != 1 {
		t.Fatalf("条数 = %d，期望 1（只读本前缀的分片）", len(records))
	}
}

// TestAppendAfterClose 退出路径上会先停服务再关文件，
// 这时仍有事件进来的话不能 panic。
func TestAppendAfterClose(t *testing.T) {
	instance, _ := newTestJournal(t)
	if err := instance.Close(); err != nil {
		t.Fatalf("Close 报错：%v", err)
	}
	// 重复关闭要幂等。
	if err := instance.Close(); err != nil {
		t.Errorf("重复 Close 报错：%v", err)
	}
	instance.Append(Record{Kind: KindStop})
}

// TestConcurrentAppend 网关是并发的，多个请求同时写不能交错出坏行。
// 分片按天切换也不能让并发写入分裂成两行。
func TestConcurrentAppend(t *testing.T) {
	instance, _ := newTestJournal(t)

	const writers, perWriter = 8, 40
	var group sync.WaitGroup
	for i := 0; i < writers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < perWriter; j++ {
				instance.Append(Record{Kind: KindChat, Model: "glm-5.3"})
			}
		}()
	}
	group.Wait()
	instance.Close()

	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(records) != writers*perWriter {
		t.Fatalf("条数 = %d，期望 %d（有丢行或坏行）", len(records), writers*perWriter)
	}
}

// TestPruneRemovesOldShards 超过保留天数的分片要被删掉，保留期内一个不动。
func TestPruneRemovesOldShards(t *testing.T) {
	instance, dir := newTestJournal(t)

	// 铺 5 个分片：今天往前数 0～4 天。
	for offset := -4; offset <= 0; offset++ {
		line := `{"kind":"chat","msg":"第` + dayOffset(offset) + `天"}` + "\n"
		if err := os.WriteFile(shardPathOf(dir, dayOffset(offset)), []byte(line), 0o600); err != nil {
			t.Fatalf("写测试分片失败：%v", err)
		}
	}

	// 保留 3 天 = 今天加前两天，所以 -3、-4 两天该消失。
	removed, err := instance.Prune(3)
	if err != nil {
		t.Fatalf("Prune 失败：%v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("删了 %d 个分片，期望 2：%v", len(removed), removed)
	}
	for _, offset := range []int{-4, -3} {
		if _, err := os.Stat(shardPathOf(dir, dayOffset(offset))); !os.IsNotExist(err) {
			t.Errorf("%d 天前的分片应当被删掉", -offset)
		}
	}
	for _, offset := range []int{-2, -1, 0} {
		if _, err := os.Stat(shardPathOf(dir, dayOffset(offset))); err != nil {
			t.Errorf("%d 天前的分片不该被删：%v", -offset, err)
		}
	}
}

// TestPruneKeepsWhenDisabled 保留天数非法时宁可占磁盘，也不误删用户数据。
func TestPruneKeepsWhenDisabled(t *testing.T) {
	instance, dir := newTestJournal(t)
	path := shardPathOf(dir, dayOffset(-30))
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("写测试分片失败：%v", err)
	}

	removed, err := instance.Prune(0)
	if err != nil {
		t.Fatalf("Prune 报错：%v", err)
	}
	if len(removed) != 0 {
		t.Errorf("保留期为 0 时不该删任何东西：%v", removed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("分片被误删了：%v", err)
	}
}

// TestImportLegacy 旧版单文件要按记录时间拆进分片，且不动源文件——
// 用户的历史日志不能在升级时凭空消失。
func TestImportLegacy(t *testing.T) {
	instance, dir := newTestJournal(t)

	base := time.Now()
	content := `{"t":"` + base.AddDate(0, 0, -1).Format(time.RFC3339) + `","kind":"chat","msg":"昨天"}`
	content += "\n" + `{"t":"` + base.Format(time.RFC3339) + `","kind":"chat","msg":"今天"}` + "\n"
	content += "\n" + `{"kind":"chat","msg":"坏行` + "\n" // 损坏的最后一行
	legacy := filepath.Join(dir, "legacy.jsonl")
	if err := os.WriteFile(legacy, []byte(content), 0o600); err != nil {
		t.Fatalf("写旧事件文件失败：%v", err)
	}

	imported, err := instance.ImportLegacy(legacy)
	if err != nil {
		t.Fatalf("导入失败：%v", err)
	}
	if imported != 2 {
		t.Fatalf("导入 %d 条，期望 2（坏行跳过）", imported)
	}

	// 源文件必须还在：调用方负责改名，导入本身绝不删源。
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("源文件被动了：%v", err)
	}

	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(records) != 2 {
		t.Fatalf("读回 %d 条，期望 2", len(records))
	}
	if records[0].Message != "昨天" || records[1].Message != "今天" {
		t.Errorf("导入顺序不对：%q / %q", records[0].Message, records[1].Message)
	}
}

// TestImportLegacyMissingFile 没有旧文件时是正常情况，不该报错。
func TestImportLegacyMissingFile(t *testing.T) {
	instance, dir := newTestJournal(t)
	imported, err := instance.ImportLegacy(filepath.Join(dir, "不存在.jsonl"))
	if err != nil {
		t.Fatalf("文件不存在不该报错：%v", err)
	}
	if imported != 0 {
		t.Errorf("导入 %d 条，期望 0", imported)
	}
}

// TestEscapeHTMLIsDisabled 模型名与错误信息里可能出现 <>&，
// 被转义成 \u003c 之类的会让 jq 与 grep 都难用。
func TestEscapeHTMLIsDisabled(t *testing.T) {
	instance, _ := newTestJournal(t)
	instance.Append(Record{Kind: KindChat, Message: "上游 <html> 错误"})
	path := instance.Path()
	instance.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if strings.Contains(string(raw), `\u003c`) {
		t.Errorf("HTML 被转义了，文件不好读：%s", string(raw))
	}
}

// TestRedactPayloadsExpires 超过 TTL 的对话正文读取时应被脱敏为空，
// 而其它统计字段必须原样保留（统计不能因为脱敏而失真）。
func TestRedactPayloadsExpires(t *testing.T) {
	old := Record{
		Kind:         KindChat,
		Time:         time.Now().Add(-31 * 24 * time.Hour),
		RequestText:  "过期了的请求正文",
		ResponseText: "过期了的回复正文",
		Credit:       1.5,
		In:           100,
	}
	got := old.RedactPayloads(time.Now())
	if got.RequestText != "" || got.ResponseText != "" {
		t.Errorf("过期正文应被清空，得到 req=%q resp=%q", got.RequestText, got.ResponseText)
	}
	if got.Credit != 1.5 || got.In != 100 {
		t.Error("脱敏不该动到统计字段")
	}
}

// TestRedactPayloadsKeepsFresh 未过期的正文必须原样保留，
// 否则「点开详情看被拦内容」就失效了。
func TestRedactPayloadsKeepsFresh(t *testing.T) {
	fresh := Record{
		Kind:        KindChat,
		Time:        time.Now().Add(-2 * 24 * time.Hour),
		RequestText: "今天被拦的请求",
	}
	got := fresh.RedactPayloads(time.Now())
	if got.RequestText != "今天被拦的请求" {
		t.Errorf("未过期正文应保留，得到 %q", got.RequestText)
	}
}

// TestRedactPayloadsSkipsEmpty 没有正文的事件直接原样返回，不做多余拷贝。
func TestRedactPayloadsSkipsEmpty(t *testing.T) {
	old := Record{Kind: KindChat, Time: time.Now().Add(-99 * 24 * time.Hour), Credit: 3}
	got := old.RedactPayloads(time.Now())
	if got.Credit != 3 || got.RequestText != "" {
		t.Errorf("无正文事件应原样返回，得到 %+v", got)
	}
}

// TestAppendTruncatesHugePayload 写入时就必须给正文设上限。
//
// 这是从一个真实故障来的：失败事件会留存请求/响应正文，而正文没有上限，
// 一次大请求写下几百 KB 的一行，之后读取器一炸，整个面板的事件都读不出来
// （「读取事件文件失败：bufio.Scanner: token too long」）。
func TestAppendTruncatesHugePayload(t *testing.T) {
	instance, _ := newTestJournal(t)

	huge := strings.Repeat("啊", payloadLimit) // 3 字节一个字符，远超上限
	instance.Append(Record{
		Kind:         KindChat,
		RequestText:  huge,
		ResponseText: huge,
	})

	info, err := os.Stat(instance.Path())
	if err != nil {
		t.Fatalf("Stat 失败：%v", err)
	}
	// 两个正文各截到上限附近；再加 JSON 转义与其它字段，留一倍余量。
	if info.Size() > 4*payloadLimit {
		t.Errorf("落盘 %d 字节，正文没有截断", info.Size())
	}

	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	if len(records) != 1 {
		t.Fatalf("读回 %d 条，期望 1 条", len(records))
	}
	if !strings.Contains(records[0].RequestText, "已截断") {
		t.Error("截断后应当留下标记，否则用户以为正文就这么长")
	}
	// 截在字符边界上：整条记录必须是合法的 UTF-8，不能出现半个汉字。
	if !utf8.ValidString(records[0].RequestText) {
		t.Error("截断切在了多字节字符中间，尾巴是乱码")
	}
}

// TestReadSkipsOverLongLine 读取超长行时跳过它继续读，而不是整次失败。
//
// 老版本写下的文件里已经有这样的行，用户不该为此丢掉全部历史。
func TestReadSkipsOverLongLine(t *testing.T) {
	instance, dir := newTestJournal(t)

	var builder strings.Builder
	builder.WriteString(`{"kind":"chat","msg":"第一条"}` + "\n")
	builder.WriteString(`{"kind":"chat","msg":"` + strings.Repeat("x", maxLineBytes+1024) + `"}` + "\n")
	builder.WriteString(`{"kind":"chat","msg":"第三条"}` + "\n")
	if err := os.WriteFile(shardPathOf(dir, dayOffset(0)), []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("写测试文件失败：%v", err)
	}

	records, err := instance.Read(time.Time{})
	if err != nil {
		t.Fatalf("超长行不该让整次读取失败：%v", err)
	}
	if len(records) != 2 {
		t.Fatalf("读回 %d 条，期望 2 条（超长的那条被跳过）", len(records))
	}
	if records[0].Message != "第一条" || records[1].Message != "第三条" {
		t.Errorf("读回的不是首尾两条：%q / %q", records[0].Message, records[1].Message)
	}
}
