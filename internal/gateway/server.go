// Package gateway 提供本地 OpenAI 兼容 HTTP 接口，供 pi 接入。
//
// 只实现两个端点：GET /v1/models 与 POST /v1/chat/completions。
// 响应侧是纯字节透传——后端返回的本来就是标准 OpenAI SSE，
// 网关只负责补上鉴权、做少量请求改写，并把鉴权失败翻译成友好错误。
package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/logging"
)

// maxErrorBody 是读取后端错误响应体的上限，避免异常响应撑爆内存。
const maxErrorBody = 64 << 10

// Server 是本地网关。它不持有任何状态，所有请求都转发到后端的实时接口。
type Server struct {
	cfg        *config.Config
	cred       *credential.Manager
	up         Backend
	promotions *catalog.Promotions
	logger     *logging.Logger
	journal    *journal.Journal // 允许为 nil（测试场景）

	// gate 是并发闸门。它属于 Server 而不是 Backend：
	// 网关的两个入口（本机 API 与局域网 relay）都挂同一个 Handler，
	// 所以闸门自动对两条路径都生效，也不可能被其中一条绕过。
	gate *Gate
}

// NewServer 构造网关。cred 与 events 都允许为 nil：
// 客户端模式的网关不持有凭证，health 接口因此不会有账号摘要。
//
// maxConcurrent 是对后端并发的上限，<=0 表示不限。它保护的是**后端**，
// 因为多条入口（本机直连、局域网客户端）最终都归到同一个 WorkBuddy 账号上，
// 并发翻倍会让本就存在的 429 提前到来。
func NewServer(cfg *config.Config, cred *credential.Manager, up Backend,
	promotions *catalog.Promotions, logger *logging.Logger, events *journal.Journal,
	maxConcurrent int) *Server {
	return &Server{cfg: cfg, cred: cred, up: up, promotions: promotions,
		logger: logger, journal: events, gate: NewGate(maxConcurrent)}
}

// Handler 返回本地网关的 HTTP 处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	return mux
}

// handleHealth 返回进程与账号的摘要信息，不含任何凭据。
//
// 客户端模式没有凭证（s.cred 为 nil），这时如实说明自己在转发，
// 而不是返回一个空壳的账号状态——那样看起来像「凭证坏了」。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	summary := map[string]any{"status": "ok"}
	if s.cred == nil {
		summary["auth_file"] = ""
		summary["mode"] = "relay-only"
	} else {
		summary["auth_file"] = s.cred.Path()
		for key, value := range s.cred.Summary() {
			summary[key] = value
		}
	}
	writeJSON(w, http.StatusOK, summary)
}

// authorize 是 API 端口的准入。
//
// 不设密钥。它只听 127.0.0.1；而「能访问本机 API 的程序」本来就
// 能直接读配置目录里的登录态，再加一道密钥挡不住任何东西，
// 只会让每次重启都要改 pi 的配置。
//
// relay 端口是另一回事：它必须对局域网开放，所以有自己的一套
// 真准入（PSK + TLS 指纹 + 设备吊销），在 relay 包里。
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	return true
}

// bearerToken 从 Authorization 头里取出 Bearer 令牌。
func bearerToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
}

// decodeJSONBody 解析请求体，并保留数字的原始精度。
func decodeJSONBody(r *http.Request) (map[string]any, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("请求体为空")
		}
		return nil, err
	}
	return body, nil
}

// writeJSON 输出一个 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeError 输出一个 OpenAI 风格的错误响应。
func writeError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": message, "type": kind},
	})
}
