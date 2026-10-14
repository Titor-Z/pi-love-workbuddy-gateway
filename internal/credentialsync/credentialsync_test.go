package credentialsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDecideCoversAllEpochRelations 三种大小关系必须各自有明确结论，
// 且「本地更大」必须是 Conflict——这是「不自动覆盖」这条地基的落点。
func TestDecideCoversAllEpochRelations(t *testing.T) {
	cases := []struct {
		local, remote int64
		want          Decision
	}{
		{-1, 1, Accept},          // 首次同步
		{3, 7, Accept},           // 网关更新
		{7, 7, AcceptIdempotent}, // 重复同步
		{9, 7, Conflict},         // 本地更可能新
		{9, 1, Conflict},         // 网关归零后重来
	}
	for _, c := range cases {
		if got := Decide(c.local, c.remote); got != c.want {
			t.Errorf("Decide(%d,%d) = %v，期望 %v", c.local, c.remote, got, c.want)
		}
	}
}

// TestDecideNeverSilentlyOverwriteNewerLocal 本地更新时绝不能被静默覆盖。
// 这条是地基：自动合并会丢东西，静默采用网关会丢外出期间的刷新。
func TestDecideNeverSilentlyOverwriteNewerLocal(t *testing.T) {
	if got := Decide(10, 2); got == Accept || got == AcceptIdempotent {
		t.Fatalf("本地 epoch 更大时返回了 %v——这会静默丢凭证", got)
	}
}

// TestDecisionStringsAreReadable 这些字符串会出现在面板和日志里，
// 必须是给人看的，不能是枚举名。
func TestDecisionStringsAreReadable(t *testing.T) {
	for _, decision := range []Decision{Accept, AcceptIdempotent, Conflict, RejectStale} {
		text := decision.String()
		if text == "" || text == "未知" {
			t.Errorf("%d 的中文说明不可用：%q", int(decision), text)
		}
	}
}

// TestValidateRejectsMalformedEnvelopes 结构不完整的下发必须被拒。
// 少了 accessToken 的信封如果被接受，客户端会落一份用不了的凭证，
// 外出时才发现——那时已经来不及同步了。
func TestValidateRejectsMalformedEnvelopes(t *testing.T) {
	cases := map[string]Envelope{
		"epoch 为零":  {Epoch: 0, Credential: map[string]any{"auth": map[string]any{"accessToken": "t"}}},
		"epoch 为负":  {Epoch: -5, Credential: map[string]any{"auth": map[string]any{"accessToken": "t"}}},
		"凭证为空":      {Epoch: 1, Credential: map[string]any{}},
		"缺少 auth 段": {Epoch: 1, Credential: map[string]any{"account": map[string]any{}}},
		"auth 类型不对": {Epoch: 1, Credential: map[string]any{"auth": "oops"}},
		"缺少访问令牌":    {Epoch: 1, Credential: map[string]any{"auth": map[string]any{"refreshToken": "r"}}},
		"访问令牌为空串":   {Epoch: 1, Credential: map[string]any{"auth": map[string]any{"accessToken": ""}}},
	}
	for name, envelope := range cases {
		if err := envelope.Validate(); err == nil {
			t.Errorf("%s：应当被拒绝", name)
		}
	}

	good := Envelope{Epoch: 1, Credential: map[string]any{
		"auth": map[string]any{"accessToken": "token"},
	}}
	if err := good.Validate(); err != nil {
		t.Errorf("合法的信封被拒了：%v", err)
	}
}

// TestValidateIgnoresUnknownFields 登录态里有 account、accounts 等未识别字段，
// 必须照搬不裁剪——裁剪过的工作簿会让 WorkBuddy 客户端判定完整性错误。
func TestValidateIgnoresUnknownFields(t *testing.T) {
	envelope := Envelope{Epoch: 1, Credential: map[string]any{
		"auth":    map[string]any{"accessToken": "t"},
		"account": map[string]any{"uid": "123"},
		"future":  []any{1, 2, 3},
	}}
	if err := envelope.Validate(); err != nil {
		t.Errorf("含未识别字段的信封被拒了：%v", err)
	}
}

// TestEpochRoundTrip epoch 必须能原样往返。
// 存的是一个整数，读出时被当成字符串或丢了前导零都会造成误判。
func TestEpochRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential-epoch")

	for _, want := range []int64{1, 7, 42, 1 << 40} {
		if err := WriteEpoch(path, want); err != nil {
			t.Fatalf("写 %d 失败：%v", want, err)
		}
		got, err := ReadEpoch(path)
		if err != nil {
			t.Fatalf("读失败：%v", err)
		}
		if got != want {
			t.Errorf("往返后 = %d，期望 %d", got, want)
		}
	}
}

