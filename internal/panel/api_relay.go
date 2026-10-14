package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
)

// relayPreference 是「是否开放转发服务」这一项偏好。
//
// 单独一个文件而不是塞进 config：它是会被面板改写的持久状态，
// 而 config 是启动参数，两件事的写入时机与责任人不同。
type relayPreference struct {
	Enabled bool `json:"enabled"`
}

// readRelayPreference 读出当前偏好；文件不存在或坏掉都当作未开启——
// 转发服务是**选择开启**的功能，默认值必须是关闭。
func readRelayPreference() bool {
	raw, err := os.ReadFile(appdir.RelayPreferencePath())
	if err != nil {
		return false
	}
	var preference relayPreference
	if json.Unmarshal(raw, &preference) != nil {
		return false
	}
	return preference.Enabled
}

// setRelayEnabled 改偏好。写入失败要让用户知道，而不是默默不生效。
func (p *Panel) setRelayEnabled(enabled bool) error {
	raw, err := json.MarshalIndent(relayPreference{Enabled: enabled}, "", "  ")
	if err != nil {
		return err
	}
	path := appdir.RelayPreferencePath()
	// 目录可能还不存在（首次开启），os.WriteFile 不会自己建。
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// relayResponse 描述转发服务（局域网客户端接入的那个端口）的状态。
//
// 这里**不返回密钥本身**，只给一个看不出内容的提示：
// 密钥出现在 /api/status 里，等于把账号开放给任何能访问面板的地方。
type relayResponse struct {
	Enabled     bool            `json:"enabled"`
	Address     string          `json:"address"`
	Fingerprint string          `json:"fingerprint"`
	PSKHint     string          `json:"psk_hint,omitempty"`
	Devices     []devices.Entry `json:"devices"`
}

// handleRelay 返回转发服务的状态与已登记的设备。
//
// 只在网关模式下有意义。客户端模式没有这个端口，也没有设备表——
// 它的面板显示「网关在线 / 本设备 ID」就够。
func (p *Panel) handleRelay(w http.ResponseWriter, _ *http.Request) {
	if !p.requireGatewayMode() {
		writeError(w, http.StatusNotImplemented, "not_configured", "客户端模式没有转发服务")
		return
	}
	response := relayResponse{
		Enabled:     p.deps.RelayEnabled,
		Address:     p.deps.RelayAddr,
		Fingerprint: p.deps.TLSFingerprint,
		PSKHint:     p.deps.PSKHint,
		Devices:     []devices.Entry{},
	}
	if p.deps.RelayEnabled && p.deps.Devices != nil {
		response.Devices = p.deps.Devices.List()
	}
	writeJSON(w, http.StatusOK, response)
}

// handleRevokeDevice 停用一台设备。
//
// 效力必须说实话：设备名是客户端自报的，持有密钥的人换一个 ID 就能重连。
// 所以这只是「挡住这个 ID」的管理手段，不是访问控制——
// 提示语写在这里，用户才不会在误操作后以为已经安全。
func (p *Panel) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "缺少设备 ID")
		return
	}
	if p.deps.Devices == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "当前模式没有设备表")
		return
	}
	if !p.deps.Devices.Revoke(id) {
		writeError(w, http.StatusNotFound, "not_found", "没有这台设备")
		return
	}
	p.deps.Logger.Printf("设备 %q 已被停用", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUnrevokeDevice 撤销停用。误操作要能回头，否则用户只能去删文件。
func (p *Panel) handleUnrevokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "缺少设备 ID")
		return
	}
	if p.deps.Devices == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "当前模式没有设备表")
		return
	}
	if !p.deps.Devices.Unrevoke(id) {
		writeError(w, http.StatusNotFound, "not_found", "没有这台设备")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// notClientMode 是网关模式专属功能的统一前置。
func (p *Panel) requireGatewayMode() bool {
	return p.deps.Mode == config.ModeGateway
}

// resetPSKRequest 只承载「确认一下」这一个字段。
type resetPSKRequest struct {
	Confirm bool `json:"confirm"`
}

// handleResetPSK 重置网关密钥。
//
// 它会**踢掉所有已配对客户端**——没有只踢一台的办法：设备 ID 由客户端自报，
// 换个 ID 就能绕过，所以按 ID 停用防不住人。这一条必须在 UI 上二次确认，
// 也要在响应里说清楚，免得用户以为「吊销」就是安全机制。
func (p *Panel) handleResetPSK(w http.ResponseWriter, r *http.Request) {
	if !p.requireGatewayMode() {
		writeError(w, http.StatusNotImplemented, "not_configured", "只有网关模式有密钥")
		return
	}
	var request resetPSKRequest
	_ = json.NewDecoder(r.Body).Decode(&request)
	if !request.Confirm {
		writeError(w, http.StatusBadRequest, "confirm_required",
			"重置会踢掉所有已配对客户端，且无法只踢一台；确认请带上 \"confirm\": true")
		return
	}
	newKey, err := identity.ResetPSK(appdir.PSKPath())
	if err != nil {
		p.fail(w, "重置密钥", err)
		return
	}
	p.deps.Logger.Printf("网关密钥已重置，%d 台设备需要重新配对", p.deviceCount())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		// 返回新密钥是必要的：用户要把它填进各台客户端。面板只在本机显示。
		"psk": newKey,
	})
}

// enableRelayRequest 表示「我要开转发服务」。
type enableRelayRequest struct {
	Enable bool `json:"enable"`
}

// handleEnableRelay 在下次启动时是否开放转发端口。
//
// 刻意做成「改配置 + 重启后生效」，不在运行时开关监听：运行时增删监听意味着
// 要处理正在建立的 TLS 连接与已连接会话，那部分复杂度的收益是省一次重启。
// 面板会提示用户点「重启」。
func (p *Panel) handleEnableRelay(w http.ResponseWriter, r *http.Request) {
	if !p.requireGatewayMode() {
		writeError(w, http.StatusNotImplemented, "not_configured", "只有网关模式有转发服务")
		return
	}
	var request enableRelayRequest
	_ = json.NewDecoder(r.Body).Decode(&request)
	if err := p.setRelayEnabled(request.Enable); err != nil {
		p.fail(w, "修改转发服务开关", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": request.Enable})
}

// deviceCount 已登记的客户端数量。
func (p *Panel) deviceCount() int {
	if p.deps.Devices == nil {
		return 0
	}
	return len(p.deps.Devices.List())
}

// panelReadRelayPreference 导给 main 用，避免它复制一份默认值的判断。
// 转发服务是选择开启的功能，读不到、读坏了一律当作关闭。
func PanelReadRelayPreference() bool { return readRelayPreference() }
