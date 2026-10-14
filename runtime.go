package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/gateway"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
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
	return &runtime{
		backend:     backend,
		device:      device,
		psk:         pairing.PSK,
		fingerprint: pairing.Fingerprint,
	}, nil
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
	r.relay = relay.NewServer(relay.Deps{PSK: psk, Devices: registry, Version: version})
	return nil
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
