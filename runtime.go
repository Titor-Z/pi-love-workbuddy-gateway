package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/gateway"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/panel"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relay"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relaytls"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// pairedGateway 是客户端存下的配对信息。
//
// 这个文件间接持有另一台机器上账号的访问权限，所以它的权限要求
// 与主密钥文件相同（0600）——只给 psk.txt 设 0600 而把它设成 0644，
// 等于权限白设。
type pairedGateway struct {
	URL         string `json:"url"`
	PSK         string `json:"psk"`
	Fingerprint string `json:"fingerprint"`
}

// runtime 是两种模式装配完后共用的东西。
//
// 之所以要这么一层：网关模式与客户端模式的差别**只在 backend 是谁与有没有凭证**，
// 其余（网关 Handler、面板、HTTP 服务）完全一致。把差别集中在这里，
// 主流程里就不会散落 if mode == ... 这种分支了。
type runtime struct {
	backend gateway.Backend

	// credential / billing / authFile 只在网关模式下非空。
	// 客户端模式**刻意不加载凭证**：P0 的客户端手上不该有令牌。
	credential *credential.Manager
	billing    *billing.Client
	authFile   string
	keyID      string // 解密密钥指纹，仅网关模式有

	device      *identity.Device
	fingerprint string
	psk         string

	// devices 与 relay 只在网关模式且开启了转发服务时非空。
	devices *devices.Registry
	relay   *relay.Server

	// syncAllowed 表示网关是否允许下发凭证。默认关——
	// 这个端点交出的是账号令牌，比转发请求敏感一个量级。
	syncAllowed bool
	syncEpoch   *credentialCounter

	// syncer 是客户端模式的凭证同步器；网关模式为 nil。
	syncer *credentialsync.Syncer

	certificate tls.Certificate
}

// pskHint 给面板用：绝不返回密钥本身，只说有没有、给前几位方便核对。
func (r *runtime) pskHint() string {
	if r.psk == "" {
		return ""
	}
	return fmt.Sprintf("%s…（共 %d 位）", r.psk[:min(4, len(r.psk))], len(r.psk))
}

