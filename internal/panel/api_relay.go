package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/netif"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/pairing"
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
	Enabled bool `json:"enabled"`
	// Address 是配置里的监听地址（形如 0.0.0.0:8788），只用于展示「绑在哪」。
	Address string `json:"address"`
	// Addresses 是**真正可连的**地址列表（每块局域网网卡一个）。
	// 客户端要照着这个填：0.0.0.0 是填不了的，那是「本机所有网卡」的意思，
	// 不是任何一个可以连过去的地址。
	Addresses   []string        `json:"addresses"`
	Fingerprint string          `json:"fingerprint"`
	PSKHint     string          `json:"psk_hint,omitempty"`
	Devices     []devices.Entry `json:"devices"`
}

// reachableAddrs 把监听地址翻译成客户端能真正连上的地址。
//
// 枚举失败或没有可用网卡时返回空列表——面板会退回显示配置里的地址，
// 那至少是个线索，比空白强。
func reachableAddrs(listenAddr string) []string {
	addresses, err := netif.ListenAddrs(listenAddr)
	if err != nil {
		return []string{}
	}
	return addresses
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
		Addresses:   reachableAddrs(p.deps.RelayAddr),
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

// SwitchExecutor 由 main 注入：真的执行切换（写本地登录态、标记副本活跃）。
type SwitchExecutor func() error

// switchExecutor 是注入的执行器；未注入时切换一律拒绝。
var switchExecutor SwitchExecutor

// SetSwitchExecutor 注入切换执行器。
func SetSwitchExecutor(executor SwitchExecutor) { switchExecutor = executor }

// handleSwitchExecute 客户端模式：用已同步的凭证切到网关模式。
//
// 做完之后要重启才生效（模式由启动参数决定）。所以这里返回一句
// 明确的「重启后生效」，而不是假装已经切好了。
func (p *Panel) handleSwitchExecute(w http.ResponseWriter, _ *http.Request) {
	if !p.requireClientMode() {
		writeError(w, http.StatusNotImplemented, "not_configured", "只有客户端模式需要切换")
		return
	}
	if switchExecutor == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "本模式不支持切换")
		return
	}
	if err := switchExecutor(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	p.deps.Logger.Printf("已用同步来的凭证切换到网关模式，重启后生效")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "已写好本机凭证。请停掉本进程，然后不带 -mode client 重新启动。",
	})
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

// pairingRequest 是配对表单提交的内容。
type pairingRequest struct {
	// Address 是网关地址，形如 192.168.1.5:8790（协议可省略）。
	Address string `json:"address"`
	// PSK 是网关控制台显示的那串密钥。
	PSK string `json:"psk"`
}

// pairingResponse 告诉前端下一步该做什么。
//
// 分两步是因为信任必须由人建立：先看清钥匙长什么样，
// 再决定要不要认。自动信任会让中间人也通过。
type pairingResponse struct {
	// NeedsConfirm 为真表示已连上网关并取到指纹，等待用户确认。
	NeedsConfirm bool   `json:"needs_confirm"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	Address      string `json:"address,omitempty"`
	// Paired 为真表示配对已完成。
	Paired bool `json:"paired"`
	// Error 是失败原因，直接显示给用户。
	Error string `json:"error,omitempty"`
}

// handlePairingProbe 第一步：连上网关取指纹。
//
// 这一步**刻意不校验、不建立信任**——它的语义是「让用户看看这把钥匙」。
// 所以即使地址填错、密钥填错，也要把能拿到的信息拿回来，
// 让用户在表单里看到具体哪一步不对。
func (p *Panel) handlePairingProbe(w http.ResponseWriter, r *http.Request) {
	if !p.requireClientMode() {
		writeError(w, http.StatusNotImplemented, "not_configured", "只有客户端模式需要配对")
		return
	}
	var request pairingRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
		return
	}
	address := pairing.NormalizeAddress(request.Address)
	if address == "" || strings.ContainsAny(request.Address, "/?") {
		writeJSON(w, http.StatusOK, pairingResponse{
			Error: "网关地址只能是「主机:端口」，形如 192.168.1.5:8790",
		})
		return
	}
	if strings.TrimSpace(request.PSK) == "" {
		writeJSON(w, http.StatusOK, pairingResponse{Error: "请填入网关控制台显示的密钥"})
		return
	}

	// 先落一份「未确认指纹」的配对，让后续连接能带着密钥过去验。
	// 指纹留空：此时还不该信任任何证书。
	provisional := pairing.Info{URL: address, PSK: strings.TrimSpace(request.PSK)}
	fingerprint, err := probeFingerprint(address)
	if err != nil {
		writeJSON(w, http.StatusOK, pairingResponse{Error: err.Error()})
		return
	}
	provisional.Fingerprint = ""
	if err := p.savePairing(provisional); err != nil {
		p.fail(w, "保存配对信息", err)
		return
	}
	writeJSON(w, http.StatusOK, pairingResponse{
		NeedsConfirm: true,
		Fingerprint:  fingerprint,
		Address:      address,
	})
}

// handlePairingConfirm 第二步：用户比对过指纹，确认信任。
func (p *Panel) handlePairingConfirm(w http.ResponseWriter, r *http.Request) {
	if !p.requireClientMode() {
		writeError(w, http.StatusNotImplemented, "not_configured", "只有客户端模式需要配对")
		return
	}
	var request struct {
		Address     string `json:"address"`
		PSK         string `json:"psk"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
		return
	}
	if strings.TrimSpace(request.Fingerprint) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "缺少要确认的指纹")
		return
	}
	info := pairing.Info{
		URL:         pairing.NormalizeAddress(request.Address),
		PSK:         strings.TrimSpace(request.PSK),
		Fingerprint: pairing.NormalizeFingerprint(request.Fingerprint),
	}
	if err := p.savePairing(info); err != nil {
		p.fail(w, "保存配对信息", err)
		return
	}
	p.deps.Logger.Printf("已与网关配对：%s", info.URL)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "paired": true})
}

// savePairing 写配对文件。权限由 pairing 包统一保证 0600。
func (p *Panel) savePairing(info pairing.Info) error {
	return pairing.Save(appdir.ClientPath(), info)
}

// requireClientMode 是客户端模式专属接口的前置。
func (p *Panel) requireClientMode() bool { return p.deps.Mode == config.ModeClient }

// probeFingerprint 由 main 注入：它要建 TLS 连接，
// 而建连接的细节在 relaytls 那一层，面板不该知道。
var probeFingerprint = func(address string) (string, error) {
	return "", fmt.Errorf("当前模式不支持探测指纹")
}

// SetProbeFingerprint 注入指纹探测实现。
func SetProbeFingerprint(probe func(string) (string, error)) { probeFingerprint = probe }