// TestReadEpochMissingIsZeroNotError 文件不存在就是还没同步过，
// 不是错误——首次启动必然走到这里。
func TestReadEpochMissingIsZeroNotError(t *testing.T) {
	got, err := ReadEpoch(filepath.Join(t.TempDir(), "不存在"))
	if err != nil {
		t.Fatalf("文件不该导致报错：%v", err)
	}
	if got != 0 {
		t.Errorf("首次读取 = %d，期望 0", got)
	}
}

// TestReadEpochRejectsGarbage 文件被写坏了要报错，不能默默当成 0——
// 那会让客户端以为自己从没同步过，从而接受一份更旧的凭证。
func TestReadEpochRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-epoch")
	if err := os.WriteFile(path, []byte("不是数字"), 0o600); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if _, err := ReadEpoch(path); err == nil {
		t.Fatal("坏文件应当报错")
	}
}

// TestNextEpochIncrements 每次成功同步 +1。
func TestNextEpochIncrements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-epoch")
	for want := int64(1); want <= 5; want++ {
		got, err := NextEpoch(path)
		if err != nil {
			t.Fatalf("第 %d 次失败：%v", want, err)
		}
		if got != want {
			t.Fatalf("第 %d 次得到 %d", want, got)
		}
	}
}

// TestWriteEpochIsAtomicAndPrivate 计数器是权威状态，
// 写一半留下坏文件会让客户端误判新旧；权限必须是 0600。
func TestWriteEpochIsAtomicAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credential-epoch")
	if err := WriteEpoch(path, 9); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	// 临时文件必须已被改名走，不能留在目录里。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("读目录失败：%v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("留下了临时文件 %s", entry.Name())
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限 = %o，期望 600", perm)
	}
}

// TestClientEpochAfterGoingGateway 切 gateway 时 epoch = 同步来的 + 1。
// 语义是「这台机器出去自己刷过」——回连时网关据此知道该拒绝覆盖。
func TestClientEpochAfterGoingGateway(t *testing.T) {
	synced := int64(7)
	if got := ClientEpochAfterGoingGateway(synced); got != 8 {
		t.Errorf("切 gateway 后 epoch = %d，期望 8", got)
	}
}

// TestBackoffGrowsThenCaps 退避必须封顶，否则一次长时间断网会让它
// 几小时都不再尝试——用户回家后会以为同步坏了。
func TestBackoffGrowsThenCaps(t *testing.T) {
	schedule := []int64{1, 2, 4, 8, 16, 30, 30, 30}
	for attempt := 0; attempt < len(schedule); attempt++ {
		if got := BackoffMinutes(attempt); got != schedule[attempt] {
			t.Errorf("第 %d 次失败后等待 %d 分钟，期望 %d",
				attempt, got, schedule[attempt])
		}
	}
}

