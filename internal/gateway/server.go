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
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// maxErrorBody 是读取后端错误响应体的上限，避免异常响应撑爆内存。
const maxErrorBody = 64 << 10

// Server 是本地网关。它不持有任何状态，所有请求都转发到后端的实时接口。
type Server struct {
	cfg        *config.Config
	cred       *credential.Manager
	up         *upstream.Client
	promotions *catalog.Promotions
	logger     *logging.Logger
	journal    *journal.Journal // 允许为 nil（测试场景）
}

// NewServer 构造网关。events 允许为 nil。
func NewServer(cfg *config.Config, cred *credential.Manager, up *upstream.Client,
	promotions *catalog.Promotions, logger *logging.Logger, events *journal.Journal) *Server {
	return &Server{cfg: cfg, cred: cred, up: up, promotions: promotions,
		logger: logger, journal: events}
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
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	summary := map[string]any{
		"status":    "ok",
		"auth_file": s.cred.Path(),
	}
	for key, value := range s.cred.Summary() {
		summary[key] = value
	}
	writeJSON(w, http.StatusOK, summary)
}

// authorize 校验本地访问密钥；未配置密钥时直接放行。
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.APIKey == "" {
		return true
	}
	if bearerToken(r) == s.cfg.APIKey || r.Header.Get("X-Api-Key") == s.cfg.APIKey {
		return true
	}
	writeError(w, http.StatusUnauthorized, "invalid_request_error", "本地访问密钥不正确")
	return false
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