// buildRuntime 按模式装配运行时。两条路的最大共同点是：
// 都产出一个 gateway.Backend，之后的一切对它的来源完全无感。
func buildRuntime(cfg *config.Config, logger *logging.Logger) (*runtime, error) {
	device, err := loadDevice(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Mode == config.ModeClient {
		return buildClientRuntime(cfg, device)
	}
	return buildGatewayRuntime(cfg, logger, device)
}

// loadDevice 载入本设备身份。
//
// 设备 ID 与运行模式无关：同一台机器切网关 / 客户端是同一个身份，
// 否则网关那侧的设备表会因为一次模式切换就分裂出两个条目。
func loadDevice(cfg *config.Config) (*identity.Device, error) {
	device, err := identity.LoadDevice(appdir.DevicePath())
	if err != nil {
		return nil, err
	}
	if cfg.DeviceName != "" {
		device.Name = cfg.DeviceName
	}
	return device, nil
}

// buildGatewayRuntime 装配网关模式：持有凭证、直连后端。
func buildGatewayRuntime(cfg *config.Config, logger *logging.Logger,
	device *identity.Device) (*runtime, error) {

	cred, cipher, err := buildCredential(cfg, logger.Printf)
	if err != nil {
		return nil, err
	}
	parts := &runtime{
		backend:    upstream.NewClient(cred, ""),
		credential: cred,
		billing:    billing.NewClient(cred, ""),
		authFile:   cred.Path(),
		keyID:      cipher.KeyID(),
		device:     device,
	}
	if cfg.RelayEnabled {
		if err := parts.enableRelay(cfg, logger); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// buildClientRuntime 装配客户端模式：把请求转发给网关。
func buildClientRuntime(cfg *config.Config, device *identity.Device) (*runtime, error) {
	pairing, err := loadPairing(cfg)
	if err != nil {
		return nil, err
	}
	backend, err := relay.NewClient(relay.Config{
		BaseURL:     pairing.URL,
		PSK:         pairing.PSK,
		Fingerprint: pairing.Fingerprint,
		Device:      device,
	})
	if err != nil {
		return nil, err
	}
	// 凭证同步器：只拉、只落本机加密副本，不刷新不回写不推送。
	syncer := &credentialsync.Syncer{
		Source:   backend,
		Store:    appdir.ClientCredentialPath(),
		Interval: syncInterval,
		Seal:     sealWithClientCipher,
	}
	return &runtime{
		backend:     backend,
		device:      device,
		psk:         pairing.PSK,
		fingerprint: pairing.Fingerprint,
		syncer:      syncer,
	}, nil
}

// syncInterval 是正常的凭证同步周期。
//
// 30 分钟：令牌有效期是小时级，拉太频没有意义，拉太疏则出门前可能拿到
// 一份快过期的。失败时由 Syncer 自己退避，最长也回到这个周期。
const syncInterval = 30 * time.Minute

// clientCipher 返回客户端本机的 at-rest 密钥派生器。
//
// 与网关那套完全无关：客户端首次同步时生成自己的密钥，
// 只用来加密本机的凭证副本。这比「把网关的密钥搬过来」安全——
// 后者意味着密钥多一份副本，而这份副本和凭证在同一台机器上，
// 等于把两把钥匙放在同一个抽屉里。
func clientCipher() (*atrest.Cipher, error) {
	path := appdir.ClientAtRestPath()
	existing, readErr := readClientSecret(path)
	if readErr != nil {
		return nil, readErr
	}
	if existing != "" {
		return atrest.NewCipher(existing)
	}
	secret, err := identity.NewPSK() // 同一套随机数生成，用途不同而已
	if err != nil {
		return nil, err
	}
	cipher, err := atrest.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return nil, err
	}
	return cipher, nil
}

// readClientSecret 读客户端自己的密钥；不存在返回空串。
func readClientSecret(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// sealWithClientCipher 用客户端自己的密钥加密一份凭证。
func sealWithClientCipher(credential map[string]any) (map[string]any, error) {
	cipher, err := clientCipher()
	if err != nil {
		return nil, err
	}
	auth, _ := credential["auth"].(map[string]any)
	if auth == nil {
		return nil, fmt.Errorf("凭证缺少 auth 段")
	}
	sealed := make(map[string]any, len(credential))
	for key, value := range credential {
		sealed[key] = value
	}
	sealedAuth := make(map[string]any, len(auth))
	for key, value := range auth {
		sealedAuth[key] = value
	}
	for _, key := range []string{"accessToken", "refreshToken"} {
		plain, _ := auth[key].(string)
		if plain == "" {
			continue
		}
		envelope, err := cipher.Encrypt(plain)
		if err != nil {
			return nil, err
		}
		sealedAuth[key] = envelope
	}
	sealed["auth"] = sealedAuth
	return sealed, nil
}

// openClientCredential 用客户端自己的密钥解开本机凭证副本。
//
// 返回值里的文档是**明文**：只在进程内使用，调用方不得落盘。
func openClientCredential() (map[string]any, error) {
	cipher, err := clientCipher()
	if err != nil {
		return nil, err
	}
	file, exists, err := credentialsync.Load(appdir.ClientCredentialPath())
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("本机没有凭证副本")
	}
	auth, _ := file.Credential["auth"].(map[string]any)
	if auth == nil {
		return nil, fmt.Errorf("凭证副本缺少 auth 段")
	}
	plain := make(map[string]any, len(file.Credential))
	for key, value := range file.Credential {
		plain[key] = value
	}
	plainAuth := make(map[string]any, len(auth))
	for key, value := range auth {
		plainAuth[key] = value
	}
	for _, key := range []string{"accessToken", "refreshToken"} {
		value := auth[key]
		if !atrest.IsEncrypted(value) {
			// 已经是明文（理论上不该发生，但别在这里崩）。
			plainAuth[key] = value
			continue
		}
		text, err := cipher.Decrypt(value)
		if err != nil {
			return nil, fmt.Errorf("解密 %s 失败：%w（本机密钥可能已丢失）", key, err)
		}
		plainAuth[key] = text
	}
	plain["auth"] = plainAuth
	return plain, nil
}

// loadPairing 取客户端的配对信息：命令行优先，其次读已保存的那份。
//
// 分两层是为了两种用法都能成立：第一次配手写 `-gateway -psk`，
// 之后再启动就不必再敲一遍那些长串。
func loadPairing(cfg *config.Config) (pairedGateway, error) {
	pairing, err := readPairing(appdir.ClientPath())
	if err != nil {
		return pairedGateway{}, err
	}
	if cfg.GatewayURL != "" {
		pairing.URL = cfg.GatewayURL
	}
	if cfg.PSK != "" {
		pairing.PSK = cfg.PSK
	}
	// -trust 也可以顺手带上，一次认完直接启动，省一条命令。
	if cfg.Trust != "" {
		pairing.Fingerprint = cfg.Trust
		// 顺手落盘：-trust 常常与 -sync-now 一起用来完成「认一次 + 同步」。
		if _, err := appdir.EnsureDir(); err == nil {
			_ = savePairing(appdir.ClientPath(), pairing)
		}
	}
	switch {
	case pairing.URL == "":
		return pairedGateway{}, fmt.Errorf("尚未指定网关地址：用 -gateway https://<网关IP>:8790")
	case pairing.PSK == "":
		return pairedGateway{}, fmt.Errorf("尚未填入网关密钥：用 -psk <控制台显示的那串>")
	case pairing.Fingerprint == "":
		return pairedGateway{}, fmt.Errorf("尚未确认网关证书指纹：先在本机跑一次 " +
			"`-gateway <地址> -probe` 拿到指纹，再用 -trust <指纹> 确认")
	}
	return pairing, nil
}

// readPairing 读配对文件；不存在就当作还没配对过。
func readPairing(path string) (pairedGateway, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return pairedGateway{}, nil
	}
	if err != nil {
		return pairedGateway{}, fmt.Errorf("读取配对信息失败：%w", err)
	}
	var pairing pairedGateway
	if err := json.Unmarshal(raw, &pairing); err != nil {
		return pairedGateway{}, fmt.Errorf("配对文件已损坏：%w（路径 %s）", err, path)
	}
	return pairing, nil
}

// savePairing 写配对信息，权限 0600。
func savePairing(path string, pairing pairedGateway) error {
	raw, err := json.MarshalIndent(pairing, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配对信息失败：%w", err)
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// enableRelay 准备好转发服务所需的密钥与证书。
func (r *runtime) enableRelay(cfg *config.Config, logger *logging.Logger) error {
	psk := cfg.PSK
	if psk == "" {
		loaded, err := identity.LoadOrCreatePSK(appdir.PSKPath())
		if err != nil {
			return err
		}
		psk = loaded
	}
	certificate, fingerprint, err := relaytls.LoadOrCreate(appdir.RelayCertPath(), appdir.RelayKeyPath())
	if err != nil {
		return err
	}
	registry, err := devices.Open(appdir.DevicesPath())
	if err != nil {
		return err
	}

	r.psk = psk
	r.fingerprint = fingerprint
	r.certificate = certificate
	r.devices = registry
	r.syncEpoch = newCredentialCounter(appdir.CredentialEpochPath())
	// 凭证同步是独立开关，与「开放转发服务」并列，默认关。
	r.syncAllowed = panel.ReadSyncPreference()
	deps := relay.Deps{PSK: psk, Devices: registry, Version: version}
	if r.syncAllowed {
		deps.CredentialSource = &credentialExporter{
			cred:  r.credential,
			epoch: r.syncEpoch,
		}
	}
	r.relay = relay.NewServer(deps)
	return nil
}

// syncPreference 是「是否允许下发凭证」这一项偏好。
//
// 单独一项而不是复用转发开关：转发是把请求送进来，
// 下发凭证是把账号令牌交出去，后者风险高一个量级，
// 必须让人分别想清楚。
type syncPreference struct {
	Enabled bool `json:"credential_sync"`
}

// setSyncPreference 改凭证同步开关。写入失败要让用户知道，而不是默默不生效。
func setSyncPreference(enabled bool) error {
	preference := syncPreference{}
	if raw, err := os.ReadFile(appdir.RelayPreferencePath()); err == nil {
		_ = json.Unmarshal(raw, &preference)
	}
	preference.Enabled = enabled
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

// journalPathForMode 按模式给出事件文件路径。
//
// 两个模式用不同的文件：同一台机器上它们可能被同时跑着（先切过去试一试），
// 而跨进程就没有 Journal 那把进程内互斥锁了，写同一个文件会交错成坏行。
// 换文件名比引文件锁简单，也让「切模式」这件事天然可解释：
// 旧的那份仍然在，没有被迁移或清空。
func journalPathForMode(cfg *config.Config) string {
	if cfg.Mode == config.ModeClient {
		return appdir.ClientJournalPath()
	}
	return appdir.JournalPath()
}

// pairingSummary 给日志用：地址可说，密钥与指纹只能说数量。
func pairingSummary(pairing pairedGateway) string {
	return fmt.Sprintf("%s（密钥 %d 位，指纹已确认 %d 位）",
		strings.TrimSpace(pairing.URL), len(pairing.PSK), len(pairing.Fingerprint))
}

// ProbeGateway 取网关证书指纹。**不建立信任**，只把结果给用户看。
//
// 这一步必须与「确认」分开：信任是用户比对过、按下确认之后才成立的，
// 连一次就信了的话，中间人换张证书照样通过。
func ProbeGateway(ctx context.Context, gatewayURL string) (string, error) {
	host, err := relayHostOnly(gatewayURL)
	if err != nil {
		return "", err
	}
	return relaytls.ProbeFingerprint(ctx, host)
}

// TrustGateway 确认一枚指纹并写进配对文件。
//
// 之后的所有连接都严格比对这个值；指纹变了会报错要求重新确认，
// 绝不静默接受（网关重装过也会走到那一步，那是需要人看一眼的时刻）。
func TrustGateway(cfg *config.Config, fingerprint string) (pairedGateway, error) {
	pairing, err := loadPairing(cfg)
	if err != nil {
		return pairedGateway{}, err
	}
	pairing.Fingerprint = fingerprint
	if _, err := appdir.EnsureDir(); err != nil {
		return pairedGateway{}, err
	}
	if err := savePairing(appdir.ClientPath(), pairing); err != nil {
		return pairedGateway{}, err
	}
	return pairing, nil
}

// relayHostOnly 从 URL 里取出「主机:端口」。
func relayHostOnly(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		// 用户在命令行上常常只写 192.168.1.5:8790，补上协议再认一次。
		parsed, err = url.Parse("https://" + raw)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("网关地址 %q 里没有主机部分", raw)
		}
	}
	return parsed.Host, nil
}

// runPairing 处理「先看清钥匙、再确认信任」这两步。
//
// 为什么要分两步、而不是连一次就自动信任：中间人此刻也可能给你一把钥匙。
// 用户必须有机会把那串指纹跟网关控制台上显示的比对，这一步省不掉。
func runPairing(cfg *config.Config) error {
	if cfg.Trust != "" {
		pairing, err := TrustGateway(cfg, cfg.Trust)
		if err != nil {
			return err
		}
		fmt.Printf("已确认网关证书：%s\n", pairing.Fingerprint)
		fmt.Printf("配对信息已写入 %s\n", appdir.ClientPath())
		fmt.Printf("现在可以启动客户端了\n")
		return nil
	}
	fingerprint, err := ProbeGateway(context.Background(), cfg.GatewayURL)
	if err != nil {
		return err
	}
	fmt.Printf("网关证书指纹：%s\n\n", fingerprint)
	fmt.Printf("请与网关控制台显示的指纹比对。确认一致后执行：\n\n")
	fmt.Printf("  %s -mode client -gateway %s -trust %s\n\n", os.Args[0], cfg.GatewayURL, fingerprint)
	fmt.Printf("不一致就不要确认——那说明你连上的可能不是你的网关。\n")
	return nil
}

// credentialExporter 把本机的凭证管理器暴露成 relay 能用的凭证源。
//
// 关键点：这里交出去的是**解密后的明文**。因为 atRestSecretKey 绑定在本机，
// 客户端机器没有它，加密信封搬不过来——只能交明文，由客户端落地时
// 用自己的密钥重新封装。明文只存在于 TLS 通道内，不落任何磁盘。
type credentialExporter struct {
	cred  *credential.Manager
	epoch *credentialCounter
}

// credentialCounter 包装网关侧的 epoch 文件。
type credentialCounter struct{ path string }

// newCredentialCounter 构造计数器。
func newCredentialCounter(path string) *credentialCounter {
	return &credentialCounter{path: path}
}

// current 读当前 epoch。
func (c *credentialCounter) current() (int64, error) {
	return credentialsync.ReadEpoch(c.path)
}

// bump 把 epoch 加一。每次成功下发都加——客户端据此判断新旧。
func (c *credentialCounter) bump() (int64, error) {
	return credentialsync.NextEpoch(c.path)
}

// CredentialEnvelope 取一份要下发的凭证。
func (e *credentialExporter) CredentialEnvelope() (credentialsync.Envelope, error) {
	doc, err := e.cred.Export()
	if err != nil {
		return credentialsync.Envelope{}, err
	}
	epoch, err := e.epoch.bump()
	if err != nil {
		return credentialsync.Envelope{}, err
	}
	return credentialsync.Envelope{Epoch: epoch, Credential: doc}, nil
}

// runCredentialSync 是客户端模式的凭证同步循环。
//
// 它只做拉取与落盘：不刷新、不回写、不推送。那三条是地基——
// 一旦客户端能刷新，多设备共享就退化成互相踢。
func runCredentialSync(ctx context.Context, syncer *credentialsync.Syncer,
	logger *logging.Logger, events *journal.Journal) {

	// 启动就拉一次：用户可能刚装好，或者隔了一夜没连。
	if err := syncer.Sync(ctx); err != nil {
		logger.Printf("凭证同步失败：%v", err)
	} else {
		logger.Printf("凭证已同步到本机")
	}

	for {
		wait := syncer.Backoff()
		if wait <= 0 {
			wait = syncInterval
		}
		logger.Printf("下次凭证同步：%s 后", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err := syncer.Sync(ctx); err != nil {
			logger.Printf("凭证同步失败：%v", err)
			continue
		}
		snapshot := syncer.Snapshot()
		logger.Printf("凭证已同步，epoch %d", snapshot.LastEpoch)
		if snapshot.Conflict != nil {
			logger.Printf("⚠️ 本机凭证比网关新（本机 %d，网关 %d）："+
				"这台机器此前切到网关模式自己刷新过，请到面板选择用哪一份",
				snapshot.Conflict.LocalEpoch, snapshot.Conflict.RemoteEpoch)
			events.Append(journal.Record{
				Kind: journal.KindError,
				Message: fmt.Sprintf("凭证冲突：本机 epoch %d，网关 %d",
					snapshot.Conflict.LocalEpoch, snapshot.Conflict.RemoteEpoch),
			})
		}
	}
}

// switchProblem 是切换被拒绝的原因。四种各自有不同的处置建议，
// 所以不能让它们共用一句「无法切换」。
type switchProblem int

const (
	// problemNone 表示可以切换。
	problemNone switchProblem = iota
	// problemNoCopy 表示本机还没有凭证副本。
	problemNoCopy
	// problemUndecryptable 表示副本在但解不开（本机密钥可能丢了）。
	problemUndecryptable
	// problemNoToken 表示解开了但没有访问令牌。
	problemNoToken
	// problemExpired 表示令牌已过期。
	problemExpired
)

// String 给人看的说明。
func (p switchProblem) String() string {
	switch p {
	case problemNoCopy:
		return "本机没有凭证副本，请先连回网关同步一次"
	case problemUndecryptable:
		return "凭证副本解不开——本机密钥可能已丢失，请重新同步"
	case problemNoToken:
		return "凭证副本里没有访问令牌，请重新同步"
	case problemExpired:
		return "凭证已过期，请先连回网关同步新的"
	}
	return ""
}

// CheckSwitchToGateway 检查本机是否具备切到网关模式的条件。
//
// 四项缺一不可，任缺一项就拒绝切换并给出对应提示。
// **不降级为本地模式**：客户端机器没装 WorkBuddy，降级等于不能用，
// 不如明说差在哪。
func CheckSwitchToGateway() (switchProblem, credentialsync.LocalFile, map[string]any, error) {
	file, exists, err := credentialsync.Load(appdir.ClientCredentialPath())
	if err != nil {
		return problemUndecryptable, credentialsync.LocalFile{}, nil, err
	}
	if !exists {
		return problemNoCopy, credentialsync.LocalFile{}, nil, nil
	}
	plain, err := openClientCredential()
	if err != nil {
		return problemUndecryptable, file, nil, err
	}
	auth, _ := plain["auth"].(map[string]any)
	if auth == nil || text(auth, "accessToken") == "" {
		return problemNoToken, file, nil, nil
	}
	if isExpiredDoc(plain, time.Now()) {
		return problemExpired, file, nil, nil
	}
	return problemNone, file, plain, nil
}

// isExpiredDoc 判断一份明文登录态文档的访问令牌是否已过期。
func isExpiredDoc(doc map[string]any, now time.Time) bool {
	auth, _ := doc["auth"].(map[string]any)
	if auth == nil {
		return true
	}
	expiresAt, ok := auth["expiresAt"].(float64)
	if !ok || expiresAt <= 0 {
		// 没有有效期信息时不当作过期——缺字段不等于失效，
		// 真正失效时后端会以 401 回答。
		return false
	}
	return now.UnixMilli() >= int64(expiresAt)
}

// text 取一个字符串字段；缺失或类型不对都算空。
func text(obj map[string]any, key string) string {
	value, _ := obj[key].(string)
	return value
}

// runSwitchToGateway 把本机切成网关模式。
//
// 做三件事：先同步一次拿最新凭证（避免拿一份快过期的出门），
// 解成本地登录态文件，标记副本为活跃。之后用户以 gateway 模式启动即可。
//
// 刻意不在这一步直接重启成网关模式：模式切换要改启动参数，
// 而「帮用户改启动参数」比告诉他怎么改更容易出错。
func runSwitchToGateway(cfg *config.Config) error {
	// 先同步一次。用户可能在家但很久没连过，
	// 手里的副本可能是几小时前的——那时令牌可能已经过期。
	if cfg.SyncOnly {
		return runSyncOnly(cfg)
	}
	problem, file, plain, err := CheckSwitchToGateway()
	if err != nil {
		return err
	}
	if problem != problemNone {
		return fmt.Errorf("%s", problem)
	}
	if err := writeLocalAuthFile(plain); err != nil {
		return err
	}
	if err := credentialsync.SetActive(appdir.ClientCredentialPath(), true); err != nil {
		return err
	}
	fmt.Printf("已切到网关模式。\n")
	fmt.Printf("登录态已写入：%s\n", localAuthPath())
	fmt.Printf("凭证版本：epoch %d（同步于 %s）\n",
		file.Epoch, file.SyncedAt.Format(time.RFC3339))
	fmt.Printf("\n现在可以这样启动：\n\n")
	fmt.Printf("  %s -mode gateway\n\n", os.Args[0])
	fmt.Printf("注意：从这一刻起本机会自己刷新令牌，请先停掉客户端模式的那个进程。\n")
	fmt.Printf("回连局域网时若面板提示凭证冲突，说明网关那份更旧——由你选择用哪一份。\n")
	return nil
}

// runSyncOnly 只同步一次，用于出门前手动刷新凭证。
func runSyncOnly(cfg *config.Config) error {
	device, err := loadDevice(cfg)
	if err != nil {
		return err
	}
	parts, err := buildClientRuntime(cfg, device)
	if err != nil {
		return err
	}
	if err := parts.syncer.Sync(context.Background()); err != nil {
		return err
	}
	snapshot := parts.syncer.Snapshot()
	fmt.Printf("凭证已同步，epoch %d\n", snapshot.LastEpoch)
	if snapshot.Conflict != nil {
		fmt.Printf("⚠️ 本机凭证比网关新（本机 %d，网关 %d）。\\n"+
			"这台机器此前切到网关模式自己刷新过。\\n"+
			"用哪一份由你决定：保留本机就不用管；想用网关的，"+
			"把本机 epoch 调低后再同步一次。\\n",
			snapshot.Conflict.LocalEpoch, snapshot.Conflict.RemoteEpoch)
	}
	return nil
}

// localAuthPath 是切到网关模式后写的登录态文件路径。
func localAuthPath() string {
	return appdir.LocalAuthPath()
}

// writeLocalAuthFile 把明文登录态写成本机文件。
//
// 权限 0600：这份文件就是账号凭据。之后 credential.Manager 会按
// 「以文件为准」的既有逻辑读它，包含与桌面端共用时的自动跟随。
func writeLocalAuthFile(doc map[string]any) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化登录态失败：%w", err)
	}
	path := localAuthPath()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		return fmt.Errorf("写入登录态失败：%w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("替换登录态失败：%w", err)
	}
	return nil
}
