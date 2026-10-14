package panel

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/devices"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/gateway"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/paircode"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/webui"
)

// nonceHeader 是页面用来证明「我确实读过本进程生成的 HTML」的请求头。
//
// 为什么用页内随机数而不是登录口令：面板是单用户本机工具，
// 真正的威胁是「你访问的某个网站偷偷向 127.0.0.1:8788 发请求」，
// 而不是「同机器上的另一个用户」。跨站请求有两个拦不住的坎：
// 读不到我们返回的 HTML（同源策略），也没法给表单加自定义请求头。
// 所以把每次启动生成的随机数写进页面，就能挡住这一类攻击，
// 同时不必让用户去记一个口令——「不想碰命令行」的人没有地方读它。
//
// 残余风险见 README：能读你本机文件的程序依然可以绕过，这需要更强的隔离，
// 不在本工具的职责范围内。
const nonceHeader = "X-Plwg-Nonce"

// noncePlaceholder 是前端 HTML 里留给随机数的位置。
const noncePlaceholder = "__PLWG_NONCE__"

// Deps 是面板的依赖。
type Deps struct {
	Version   string
	StartedAt time.Time

	// Mode 决定面板显示哪些区块：客户端模式没有凭证，
	// 余额、账号状态这些部分不能照常渲染成「读取失败」。
	Mode string
	// AuthFile / Credential / Billing 只在网关模式下非空。
	AuthFile string

	// Syncer 是客户端模式的凭证同步器；网关模式为 nil。
	Syncer *credentialsync.Syncer
	// PairingErr 非空表示客户端模式尚未配对。面板据此显示配对表单。
	PairingErr error
	// PairingAddress 是本次要连的网关地址，预填进表单。
	PairingAddress string

	// RelayAddr / RelayEnabled / TLSFingerprint / PSK / PSKHint / Devices
	// 描述转发服务，只有网关模式才有。
	//
	// PSK 是完整密钥，**只经由专用的按需接口下发**（POST /api/relay/psk/reveal），
	// 不随任何列表接口返回。理由：列表接口会被面板的轮询反复拉取，
	// 密钥跟着在浏览器内存与网络面板里反复出现，没有必要。
	// PSKHint 是可以随列表返回的提示（前几位 + 总长度）。
	RelayAddr      string
	RelayEnabled   bool
	TLSFingerprint string
	PSK            string
	PSKHint        string
	// PairingCodes 生成一次性配对码。只有网关模式才有。
	PairingCodes *paircode.Code
	Devices      *devices.Registry
	Device       *identity.Device

	ListenAddr     string
	ConsoleAddr    string
	LogPath        string
	JournalPath    string
	PromotionsPath string

	Credential *credential.Manager
	Upstream   gateway.Backend
	Billing    *billing.Client
	Promotions *catalog.Promotions
	Journal    *journal.Journal
	Logger     *logging.Logger
	Control    *Control
}

// Panel 是本机 Web 控制台。
type Panel struct {
	deps  Deps
	nonce string
	index []byte // 已注入随机数的首页

	// promotions 会被面板自己改（用户在设置页编辑），所以要用锁护住。
	promotionsMu     sync.RWMutex
	activePromotions *catalog.Promotions
}

// currentPromotions 取当前生效的促销配置。
func (p *Panel) promotions() *catalog.Promotions {
	p.promotionsMu.RLock()
	defer p.promotionsMu.RUnlock()
	return p.activePromotions
}

// setPromotions 在用户改完促销配置后立刻生效，不必等重启。
func (p *Panel) setPromotions(next *catalog.Promotions) {
	p.promotionsMu.Lock()
	defer p.promotionsMu.Unlock()
	p.activePromotions = next
}

// New 构造面板。首页在启动时读入内存并注入随机数——
// 页面不大，省掉每次请求读文件的开销与错误处理。
func New(deps Deps) (*Panel, error) {
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}
	index, err := buildIndex(nonce)
	if err != nil {
		return nil, err
	}
	return &Panel{deps: deps, nonce: nonce, index: index, activePromotions: deps.Promotions}, nil
}

