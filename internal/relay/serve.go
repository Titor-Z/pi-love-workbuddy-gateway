package relay

import (
	"encoding/json"
	"net/http"
)

// ProtocolVersion 是转发协议的版本。客户端与网关不一致时连不上，
// 比连上了才发现字段对不上好。
const ProtocolVersion = 1

// writeJSON 写 JSON 响应。HTML 转义无关紧要（这些响应不含标记字符），
// 但保持一致比在这里特殊化简单。
func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeRelayError 是 relay 侧的错误响应。
//
// 结构照 OpenAI 的 {error:{message,type}} 来：客户端可能直接把它当后端错误看，
// 换个形状会让 SDK 解析不出信息。
func writeRelayError(w http.ResponseWriter, code int, errorType, message string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{"message": message, "type": errorType},
	})
}