// TestNormalizeRefreshedAt 网关时间戳可能带时区也可能不带，
// 比较前必须统一，否则「是否比上次新」会给出错误答案。
func TestNormalizeRefreshedAt(t *testing.T) {
	cases := map[string]string{
		"2026-10-15T10:00:00+08:00": "2026-10-15T02:00:00Z",
		"2026-10-15T02:00:00Z":      "2026-10-15T02:00:00Z",
		"":                          "",
		"不是时间":                      "",
	}
	for input, want := range cases {
		if got := NormalizeRefreshedAt(input); got != want {
			t.Errorf("NormalizeRefreshedAt(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestRefreshedAtIsNewer 只有网关确实刷新过才立刻再拉一次。
func TestRefreshedAtIsNewer(t *testing.T) {
	if !RefreshedAtIsNewer("2026-10-15T03:00:00Z", "2026-10-15T02:00:00Z") {
		t.Error("更晚的时间应当判为新")
	}
	if RefreshedAtIsNewer("2026-10-15T01:00:00Z", "2026-10-15T02:00:00Z") {
		t.Error("更早的时间不该判为新")
	}
	if RefreshedAtIsNewer("", "2026-10-15T02:00:00Z") {
		t.Error("空值不该判为新")
	}
	if RefreshedAtIsNewer("不是时间", "2026-10-15T02:00:00Z") {
		t.Error("无法解析的值不该判为新")
	}
}

// TestEnvelopeSurvivesJSONRoundTrip 明文凭证要过网络，
// 序列化往返必须无损——尤其数字不能被改成 float 精度丢失的形式。
func TestEnvelopeSurvivesJSONRoundTrip(t *testing.T) {
	original := Envelope{
		Epoch:              9,
		GatewayRefreshedAt: "2026-10-15T02:00:00Z",
		Credential: map[string]any{
			"auth": map[string]any{
				"accessToken":  map[string]any{"$wbEncrypted": float64(1)},
				"refreshToken": "r",
			},
			"account": map[string]any{"uid": "123"},
		},
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	var decoded Envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	if decoded.Epoch != original.Epoch ||
		decoded.GatewayRefreshedAt != original.GatewayRefreshedAt {
		t.Errorf("往返后头部不一致：%+v", decoded)
	}
	auth := decoded.Credential["auth"].(map[string]any)
	if auth["refreshToken"] != "r" {
		t.Error("auth 段在往返中丢了字段")
	}
}

// TestParseEpochTolerantOfWhitespace 手工编辑过的文件带个换行不该导致误判。
func TestParseEpochTolerantOfWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-epoch")
	if err := os.WriteFile(path, []byte("  42\n"), 0o600); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	got, err := ReadEpoch(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if got != 42 {
		t.Errorf("读到的 epoch = %d，期望 42", got)
	}
}

// TestLocalFileRoundTrip 本地副本必须能原样往返。
func TestLocalFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	want := LocalFile{
		Epoch:      7,
		SyncedAt:   time.Now().UTC().Truncate(time.Second),
		Active:     false,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "sealed"}},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	got, exists, err := Load(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if !exists {
		t.Fatal("应当报告副本存在")
	}
	if got.Epoch != want.Epoch || got.Active != want.Active {
		t.Errorf("往返后不一致：%+v", got)
	}
	if !got.SyncedAt.Equal(want.SyncedAt) {
		t.Errorf("时间往返后 = %v，期望 %v", got.SyncedAt, want.SyncedAt)
	}
}

// TestSaveRejectsBadInput 空凭证或 epoch 为负不能落盘。
// 落一份用不了的副本，用户要到外出时才发现。
func TestSaveRejectsBadInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	for name, file := range map[string]LocalFile{
		"epoch 为负": {Epoch: -1, Credential: map[string]any{"a": 1}},
		"epoch 为零": {Epoch: 0, Credential: map[string]any{"a": 1}},
		"凭证为空":     {Epoch: 1, Credential: map[string]any{}},
	} {
		if err := Save(path, file); err == nil {
			t.Errorf("%s：应当被拒绝", name)
		}
	}
}

// TestLoadMissingReportsAbsence 没有副本要能区分出来，
// 面板据此提示「先去同步」，而不是报一个读文件的错。
func TestLoadMissingReportsAbsence(t *testing.T) {
	_, exists, err := Load(filepath.Join(t.TempDir(), "不存在.json"))
	if err != nil {
		t.Fatalf("文件不存在不该报错：%v", err)
	}
	if exists {
		t.Error("不存在的文件不该报告为存在")
	}
}

// TestSetActiveTogglesWithoutTouchingCredential 切回 client 模式只改标记，
// 不动凭证本身——那份加密副本要留着下次出门用。
func TestSetActiveTogglesWithoutTouchingCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	original := LocalFile{
		Epoch:      3,
		SyncedAt:   time.Now().UTC(),
		Active:     true,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "sealed"}},
	}
	if err := Save(path, original); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if err := SetActive(path, false); err != nil {
		t.Fatalf("置为非活跃失败：%v", err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if got.Active {
		t.Error("活跃标记没有被改掉")
	}
	if fmt.Sprint(got.Credential) != fmt.Sprint(original.Credential) {
		t.Error("凭证内容被顺带改动了")
	}
	// 幂等：再设一次同样的值不该报错。
	if err := SetActive(path, false); err != nil {
		t.Errorf("重复设置同样的值报错了：%v", err)
	}
}

// TestSetActiveWithoutFile 没有副本时不能凭空造一个只有标记的文件。
func TestSetActiveWithoutFile(t *testing.T) {
	if err := SetActive(filepath.Join(t.TempDir(), "x.json"), true); err == nil {
		t.Error("没有副本时应当报错")
	}
}

// TestSaveIsPrivate 这份文件间接持有账号访问权，权限必须 0600。
func TestSaveIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credentials.json")
	if err := Save(path, LocalFile{
		Epoch:      1,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "sealed"}},
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限 = %o，期望 600", perm)
	}
}
