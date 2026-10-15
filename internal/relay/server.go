// Package relay 实现网关与客户端之间的转发通道。
//
// 一边是挂在网关第三个端口上的服务端：校验 PSK、登记设备、把请求交给
// 网关自己的 Handler；另一边是客户端侧使用的 Backend 实现，
// 让 client 模式下的网关 Server 完全不知道自己其实没有凭证。
//
// 关于鉴权（重要，别误会）：
//
//   - 真正的安全边界是 **PSK + TLS 指纹钉扎**。
//   - device_id 由客户端**自报**，可以伪造。它只是审计用的标签，
//     吊销某个 ID 也换不来安全——换个 ID 就能重连。
//     要踢人只能轮换 PSK。
package relay

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/paircode"
)

const (
	// HeaderPSK 携带预共享密钥。
	HeaderPSK = "X-Plwg-Psk"
	// HeaderDevice 携带设备 ID。自报，可伪造，只用于审计与归类。
	HeaderDevice = "X-Plwg-Device"
	// HeaderDeviceName 携带设备名，仅用于在面板上认得出是哪台机器。
	HeaderDeviceName = "X-Plwg-Device-Name"
)

// pingPath 让客户端不必发一次真实请求就能确认网关在不在、指纹对不对。
const pingPath = "/relay/v1/ping"

// pairPath 是配对端点。它**不需要 PSK**——短码本身就是这次的凭据。
// 校验通过后把长密钥交出去（走 TLS），之后客户端一直用长密钥。
const pairPath = "/relay/v1/pair"

// overridesPath 是模型覆盖表同步端点。
//
// 与凭证端点的方向不同：凭证**只下不下上**，覆盖表**接受推送**。
// 理由是覆盖表不含任何秘密（就是别名、日期、思考档位），而用户在外面
// 那台机器上的改动必须能带回来。详见 docs/model-overrides-design.md。
const overridesPath = "/relay/v1/model-overrides"

// credentialPath 是凭证同步端点。
//
// 它返回的是账号令牌，比转发请求敏感一个量级，因此有三重保护：
// 独立开关（默认关，见 CredentialSource 为 nil 时的行为）、PSK、TLS 指纹。
const credentialPath = "/relay/v1/credential"

// Deps 是 relay 服务端的依赖。
type Deps struct {
	// Backend 接收通过鉴权的请求。生产里它就是网关自己的 Handler——
	// 两者同进程，直接挂过去，不做 HTTP 反代：少一跳，也少一次可能破坏
	// SSE 的缓冲。
	Backend http.Handler
	// CredentialSource 提供凭证同步。为 nil 时该端点一律 403——
	// 这个端点返回的是账号令牌，比转发请求敏感一个量级，
	// 必须由用户在控制台显式开启。
	CredentialSource CredentialSource
	// Devices 登记到访设备；为 nil 时不登记（仍然校验 PSK）。
	Devices DeviceRegistry
	// PairingCodes 校验一次性配对码。为 nil 时 /relay/v1/pair 一律拒绝。
	PairingCodes *paircode.Code
	// Overrides 提供模型覆盖表。为 nil 时该端点返回 501：客户端模式下没有它。
	Overrides OverridesStore
	PSK       string
	Version   string
	Logger    Reporter
}

// DeviceRegistry 是 relay 需要的设备表能力。抽窄接口是为了让调用方能不用
// 真实的 devices.Registry 也能测试。
type DeviceRegistry interface {
	Observe(id, name string)
	IsRevoked(id string) bool
}

// OverridesStore 是 relay 需要的覆盖表能力。抽窄接口是为了让调用方
// 不用构造真实的 catalog.Store 也能测试。
type OverridesStore interface {
	// Snapshot 返回当前表的副本。
	Snapshot() *catalog.Overrides
	// Apply 带乐观并发检查地替换整表；基准不符时返回 catalog.ErrStaleRevision。
	Apply(base int64, models map[string]catalog.Override, by string) (*catalog.Overrides, error)
}

// Reporter 是 relay 记日志的最小接口，避免它依赖具体的日志实现。
type Reporter interface {
	Printf(format string, args ...any)
}

// Server 是挂在网关 relay 端口上的 HTTP 处理器。
type Server struct {
	deps Deps
}

// NewServer 构造 relay 服务端。
// Backend 允许之后用 SetBackend 补上——main 里要先建网关 Server 才能拿到 Handler，
// 两边互相依赖，按顺序装配更清楚。
func NewServer(deps Deps) *Server {
	return &Server{deps: deps}
}

// SetBackend 指定通过鉴权后的请求交给谁处理。
func (s *Server) SetBackend(handler http.Handler) { s.deps.Backend = handler }

// Handler 返回 relay 的全部路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+pingPath, s.authorized(s.handlePing))
	mux.Handle("/v1/", s.authorized(s.forward()))
	mux.HandleFunc("GET "+credentialPath, s.authorized(s.handleCredential))
	mux.HandleFunc("GET "+overridesPath, s.authorized(s.handleGetOverrides))
	mux.HandleFunc("PUT "+overridesPath, s.authorized(s.handlePutOverrides))
	// 配对端点不走 authorized：它要拿短码换长密钥，而长密钥正是它还没有的东西。
	mux.HandleFunc("POST "+pairPath, s.handlePair)
	return mux
}

