package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/optional"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/overridessync"
)

// OverridesSyncer 是客户端模式下与网关对齐模型覆盖表的能力。
//
// 网关模式没有它：网关就是权威，没有可同步的对象。抽成接口是为了让面板
// 不必知道 relay 与 TLS 的存在，也让测试能用一个假的同步器。
type OverridesSyncer interface {
	// Status 只读本地、不联网。
	Status() overridessync.Status
	// Sync 拉取、三方合并，必要时回推。prefer 决定冲突时以哪一边为准。
	Sync(ctx context.Context, prefer overridessync.Prefer) (overridessync.Status, error)
}

// handleOverridesStatus 返回同步状态：本机有几处改动、基准停在哪。
// 不联网——模型页每次打开都要看它，不该为它卡一次网络。
func (p *Panel) handleOverridesStatus(w http.ResponseWriter, _ *http.Request) {
	// 用 optional.Missing 而不是判等：网关模式传进来的可能是一个
	// 「类型化的 nil」（`(*overridessync.Syncer)(nil)`），判等判不出来，
	// 调用时就会 panic——而那是每次打开模型页都会走到的路径。
	if optional.Missing(p.deps.OverridesSyncer) {
		writeError(w, http.StatusNotImplemented, "not_configured",
			"网关模式不需要同步模型覆盖表：这台机器就是权威")
		return
	}
	writeJSON(w, http.StatusOK, p.deps.OverridesSyncer.Status())
}

// handleOverridesSync 执行一次同步。
//
// 请求体可带 prefer（local / remote）：冲突时以哪一边为准。
// 不带就不替用户决定，返回 200 + 冲突清单——冲突是**正常结果**，
// 不是「操作失败」，所以不用错误码。
func (p *Panel) handleOverridesSync(w http.ResponseWriter, r *http.Request) {
	if optional.Missing(p.deps.OverridesSyncer) {
		writeError(w, http.StatusNotImplemented, "not_configured",
			"网关模式不需要同步模型覆盖表：这台机器就是权威")
		return
	}
	var request struct {
		Prefer string `json:"prefer"`
	}
	// 请求体允许为空（用户直接点同步），解析失败不当错误。
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&request)
	prefer := overridessync.Prefer(request.Prefer)
	if prefer != overridessync.PreferNone && prefer != overridessync.PreferLocal &&
		prefer != overridessync.PreferRemote {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"prefer 只能是 local 或 remote")
		return
	}
	status, err := p.deps.OverridesSyncer.Sync(r.Context(), prefer)
	if err != nil {
		p.fail(w, "同步模型覆盖表", err)
		return
	}
	if len(status.Conflicts) == 0 {
		p.deps.Logger.Printf("模型覆盖表已与网关对齐（revision %d）", status.LocalRevision)
	}
	writeJSON(w, http.StatusOK, status)
}
