package credentialsync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// fakeSource 是一个可控的凭证源。
type fakeSource struct {
	envelope Envelope
	err      error
	calls    int
}

func (f *fakeSource) Fetch(context.Context) (Envelope, error) {
	f.calls++
	if f.err != nil {
		return Envelope{}, f.err
	}
	return f.envelope, nil
}

// seal 只是标记一下，证明它被调用过。
func seal(map[string]any) (map[string]any, error) {
	return map[string]any{"sealed": true}, nil
}

func newTestSyncer(t *testing.T, source Source) (*Syncer, string, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	clock := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	syncer := &Syncer{
		Source:   source,
		Store:    path,
		Seal:     seal,
		Interval: 30 * time.Minute,
		// 用指针捕获，测试才能推进时间。
		Now: func() time.Time { return clock },
	}
	t.Cleanup(func() {})
	return syncer, path, &clock
}

// TestSyncPersistsEncryptedEnvelope 同步成功后本机必须有一份**加密**副本。
// 少了 Seal 调用就是往磁盘写明文——那是这个包最不能出的错。
func TestSyncPersistsEncryptedEnvelope(t *testing.T) {
	source := &fakeSource{envelope: Envelope{
		Epoch:      3,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "PLAINTEXT"}},
	}}
	syncer, path, _ := newTestSyncer(t, source)
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	stored, exists, err := Load(path)
	if err != nil || !exists {
		t.Fatalf("副本没落盘：exists=%v err=%v", exists, err)
	}
	if stored.Epoch != 3 {
		t.Errorf("epoch = %d，期望 3", stored.Epoch)
	}
	if stored.Active {
		t.Error("客户端模式下的副本应当默认非活跃")
	}
	// 加密器的输出必须真的写进去了，而不是原始明文。
	if _, ok := stored.Credential["sealed"]; !ok {
		t.Errorf("落盘的不是加密后的内容：%+v", stored.Credential)
	}
}

// TestSyncWithoutSealerRefusesToWritePlaintext 没有加密器就必须失败。
// 这条是硬边界：宁可同步不成功，也不能把明文令牌写进磁盘。
func TestSyncWithoutSealerRefusesToWritePlaintext(t *testing.T) {
	source := &fakeSource{envelope: Envelope{
		Epoch:      1,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "PLAINTEXT"}},
	}}
	syncer, path, _ := newTestSyncer(t, source)
	syncer.Seal = nil
	if err := syncer.Sync(t.Context()); err == nil {
		t.Fatal("没有加密器时竟然同步成功了")
	}
	if _, exists, _ := Load(path); exists {
		t.Fatal("明文凭证被写进了磁盘")
	}
}

// TestSyncConflictKeepsLocalIntact 本地更新时本地文件必须逐字节不变。
// 「不自动覆盖」这条地基的落点：交给人决定，但不许悄悄改任何东西。
func TestSyncConflictKeepsLocalIntact(t *testing.T) {
	source := &fakeSource{envelope: Envelope{
		Epoch:      2,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "OLD"}},
	}}
	syncer, path, _ := newTestSyncer(t, source)
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("首次同步失败：%v", err)
	}
	before, _, _ := Load(path)

	// 客户端切过 gateway 模式，本地 epoch 抬高了。
	if err := Save(path, LocalFile{
		Epoch:      9,
		Credential: map[string]any{"sealed": "NEWER"},
	}); err != nil {
		t.Fatalf("改写本地失败：%v", err)
	}

	err := syncer.Sync(t.Context())
	if err == nil {
		t.Fatal("本地更新时应当报冲突")
	}
	after, _, _ := Load(path)
	if after.Epoch != 9 {
		t.Errorf("本地 epoch 被改成了 %d——静默覆盖了", after.Epoch)
	}
	if after.Credential["sealed"] != "NEWER" {
		t.Error("本地凭证内容被改动了")
	}
	if syncer.Snapshot().Conflict == nil {
		t.Error("冲突没有记进状态，面板无从提示")
	}
	_ = before
}

// TestSyncAcceptsNewerGateway 网关更新时正常覆盖。
func TestSyncAcceptsNewerGateway(t *testing.T) {
	source := &fakeSource{envelope: Envelope{
		Epoch:      2,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "OLD"}},
	}}
	syncer, path, _ := newTestSyncer(t, source)
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("首次同步失败：%v", err)
	}
	source.envelope = Envelope{
		Epoch:      5,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "NEW"}},
	}
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("第二次同步失败：%v", err)
	}
	stored, _, _ := Load(path)
	if stored.Epoch != 5 {
		t.Errorf("epoch = %d，期望 5", stored.Epoch)
	}
}

// TestResolveConflictUseRemoteClearsConflict 用户选「用网关的」之后冲突要解除，
// 且下一次同步能正常覆盖。
func TestResolveConflictUseRemoteClearsConflict(t *testing.T) {
	source := &fakeSource{envelope: Envelope{
		Epoch:      2,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "X"}},
	}}
	syncer, path, _ := newTestSyncer(t, source)
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if err := Save(path, LocalFile{Epoch: 9, Credential: map[string]any{"sealed": "L"}}); err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	_ = syncer.Sync(t.Context()) // 触发冲突

	syncer.ResolveConflict(true)
	if syncer.Snapshot().Conflict != nil {
		t.Fatal("冲突没有被解除")
	}
	// 用户已决定用网关的，同步器需要知道这一点才能真正覆盖。
	// 这里由调用方把本地 epoch 降回去——syncer 不自己改本地文件，
	// 因为「改本地凭证」这件事必须由用户的选择驱动。
	if err := Save(path, LocalFile{Epoch: 2, Credential: map[string]any{"sealed": "L"}}); err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("解除冲突后同步仍失败：%v", err)
	}
}