// authorized 是 relay 自己的准入。
//
// 它与网关 API 端口的 -api-key **没有任何关系**，是两个独立的口子：
// 这里认的是 PSK，那里认的是本地密钥。所以通过鉴权之后还要往上下文里
// 打个标记，让网关知道不必再要求 -api-key。
func (s *Server) authorized(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !PSKMatches(s.deps.PSK, r.Header.Get(HeaderPSK)) {
			writeRelayError(w, http.StatusUnauthorized, "unauthorized",
				"缺少或错误的网关密钥。请在客户端填入网关控制台显示的密钥。")
			return
		}
		deviceID := strings.TrimSpace(r.Header.Get(HeaderDevice))
		if s.deps.Devices != nil && s.deps.Devices.IsRevoked(deviceID) {
			writeRelayError(w, http.StatusForbidden, "device_revoked",
				"这台设备已被停用。要让本网关彻底断某个人的访问，请重置密钥——"+
					"设备名是客户端自报的，停用它能被换一个名字绕过。")
			return
		}
		if s.deps.Devices != nil {
			s.deps.Devices.Observe(deviceID, strings.TrimSpace(r.Header.Get(HeaderDeviceName)))
		}

		ctx := MarkRelay(WithDevice(r.Context(), deviceID))
		next(w, r.WithContext(ctx))
	}
}

// handlePair 用一次性配对码换取长密钥。
//
// 这个端点是唯一一个不需要 PSK 的入口，所以它的防护全在配对码上：
// 5 分钟有效、一次性、错 10 次作废、全局一分钟失败上限。
//
// 它只在**网关模式下转发服务开启时**才有东西可给；客户端模式没有密钥。
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if s.deps.PairingCodes == nil || s.deps.PSK == "" {
		writeRelayError(w, http.StatusNotImplemented, "not_configured",
			"本网关没有启用转发服务，无法配对")
		return
	}
	if s.deps.PairingCodes.Throttled() {
		writeRelayError(w, http.StatusTooManyRequests, "too_many_attempts",
			"配对尝试过于频繁，请稍后再试，或在网关控制台重新生成配对码")
		return
	}
	var request struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&request); err != nil {
		writeRelayError(w, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
		return
	}
	if !s.deps.PairingCodes.Redeem(request.Code) {
		writeRelayError(w, http.StatusUnauthorized, "bad_pairing_code",
			"配对码不正确或已过期。请在网关控制台重新生成一个。")
		return
	}
	if s.deps.Logger != nil {
		s.deps.Logger.Printf("配对成功，已向 %s 发放网关密钥", remoteHost(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"psk":     s.deps.PSK,
		"version": s.deps.Version,
	})
}

// handleGetOverrides 下发网关权威那一份覆盖表。
func (s *Server) handleGetOverrides(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Overrides == nil {
		writeRelayError(w, http.StatusNotImplemented, "not_configured",
			"本网关没有可同步的模型覆盖表")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Overrides.Snapshot())
}

// handlePutOverrides 接收客户端合并后的覆盖表。
//
// 带乐观并发检查：客户端必须说明它是在哪个 revision 上做的合并。
// 网关在这期间被别人改过就返回 409，让客户端重新拉取、重新合并——
// 绝不静默覆盖，那会悄悄丢掉另一个人的改动。
func (s *Server) handlePutOverrides(w http.ResponseWriter, r *http.Request) {
	if s.deps.Overrides == nil {
		writeRelayError(w, http.StatusNotImplemented, "not_configured",
			"本网关没有可同步的模型覆盖表")
		return
	}
	var request struct {
		BaseRevision int64                       `json:"base_revision"`
		Models       map[string]catalog.Override `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		writeRelayError(w, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
		return
	}
	by := DeviceFrom(r.Context())
	saved, err := s.deps.Overrides.Apply(request.BaseRevision, request.Models, by)
	switch {
	case errors.Is(err, catalog.ErrStaleRevision):
		writeRelayError(w, http.StatusConflict, "stale_revision",
			"网关上的模型覆盖表在这期间被别人改过。请重新拉取后再合并一次。")
		return
	case err != nil:
		writeRelayError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.deps.Logger != nil {
		s.deps.Logger.Printf("设备 %s 同步了模型覆盖表（revision %d）", by, saved.Revision)
	}
	writeJSON(w, http.StatusOK, saved)
}

// remoteHost 取出调用方的地址（不含端口），只用于日志。
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// forward 把请求交给网关自己的 Handler。
func (s *Server) forward() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Backend == nil {
			writeRelayError(w, http.StatusNotImplemented, "not_configured",
				"本网关没有启用转发服务")
			return
		}
		s.deps.Backend.ServeHTTP(w, r)
	}
}

// handlePing 让客户端确认「网关在不在」。不含任何账号信息。
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"version":  s.deps.Version,
		"protocol": ProtocolVersion,
	})
}

// CredentialSource 是网关侧提供凭证的能力。抽成接口是为了让测试不必
// 真的构造一个 credential.Manager。
type CredentialSource interface {
	// CredentialEnvelope 返回要下发的凭证，以及网关侧的 epoch。
	CredentialEnvelope() (credentialsync.Envelope, error)
}

// handleCredential 下发凭证。
//
// 关着的时候返回 403 而不是 404：404 会让客户端以为地址写错了，
// 而真正的原因是「网关没允许同步」。说清楚差别能省一轮排查。
func (s *Server) handleCredential(w http.ResponseWriter, r *http.Request) {
	// 用反射排除「类型化的 nil」：传了 (*T)(nil) 时接口本身非空，
	// 直接判等会漏过去，然后在调用时 panic——那会让整个连接断掉。
	if s.deps.CredentialSource == nil || reflect.ValueOf(s.deps.CredentialSource).IsNil() {
		writeRelayError(w, http.StatusForbidden, "credential_sync_disabled",
			"网关未开放凭证同步。请在网关控制台「多设备」页开启。"+
				"这个端点会交出你的账号令牌，默认必须是关的。")
		return
	}
	envelope, err := s.deps.CredentialSource.CredentialEnvelope()
	if err != nil {
		writeRelayError(w, http.StatusInternalServerError, "credential_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, envelope)
}
