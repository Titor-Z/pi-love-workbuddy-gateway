package overridessync

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
)

// table 用一组 (model → alias) 造一张覆盖表，revision 由调用方给。
func table(revision int64, entries map[string]string) *catalog.Overrides {
	out := catalog.NewOverrides()
	out.Revision = revision
	for model, alias := range entries {
		out.Models[model] = catalog.Override{Alias: alias}
	}
	return out
}

// TestMergeThreeWay 三方合并的七种情形。
// 判据是内容而不是时间戳：这些表里根本没有 updated_at。
func TestMergeThreeWay(t *testing.T) {
	cases := []struct {
		name      string
		base      map[string]string
		local     map[string]string
		remote    map[string]string
		want      map[string]string
		conflicts []string
	}{
		{
			name:   "都没改",
			base:   map[string]string{"a": "A"},
			local:  map[string]string{"a": "A"},
			remote: map[string]string{"a": "A"},
			want:   map[string]string{"a": "A"},
		},
		{
			name:   "只有本地改",
			base:   map[string]string{"a": "旧"},
			local:  map[string]string{"a": "本机改的"},
			remote: map[string]string{"a": "旧"},
			want:   map[string]string{"a": "本机改的"},
		},
		{
			name:   "只有网关改",
			base:   map[string]string{"a": "旧"},
			local:  map[string]string{"a": "旧"},
			remote: map[string]string{"a": "网关改的"},
			want:   map[string]string{"a": "网关改的"},
		},
		{
			name:   "两边改成一样",
			base:   map[string]string{"a": "旧"},
			local:  map[string]string{"a": "新"},
			remote: map[string]string{"a": "新"},
			want:   map[string]string{"a": "新"},
		},
		{
			name:      "两边都改且不同 → 冲突，谁都不覆盖",
			base:      map[string]string{"a": "旧"},
			local:     map[string]string{"a": "本机"},
			remote:    map[string]string{"a": "网关"},
			want:      map[string]string{},
			conflicts: []string{"a"},
		},
		{
			name:   "本地新增",
			base:   map[string]string{},
			local:  map[string]string{"b": "新的"},
			remote: map[string]string{},
			want:   map[string]string{"b": "新的"},
		},
		{
			name:   "网关删除，本地没动 → 跟着删",
			base:   map[string]string{"a": "A"},
			local:  map[string]string{"a": "A"},
			remote: map[string]string{},
			want:   map[string]string{},
		},
		{
			name:   "本地删除，网关没动 → 跟着删",
			base:   map[string]string{"a": "A"},
			local:  map[string]string{},
			remote: map[string]string{"a": "A"},
			want:   map[string]string{},
		},
		{
			name:   "不同模型各改各的，互不干扰",
			base:   map[string]string{"a": "A", "b": "B"},
			local:  map[string]string{"a": "本机改的", "b": "B"},
			remote: map[string]string{"a": "A", "b": "网关改的"},
			want:   map[string]string{"a": "本机改的", "b": "网关改的"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			merged, conflicts := Merge(
				table(1, testCase.base), table(1, testCase.local), table(2, testCase.remote))
			got := map[string]string{}
			for model, entry := range merged {
				got[model] = entry.Alias
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("合并结果 = %v，期望 %v", got, testCase.want)
			}
			for model, alias := range testCase.want {
				if got[model] != alias {
					t.Errorf("模型 %s = %q，期望 %q", model, got[model], alias)
				}
			}
			if len(conflicts) != len(testCase.conflicts) {
				t.Fatalf("冲突数 = %d，期望 %d：%+v", len(conflicts), len(testCase.conflicts), conflicts)
			}
			for i, model := range testCase.conflicts {
				if conflicts[i].Model != model {
					t.Errorf("冲突 %d = %q，期望 %q", i, conflicts[i].Model, model)
				}
			}
		})
	}
}

// TestCountChanges 数「本机改了几处」，新增与删除都要算进去。
func TestCountChanges(t *testing.T) {
	base := table(1, map[string]string{"a": "A", "b": "B", "c": "C"})
	local := table(1, map[string]string{"a": "改了", "c": "C", "d": "新增"})
	if got := countChanges(base, local); got != 3 {
		t.Errorf("改动处数 = %d，期望 3（a 改、b 删、d 增）", got)
	}
}

// fakeTransport 记录同步过程中对网关做了什么，并在需要时返回 409。
type fakeTransport struct {
	remote     *catalog.Overrides
	stale      bool
	fetched    int
	pushed     int
	pushedBase int64
}

func (f *fakeTransport) FetchModelOverrides(context.Context) (*catalog.Overrides, error) {
	f.fetched++
	return f.remote, nil
}

func (f *fakeTransport) PushModelOverrides(_ context.Context, base int64,
	models map[string]catalog.Override) (*catalog.Overrides, error) {
	f.pushed++
	f.pushedBase = base
	if f.stale {
		return nil, catalog.ErrStaleRevision
	}
	saved := catalog.NewOverrides()
	saved.Revision = base + 1
	saved.Models = models
	return saved, nil
}

// newSyncer 建一套本机文件 + Syncer。
func newSyncer(t *testing.T, transport Transport) (*Syncer, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := catalog.OpenStore(filepath.Join(dir, "model-overrides.json"))
	if err != nil {
		t.Fatalf("建 Store 失败：%v", err)
	}
	return &Syncer{
		Client:   transport,
		Local:    store,
		BasePath: filepath.Join(dir, "model-overrides.base.json"),
		Device:   "d1",
	}, dir
}