// Handler 返回面板的 HTTP 处理器，包含 /api/* 与静态资源。
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()

	// ping 不需要随机数：它只用来让前端确认「后端在不在」，
	// 不含任何账户信息。
	mux.HandleFunc("GET /api/ping", p.handlePing)

	mux.HandleFunc("GET /api/status", p.guarded(p.handleStatus))
	mux.HandleFunc("GET /api/billing", p.guarded(p.handleBilling))
	mux.HandleFunc("GET /api/usage", p.guarded(p.handleUsage))
	mux.HandleFunc("GET /api/models", p.guarded(p.handleModels))
	mux.HandleFunc("GET /api/models/pi", p.guarded(p.handlePiModels))
	mux.HandleFunc("GET /api/events", p.guarded(p.handleEvents))
	mux.HandleFunc("GET /api/promotions", p.guarded(p.handleGetPromotions))
	mux.HandleFunc("PUT /api/promotions", p.guarded(p.handlePutPromotions))

	// 转发服务与设备管理。只在网关模式下有意义，客户端模式一律 501。
	mux.HandleFunc("GET /api/relay", p.guarded(p.handleRelay))
	mux.HandleFunc("POST /api/relay/psk", p.guarded(p.handleResetPSK))
	mux.HandleFunc("POST /api/relay/psk/reveal", p.guarded(p.handleRevealPSK))
	mux.HandleFunc("POST /api/relay/paircode", p.guarded(p.handleIssuePairCode))
	mux.HandleFunc("POST /api/relay/enable", p.guarded(p.handleEnableRelay))
	mux.HandleFunc("POST /api/devices/revoke", p.guarded(p.handleRevokeDevice))
	mux.HandleFunc("POST /api/devices/unrevoke", p.guarded(p.handleUnrevokeDevice))

	// 凭证同步。同步开关只有网关模式有，立即同步与切换检查只有客户端模式有。
	mux.HandleFunc("GET /api/sync", p.guarded(p.handleSync))
	mux.HandleFunc("POST /api/sync", p.guarded(p.handleSetSync))
	mux.HandleFunc("POST /api/sync/now", p.guarded(p.handleSyncNow))
	mux.HandleFunc("GET /api/sync/switch-check", p.guarded(p.handleSwitchCheck))
	mux.HandleFunc("POST /api/sync/switch", p.guarded(p.handleSwitchExecute))

	// 配对：客户端模式的入口。未配对时只有这些接口可用。
	mux.HandleFunc("POST /api/pairing/probe", p.guarded(p.handlePairingProbe))
	mux.HandleFunc("POST /api/pairing/confirm", p.guarded(p.handlePairingConfirm))

	mux.HandleFunc("POST /api/control/stop", p.guarded(p.handleStop))
	mux.HandleFunc("POST /api/control/restart", p.guarded(p.handleRestart))

	mux.HandleFunc("/", p.handleStatic)
	return mux
}

// guarded 包一层校验：请求头里的随机数必须与本文档一致。
func (p *Panel) guarded(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(nonceHeader) != p.nonce {
			writeError(w, http.StatusForbidden, "unauthorized",
				"缺少或错误的控制台凭据。请从控制台页面访问，不要直接打开接口地址。")
			return
		}
		// 写操作额外校验来源：跨站请求可能带上正确的自定义头吗？不能，
		// 但这一层能挡住「用户被诱导手动粘贴脚本」之类的边角情况。
		if r.Method != http.MethodGet && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "bad_origin", "请求来源不被接受")
			return
		}
		next(w, r)
	}
}

// sameOrigin 判断请求是否来自本机同源的页面。
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// 非浏览器客户端不带 Origin，放行。
		return true
	}
	return strings.HasSuffix(origin, "//"+r.Host)
}

// handlePing 供前端探测后端是否就绪，不含任何账户信息。
func (p *Panel) handlePing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": p.deps.Version})
}

// handleStatic 提供前端静态资源；找不到的路径一律回落到首页（前端自己做路由）。
func (p *Panel) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持的请求方法")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		p.serveIndex(w)
		return
	}

	raw, err := webui.Dist.ReadFile("dist/" + path)
	if err != nil {
		p.serveIndex(w)
		return
	}
	// 静态资源内容固定，可以放心让浏览器长期缓存；
	// 首页不缓存，否则注入的随机数会过期。
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(raw))
}

// serveIndex 输出已注入随机数的首页。
func (p *Panel) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(p.index)
}

// buildIndex 读入首页并注入本次进程的随机数。
func buildIndex(nonce string) ([]byte, error) {
	raw, err := webui.Dist.ReadFile("dist/index.html")
	if err != nil {
		return nil, fmt.Errorf("读取前端首页失败：%w", err)
	}
	injected := insertNonce(raw, nonce)
	if injected == nil {
		return nil, fmt.Errorf("前端首页里既没有随机数占位符，也没有 </head>，无法注入凭据")
	}
	return injected, nil
}

// insertNonce 把随机数写进首页。
//
// 优先替换占位符；占位符没了（用户换了自制前端）就自己插一个 meta 进去——
// 宁可注入得丑一点，也不要让面板整个白屏。两者都做不到才返回 nil。
func insertNonce(raw []byte, nonce string) []byte {
	if bytes.Contains(raw, []byte(noncePlaceholder)) {
		return bytes.ReplaceAll(raw, []byte(noncePlaceholder), []byte(nonce))
	}
	index := bytes.Index(raw, []byte("</head>"))
	if index < 0 {
		return nil
	}
	meta := fmt.Sprintf(`<meta name="plwg-nonce" content="%s">`, nonce)
	injected := make([]byte, 0, len(raw)+len(meta))
	injected = append(injected, raw[:index]...)
	injected = append(injected, meta...)
	injected = append(injected, raw[index:]...)
	return injected
}

// randomNonce 生成每次启动都不同的一次性随机数。
func randomNonce() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成控制台凭据失败：%w", err)
	}
	return hex.EncodeToString(buf), nil
}

// writeJSON 输出一个 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

// writeError 输出带机器可读 code 的错误。
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

// fail 把内部错误统一成 502 输出，同时写一条日志——
// 面板上的「打不开」必须留得下线索。
func (p *Panel) fail(w http.ResponseWriter, action string, err error) {
	p.deps.Logger.Printf("✗ 面板 %s 失败：%v", action, err)
	writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
}
