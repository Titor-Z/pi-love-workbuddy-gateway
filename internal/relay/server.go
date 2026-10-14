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
	"net/http"
	"strings"
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

// Deps 是 relay 服务端的依赖。
type Deps struct {
	// Backend 接收通过鉴权的请求。生产里它就是网关自己的 Handler——
	// 两者同进程，直接挂过去，不做 HTTP 反代：少一跳，也少一次可能破坏
	// SSE 的缓冲。
	Backend http.Handler
	// Devices 登记到访设备；为 nil 时不登记（仍然校验 PSK）。
	Devices DeviceRegistry
	PSK     string
	Version string
	Logger  Reporter
}

// DeviceRegistry 是 relay 需要的设备表能力。抽窄接口是为了让调用方能不用
// 真实的 devices.Registry 也能测试。
type DeviceRegistry interface {
	Observe(id, name string)
	IsRevoked(id string) bool
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
