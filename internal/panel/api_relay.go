package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
)

// relayPreference 是「是否开放转发服务」这一项偏好。
//
// 单独一个文件而不是塞进 config：它是会被面板改写的持久状态，
// 而 config 是启动参数，两件事的写入时机与责任人不同。
type relayPreference struct {
	// Enabled 是转发开关（客户端能否连入并转发请求）。
	Enabled bool `json:"enabled"`
	// CredentialSync 是凭证同步开关（客户端能否取走账号令牌）。
	// 独立一项：两者风险差一个量级，不该被同一个开关带过去。
	CredentialSync bool `json:"credential_sync"`
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

// writeRelayPreference 落盘偏好。先建目录：首次开启时目录可能还不存在。
func writeRelayPreference(preference relayPreference) error {
	raw, err := json.MarshalIndent(preference, "", "  ")
	if err != nil {
		return err
	}
	path := appdir.RelayPreferencePath()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// setRelayEnabled 改转发开关，保留凭证同步那一项不被顺带清掉。
func (p *Panel) setRelayEnabled(enabled bool) error {
	var preference relayPreference
	if raw, err := os.ReadFile(appdir.RelayPreferencePath()); err == nil {
		_ = json.Unmarshal(raw, &preference)
	}
	preference.Enabled = enabled
	return writeRelayPreference(preference)
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

// ReadSyncPreference 读「是否允许下发凭证」开关。
//
// 与转发开关是两件事：转发是把请求送进来，下发凭证是把账号令牌交出去，
// 后者风险高一个量级，必须让人分别想清楚。默认关；读不到或读坏了
// 一律当作关闭——一个坏文件不该把账号令牌偷偷开放出去。
func ReadSyncPreference() bool {
	raw, err := os.ReadFile(appdir.RelayPreferencePath())
	if err != nil {
		return false
	}
	var preference relayPreference
	if json.Unmarshal(raw, &preference) != nil {
		return false
	}
	return preference.CredentialSync
}

// SetSyncPreference 改凭证同步开关，保留转发开关不被顺带清掉。
func SetSyncPreference(enabled bool) error {
	var preference relayPreference
	if raw, err := os.ReadFile(appdir.RelayPreferencePath()); err == nil {
		_ = json.Unmarshal(raw, &preference)
	}
	preference.CredentialSync = enabled
	return writeRelayPreference(preference)
}

// syncResponse 是凭证同步的状态。
type syncResponse struct {
	Enabled      bool                  `json:"enabled"`
	GatewayEpoch int64                 `json:"gateway_epoch"`
	Local        *credentialsync.State `json:"local,omitempty"`
}

// handleSync 返回凭证同步开关与本机副本状态。
func (p *Panel) handleSync(w http.ResponseWriter, _ *http.Request) {
	response := syncResponse{Enabled: ReadSyncPreference()}
	if epoch, err := credentialsync.ReadEpoch(appdir.CredentialEpochPath()); err == nil {
		response.GatewayEpoch = epoch
	}
	if local, exists, err := credentialsync.Load(appdir.ClientCredentialPath()); err == nil && exists {
		state := credentialsync.State{
			LocalEpoch: local.Epoch,
			LastSyncAt: local.SyncedAt,
		}
		response.Local = &state
	}
	writeJSON(w, http.StatusOK, response)
}

// handleSetSync 改凭证同步开关。默认关，且开启前必须二次确认——
// 这个开关交出的是账号令牌。
func (p *Panel) handleSetSync(w http.ResponseWriter, r *http.Request) {
	if !p.requireGatewayMode() {
		writeError(w, http.StatusNotImplemented, "not_configured", "只有网关模式有凭证可下发")
		return
	}
	var request struct {
		Enable  bool `json:"enable"`
		Confirm bool `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	if request.Enable && !request.Confirm {
		writeError(w, http.StatusBadRequest, "confirm_required",
			"开启后，局域网内持有网关密钥的设备可以取走你的账号令牌。"+
				"确认请带上 \"confirm\": true")
		return
	}
	if err := SetSyncPreference(request.Enable); err != nil {
		p.fail(w, "修改凭证同步开关", err)
		return
	}
	// 开关在下次启动生效：凭证源是在启动时装进 relay 的，
	// 运行时增删意味着要处理正在进行的 TLS 会话。
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": request.Enable})
}

// handleSyncNow 客户端模式：立即同步一次。
func (p *Panel) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	if p.deps.Syncer == nil {
		writeError(w, http.StatusNotImplemented, "not_configured",
			"只有客户端模式会同步凭证")
		return
	}
	if err := p.deps.Syncer.Sync(r.Context()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// SwitchChecker 由 main 注入：检查本机能否切到网关模式。
//
// 面板自己不做这个判断——那要读凭证副本并解密，而解密密钥在 runtime 那一层。
// 注入一个函数比让面板反向依赖 runtime 干净。
type SwitchChecker func() (canSwitch bool, reason string, epoch int64, syncedAt time.Time)

// switchChecker 是注入的检查器；未注入时一律报告不能切。
var switchChecker SwitchChecker

// SetSwitchChecker 注入切换检查器。
func SetSwitchChecker(checker SwitchChecker) { switchChecker = checker }

// handleSwitchCheck 客户端模式：检查能否切到网关模式。
//
// 返回具体原因或空串。前端据此显示「差在哪一步」，
// 而不是一句「无法切换」让用户无从下手。
func (p *Panel) handleSwitchCheck(w http.ResponseWriter, _ *http.Request) {
	if switchChecker == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "本模式不支持切换")
		return
	}
	canSwitch, reason, epoch, syncedAt := switchChecker()
	writeJSON(w, http.StatusOK, map[string]any{
		"can_switch": canSwitch,
		"reason":     reason,
		"epoch":      epoch,
		"synced_at":  syncedAt,
	})
}