// seed 预置本机工作副本与共同祖先。
func seed(t *testing.T, syncer *Syncer, local, base *catalog.Overrides) {
	t.Helper()
	if _, err := syncer.Local.Replace(local.Models, "d1"); err != nil {
		t.Fatalf("写入本机表失败：%v", err)
	}
	if err := catalog.SaveOverrides(syncer.BasePath, base); err != nil {
		t.Fatalf("写入基准失败：%v", err)
	}
}

// TestSyncAdoptsWhenClean 本机没改动时直接跟上网关，不该往回推。
func TestSyncAdoptsWhenClean(t *testing.T) {
	transport := &fakeTransport{remote: table(5, map[string]string{"a": "网关的"})}
	syncer, _ := newSyncer(t, transport)
	seed(t, syncer, table(1, nil), table(1, nil))

	status, err := syncer.Sync(t.Context(), PreferNone)
	if err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if !status.Adopted || status.Pushed {
		t.Fatalf("应当直接采纳、不该推送：%+v", status)
	}
	if transport.pushed != 0 {
		t.Error("本机没改动却往回推了")
	}
	// 本机与基准都要变成网关那一份，且版本号与网关一致。
	if got := syncer.Local.Snapshot(); got.Revision != 5 || got.Models["a"].Alias != "网关的" {
		t.Errorf("本机表没跟上：%+v", got)
	}
	base, err := catalog.LoadOverrides(syncer.BasePath)
	if err != nil || base.Revision != 5 {
		t.Errorf("基准没更新：%+v err=%v", base, err)
	}
}

// TestSyncPushesLocalChanges 本机有改动时合并后回推，并把基准对齐到新版本。
func TestSyncPushesLocalChanges(t *testing.T) {
	transport := &fakeTransport{remote: table(5, map[string]string{"a": "旧", "b": "网关改的"})}
	syncer, _ := newSyncer(t, transport)
	seed(t, syncer, table(5, map[string]string{"a": "本机改的", "b": "旧"}), table(5, map[string]string{"a": "旧", "b": "旧"}))

	status, err := syncer.Sync(t.Context(), PreferNone)
	if err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if !status.Pushed || status.Adopted {
		t.Fatalf("应当回推：%+v", status)
	}
	if transport.pushedBase != 5 {
		t.Errorf("推送基准 = %d，期望 5", transport.pushedBase)
	}
	local := syncer.Local.Snapshot()
	if local.Models["a"].Alias != "本机改的" || local.Models["b"].Alias != "网关改的" {
		t.Errorf("合并结果不对：%+v", local.Models)
	}
	if local.Revision != 6 {
		t.Errorf("本机版本 = %d，期望 6", local.Revision)
	}
}

// TestSyncReportsConflictWithoutPushing 冲突时只报告，不推、不写。
func TestSyncReportsConflictWithoutPushing(t *testing.T) {
	transport := &fakeTransport{remote: table(5, map[string]string{"a": "网关改的"})}
	syncer, _ := newSyncer(t, transport)
	seed(t, syncer, table(5, map[string]string{"a": "本机改的"}), table(5, map[string]string{"a": "旧"}))

	status, err := syncer.Sync(t.Context(), PreferNone)
	if err != nil {
		t.Fatalf("有冲突不该算错误：%v", err)
	}
	if len(status.Conflicts) != 1 || status.Conflicts[0].Model != "a" {
		t.Fatalf("没报出冲突：%+v", status)
	}
	if transport.pushed != 0 {
		t.Error("有冲突却还是推了——那会丢掉另一边的改动")
	}
	if got := syncer.Local.Snapshot().Models["a"].Alias; got != "本机改的" {
		t.Errorf("冲突时本机表不该被改：%q", got)
	}
}

// TestSyncStaleRevision 网关在合并期间又变了：要把「请再同步一次」说清楚。
func TestSyncStaleRevision(t *testing.T) {
	transport := &fakeTransport{
		remote: table(5, map[string]string{"a": "旧"}),
		stale:  true,
	}
	syncer, _ := newSyncer(t, transport)
	seed(t, syncer, table(5, map[string]string{"a": "本机改的"}), table(5, map[string]string{"a": "旧"}))

	_, err := syncer.Sync(t.Context(), PreferNone)
	if err == nil || !strings.Contains(err.Error(), "再同步一次") {
		t.Fatalf("应当提示重新同步：%v", err)
	}
	// 失败时不能把本机改动冲掉。
	if got := syncer.Local.Snapshot().Models["a"].Alias; got != "本机改的" {
		t.Errorf("失败时本机改动应当保留：%q", got)
	}
}

// TestStatusIsOffline 状态查询不能联网——面板每次打开都要看它。
func TestStatusIsOffline(t *testing.T) {
	transport := &fakeTransport{remote: table(5, nil)}
	syncer, _ := newSyncer(t, transport)
	seed(t, syncer, table(5, map[string]string{"a": "本机改的"}), table(5, map[string]string{"a": "旧"}))

	status := syncer.Status()
	if transport.fetched != 0 {
		t.Error("Status 不该联网")
	}
	if status.Dirty != 1 {
		t.Errorf("改动处数 = %d，期望 1", status.Dirty)
	}
}

// TestSyncMissingBaseIsEmpty 基准文件不存在（首次同步 / 用户主动重置）
// 时按空表处理，不报错。
func TestSyncMissingBaseIsEmpty(t *testing.T) {
	transport := &fakeTransport{remote: table(3, map[string]string{"a": "网关的"})}
	syncer, _ := newSyncer(t, transport)
	// 不 seed：既没有工作副本，也没有基准。
	status, err := syncer.Sync(t.Context(), PreferNone)
	if err != nil {
		t.Fatalf("首次同步不该失败：%v", err)
	}
	if !errors.Is(err, nil) || status.RemoteRevision != 3 {
		t.Fatalf("状态不对：%+v", status)
	}
}