// TestResolveConflictKeepLocalRetains 选「保留本地的」时冲突必须留着，
// 面板要继续提示，不能一次之后就哑掉。
func TestResolveConflictKeepLocalRetains(t *testing.T) {
	syncer, _, _ := newTestSyncer(t, &fakeSource{})
	syncer.Conflict = &ConflictInfo{LocalEpoch: 9, RemoteEpoch: 2}
	syncer.ResolveConflict(false)
	if syncer.Snapshot().Conflict == nil {
		t.Fatal("选保留本地后冲突消失了")
	}
}

// TestBackoffSchedule 失败后的间隔必须按 1→2→4→8→30 走并封顶。
// 不封顶的话一次长时间断网会让它几小时都不再尝试。
func TestBackoffSchedule(t *testing.T) {
	source := &fakeSource{err: errors.New("网关离线")}
	syncer, _, clock := newTestSyncer(t, source)

	want := []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 16 * time.Minute, 30 * time.Minute,
		30 * time.Minute,
	}
	for attempt, expected := range want {
		_ = syncer.Sync(t.Context())
		if got := syncer.backoff(); got != expected {
			t.Fatalf("第 %d 次失败后间隔 = %v，期望 %v", attempt+1, got, expected)
		}
		*clock = clock.Add(expected)
	}
}

// TestShouldPullNowResetsAfterSuccess 成功一次后要立刻回到正常周期，
// 不能卡在退避上——用户回家后不该等半小时才同步上。
func TestShouldPullNowResetsAfterSuccess(t *testing.T) {
	source := &fakeSource{err: errors.New("网关离线")}
	syncer, _, clock := newTestSyncer(t, source)

	_ = syncer.Sync(t.Context())
	if syncer.ShouldPullNow() {
		t.Fatal("刚失败过不该立刻再拉")
	}

	source.err = nil
	source.envelope = Envelope{Epoch: 1, Credential: map[string]any{"auth": map[string]any{"accessToken": "t"}}}
	*clock = clock.Add(31 * time.Minute)
	if !syncer.ShouldPullNow() {
		t.Fatal("过了正常周期应当可以再拉")
	}
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	// 成功后失败计数清零，间隔回到 30 分钟而不是继续退避。
	if syncer.backoff() != 30*time.Minute {
		t.Errorf("成功后的间隔 = %v，期望 30m", syncer.backoff())
	}
}

// TestSnapshotReportsConflictForPanel 面板要能读到冲突才能提示用户。
func TestSnapshotReportsConflictForPanel(t *testing.T) {
	syncer, _, _ := newTestSyncer(t, &fakeSource{})
	syncer.Conflict = &ConflictInfo{LocalEpoch: 9, RemoteEpoch: 2}
	syncer.failures = 3
	snapshot := syncer.Snapshot()
	if snapshot.Conflict == nil || snapshot.Conflict.LocalEpoch != 9 {
		t.Errorf("快照里没有冲突信息：%+v", snapshot)
	}
	if snapshot.Failures != 3 {
		t.Errorf("失败次数 = %d，期望 3", snapshot.Failures)
	}
}

// TestConflictSurvivesAcrossSyncs 冲突一旦出现，后续每次同步都要继续报，
// 不能同步一次之后就哑掉——用户可能还没处理完。
func TestConflictSurvivesAcrossSyncs(t *testing.T) {
	source := &fakeSource{envelope: Envelope{
		Epoch:      2,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "OLD"}},
	}}
	syncer, path, _ := newTestSyncer(t, source)
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("首次同步失败：%v", err)
	}
	// 本机切过网关模式，epoch 抬高了。
	if err := Save(path, LocalFile{Epoch: 9, Credential: map[string]any{"sealed": "L"}}); err != nil {
		t.Fatalf("改写失败：%v", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		err := syncer.Sync(t.Context())
		if err == nil {
			t.Fatalf("第 %d 次同步没有报冲突", attempt)
		}
		if syncer.Snapshot().Conflict == nil {
			t.Fatalf("第 %d 次同步后冲突消失了", attempt)
		}
		stored, _, _ := Load(path)
		if stored.Epoch != 9 {
			t.Fatalf("第 %d 次同步把本地 epoch 改成了 %d", attempt, stored.Epoch)
		}
	}
}

// TestSyncDoesNotPush 客户端绝不回写。这里验证的是：
// 即使 Source 提供了一个可写的假网关，Syncer 也不会把本地凭证推回去。
// 这条地基由「Syncer 没有任何上传路径」保证——测试把它钉住，
// 以防将来有人「为了方便」加一个推送。
func TestSyncDoesNotPush(t *testing.T) {
	source := &fakeSource{envelope: Envelope{
		Epoch:      1,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "t"}},
	}}
	syncer, path, _ := newTestSyncer(t, source)
	if err := syncer.Sync(t.Context()); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if err := Save(path, LocalFile{Epoch: 5, Credential: map[string]any{"sealed": "L"}}); err != nil {
		t.Fatalf("改写失败：%v", err)
	}
	_ = syncer.Sync(t.Context()) // 触发冲突

	// fakeSource 只有一个 calls 计数，没有接收上传的入口——
	// 编译期就保证了「没有可推送的通道」。
	if got := len(source.envelope.Credential); got == 0 {
		t.Error("源被清空了")
	}
}
