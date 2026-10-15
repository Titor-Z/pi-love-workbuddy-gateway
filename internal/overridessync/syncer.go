package overridessync

import (
	"context"
	"errors"
	"fmt"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
)

// Transport 是与网关之间传覆盖表的能力。抽窄接口是为了让测试不必
// 真的起一个 TLS 网关。
type Transport interface {
	FetchModelOverrides(ctx context.Context) (*catalog.Overrides, error)
	PushModelOverrides(ctx context.Context, baseRevision int64,
		models map[string]catalog.Override) (*catalog.Overrides, error)
}

// Syncer 把本机的覆盖表与网关那份对齐。
//
// 只做「拉、合并、可选回推」三件事，不自动决定冲突。
type Syncer struct {
	Client Transport
	// Local 是本机的工作副本，与面板共用同一个 Store。
	Local *catalog.Store
	// BasePath 存「上次同步时网关那一份」——三方合并的共同祖先。
	// 少了它就只能比时间戳，而跨机器的时钟不可靠。
	BasePath string
	// Device 是本机身份，写进 updated_by。
	Device string
}

// Status 是一次同步的结果，也是面板要显示的东西。
type Status struct {
	LocalRevision  int64      `json:"local_revision"`
	BaseRevision   int64      `json:"base_revision"`
	RemoteRevision int64      `json:"remote_revision"`
	Dirty          int        `json:"dirty"`   // 本机相对共同祖先改过的条目数
	Pushed         bool       `json:"pushed"`  // 这次是否真的回推了
	Adopted        bool       `json:"adopted"` // 本机没有改动，直接跟上网关
	Conflicts      []Conflict `json:"conflicts"`
}

// Status 只读本地，不联网。面板用它显示「本机有几处改动」。
func (s *Syncer) Status() Status {
	local := s.Local.Snapshot()
	base := s.loadBase()
	return Status{
		LocalRevision: local.Revision,
		BaseRevision:  base.Revision,
		Dirty:         countChanges(base, local),
		Conflicts:     nil,
	}
}

// Sync 做一次完整同步。prefer 决定冲突时以哪一边为准。
//
// prefer 为空且有冲突时**只报告、不推送、不写盘**：
// 同一条两边都改过，谁对谁错只有用户知道。
func (s *Syncer) Sync(ctx context.Context, prefer Prefer) (Status, error) {
	local := s.Local.Snapshot()
	base := s.loadBase()

	remote, err := s.Client.FetchModelOverrides(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("读取网关的模型覆盖表失败：%w", err)
	}
	status := Status{
		LocalRevision:  local.Revision,
		BaseRevision:   base.Revision,
		RemoteRevision: remote.Revision,
		Dirty:          countChanges(base, local),
	}
	merged, conflicts := MergePreferring(base, local, remote, prefer)
	if len(conflicts) > 0 {
		status.Conflicts = conflicts
		return status, nil
	}

	// 本机没有改动：直接把网关那份搬过来，不必推一次再拉一次。
	if status.Dirty == 0 {
		if err := s.adopt(remote); err != nil {
			return status, err
		}
		status.Adopted = true
		status.LocalRevision = remote.Revision
		status.BaseRevision = remote.Revision
		return status, nil
	}

	// 本机有改动：把合并结果推回去。base.Revision 让网关能发现
	// 「我们合并期间它又变了」，那时它会拒绝，我们重新来过。
	saved, err := s.Client.PushModelOverrides(ctx, base.Revision, merged)
	if errors.Is(err, catalog.ErrStaleRevision) {
		return status, fmt.Errorf("网关的覆盖表在合并期间又变了，请再同步一次")
	}
	if err != nil {
		return status, fmt.Errorf("推回模型覆盖表失败：%w", err)
	}
	if err := s.adopt(saved); err != nil {
		return status, err
	}
	status.Pushed = true
	status.Dirty = 0
	status.LocalRevision = saved.Revision
	status.BaseRevision = saved.Revision
	status.RemoteRevision = saved.Revision
	return status, nil
}

// adopt 把网关那份原样落到本机：工作副本与共同祖先都写成它。
//
// 走 Adopt 而不是 Replace：Replace 会递增 revision 并重打时间戳，
// 那会让本机的版本号与网关的越差越远，下次合并的基准就错了。
func (s *Syncer) adopt(remote *catalog.Overrides) error {
	if err := catalog.SaveOverrides(s.BasePath, remote); err != nil {
		return fmt.Errorf("写入同步基准失败：%w", err)
	}
	if err := s.Local.Adopt(remote); err != nil {
		return fmt.Errorf("写入本机模型覆盖表失败：%w", err)
	}
	return nil
}

// loadBase 读共同祖先。读不出来时退回空表而不是报错：
// 首次同步、或用户删了基准文件（= 主动重置）都是这个状态。
func (s *Syncer) loadBase() *catalog.Overrides {
	table, err := catalog.LoadOverrides(s.BasePath)
	if err != nil {
		return catalog.NewOverrides()
	}
	return table
}

// countChanges 数本机相对共同祖先改过（含新增与删除）的条目数。
func countChanges(base, local *catalog.Overrides) int {
	count := 0
	for _, model := range union(base, local) {
		baseEntry, inBase := lookup(base, model)
		localEntry, inLocal := lookup(local, model)
		if changed(*localEntry, value(baseEntry), inBase, inLocal) {
			count++
		}
	}
	return count
}
