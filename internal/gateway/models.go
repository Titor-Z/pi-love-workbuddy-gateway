package gateway

import (
	"net/http"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
)

// modelObject 是本网关对外暴露的模型条目，遵循 OpenAI 的 /v1/models 结构。
// pi 并不会读这个接口——它只认 ~/.pi/agent/models.json——这里保留是为了
// 让其它 OpenAI 兼容客户端也能直接探到可用模型。
type modelObject struct {
	ID        string       `json:"id"`
	Object    string       `json:"object"`
	Created   int64        `json:"created"`
	OwnedBy   string       `json:"owned_by"`
	WorkBuddy modelBilling `json:"workbuddy"`
}

// modelBilling 是费用标记，作为厂商扩展字段挂在模型条目上。
type modelBilling struct {
	Free      bool   `json:"free"`                 // 是否免费
	Credits   string `json:"credits,omitempty"`    // 计费倍率，如 "0.29x"
	Badge     string `json:"badge,omitempty"`      // 促销徽章文案
	FreeUntil string `json:"free_until,omitempty"` // 免费截止日期
	Note      string `json:"note,omitempty"`       // 本地备注
}

// handleModels 返回后端的实时模型目录，并带上费用标记。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.up == nil {
		writeError(w, http.StatusServiceUnavailable, "not_paired",
			"本机尚未与网关配对。请打开控制台「多设备」页填入网关密钥并确认证书指纹。")
		return
	}
	if !s.authorize(w, r) {
		return
	}
	models, err := s.up.Models(r.Context())
	if err != nil {
		s.logger.Printf("✗ 拉取模型目录失败：%v", err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	created := time.Now().Unix()
	entries := catalog.Build(models, s.promotions)
	list := make([]modelObject, 0, len(entries))
	for _, entry := range entries {
		list = append(list, modelObject{
			ID:      entry.Model.ID,
			Object:  "model",
			Created: created,
			OwnedBy: "workbuddy",
			WorkBuddy: modelBilling{
				Free:      entry.Label.Free,
				Credits:   entry.Label.Credits,
				Badge:     entry.Label.Badge,
				FreeUntil: entry.Label.FreeUntil,
				Note:      entry.Label.Note,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": list})
}
