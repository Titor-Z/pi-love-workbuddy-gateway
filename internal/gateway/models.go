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
	Free      bool        `json:"free"`                 // 是否免费
	Credits   string      `json:"credits,omitempty"`    // 计费倍率，如 "0.29x"
	Badge     string      `json:"badge,omitempty"`      // 促销徽章文案
	FreeUntil string      `json:"free_until,omitempty"` // 免费截止日期
	Note      string      `json:"note,omitempty"`       // 本地备注
	Source    modelSource `json:"source"`               // 上游原始信息，供客户端模式还原
	// DisplayName 是**算好的**展示名（别名 + 费用标记 + 活动 + 截止日期）。
	// 发给 pi 的插件用：拼接规则只写在 catalog.DisplayName 一处，
	// 否则每多一个消费者就要在另一种语言里再实现一遍，早晚不一致。
	DisplayName string `json:"display_name,omitempty"`
	// ThinkingLevelMap 是用户设的思考档位映射，原样进 pi 的模型配置。
	ThinkingLevelMap map[string]*string `json:"thinking_level_map,omitempty"`
}

// modelSource 是重建 upstream.ModelInfo 所需的原始字段。
//
// 为什么不能不传：上面那几个是**合成后的标签**，不可逆——
// "0.29x" 还原不回倍率数值，徽章被抽出来了、原始 tags 没了，
// 上下文窗口与思考档位也根本不在标签里。客户端模式要自己算一遍标签
// （它可能持有不同的促销配置），就必须拿到原始值。
type modelSource struct {
	Name          string   `json:"name"`                     // 模型原名
	Credits       string   `json:"credits,omitempty"`        // 原始倍率，如 "x0.29 credits"
	Tags          []string `json:"tags,omitempty"`           // 后端标签，含促销徽章
	ContextWindow int      `json:"context_window,omitempty"` // 上下文窗口
	MaxTokens     int      `json:"max_tokens,omitempty"`     // 单次最大输出
	Images        bool     `json:"images,omitempty"`         // 接受图片输入
	Tools         bool     `json:"tools,omitempty"`          // 支持工具调用
	Thinking      bool     `json:"thinking,omitempty"`       // 支持思考模式
	Efforts       []string `json:"efforts,omitempty"`        // 可选思考档位
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
	entries := catalog.Visible(catalog.Build(models, s.overrides.Snapshot()))
	list := make([]modelObject, 0, len(entries))
	for _, entry := range entries {
		list = append(list, modelObject{
			ID:      entry.Model.ID,
			Object:  "model",
			Created: created,
			OwnedBy: "workbuddy",
			WorkBuddy: modelBilling{
				Free:             entry.Label.Free,
				Credits:          entry.Label.Credits,
				Badge:            entry.Label.Badge,
				FreeUntil:        entry.Label.FreeUntil,
				Note:             entry.Label.Note,
				DisplayName:      entry.DisplayName(),
				ThinkingLevelMap: entry.Thinking,
				Source: modelSource{
					Name:          entry.Model.Name,
					Credits:       entry.Model.Credits,
					Tags:          entry.Model.Tags,
					ContextWindow: entry.Model.MaxInputTokens,
					MaxTokens:     entry.Model.MaxOutputTokens,
					Images:        entry.Model.SupportsImages,
					Tools:         entry.Model.SupportsToolCall,
					Thinking:      entry.Model.SupportsThinking,
					Efforts:       entry.Model.Efforts,
				},
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": list})
}
