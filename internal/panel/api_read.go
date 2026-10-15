package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
)

// statusResponse 是概览页顶部的状态数据。
type statusResponse struct {
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	PID           int       `json:"pid"`
	ListenAddr    string    `json:"listen_addr"`
	ConsoleAddr   string    `json:"console_addr"`
	AuthFile      string    `json:"auth_file"`
	LogPath       string    `json:"log_path"`
	JournalPath   string    `json:"journal_path"`
	OverridesPath string    `json:"overrides_path"`
	Mode          string    `json:"mode"`
	// Paired 为假时前端应显示配对表单而不是「连不上网关」。
	Paired         bool           `json:"paired"`
	PairingAddress string         `json:"pairing_address,omitempty"`
	PairingError   string         `json:"pairing_error,omitempty"`
	Account        map[string]any `json:"account"`
}

// handleStatus 返回网关自身的状态与账号摘要。
//
// 客户端模式没有凭证（Credential 为 nil），此时 account 留空而不是硬读——
// 一个「读取失败」在这里等于骗人：本来就没有东西可读。
func (p *Panel) handleStatus(w http.ResponseWriter, _ *http.Request) {
	var account map[string]any
	if p.deps.Credential != nil {
		account = p.deps.Credential.Summary()
		delete(account, "error") // 账号读不出来时不要在面板上暴露内部错误文本
	}
	paired := p.deps.PairingErr == nil
	pairingError := ""
	if p.deps.PairingErr != nil {
		pairingError = p.deps.PairingErr.Error()
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Paired:         paired,
		PairingAddress: p.deps.PairingAddress,
		PairingError:   pairingError,
		Version:        p.deps.Version,
		StartedAt:      p.deps.StartedAt,
		UptimeSeconds:  int64(time.Since(p.deps.StartedAt).Seconds()),
		PID:            os.Getpid(),
		Mode:           p.deps.Mode,
		ListenAddr:     p.deps.ListenAddr,
		ConsoleAddr:    p.deps.ConsoleAddr,
		AuthFile:       p.deps.AuthFile,
		LogPath:        p.deps.LogPath,
		JournalPath:    p.deps.JournalPath,
		OverridesPath:  p.deps.OverridesPath,
		Account:        account,
	})
}

// handleBilling 读取资源包余额。面板不做缓存：账单接口本身有分钟级延迟，
// 再叠一层缓存只会让数字更难解释。
//
// 客户端模式下余额来自网关（经 relay 透传）——凭证在网关那台机器上，
// 本机没有账单可读。用户不必为了看一眼余额跑回网关的控制台。
func (p *Panel) handleBilling(w http.ResponseWriter, r *http.Request) {
	if p.deps.Billing == nil {
		writeError(w, http.StatusNotImplemented, "not_configured",
			"当前模式没有可用的余额来源")
		return
	}
	report, err := p.deps.Billing.FetchBillingReport(r.Context())
	if err != nil {
		p.fail(w, "读取余额", err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// modelEntry 是模型页的一行。既有合成后的标签，也有覆盖项原文——
// 编辑页要拿覆盖项回填表单。
type modelEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Free        bool     `json:"free"`
	Credits     string   `json:"credits,omitempty"`
	Badge       string   `json:"badge,omitempty"`
	FreeUntil   string   `json:"free_until,omitempty"`
	Note        string   `json:"note,omitempty"`
	Context     int      `json:"context"`
	MaxOutput   int      `json:"max_output"`
	Images      bool     `json:"images"`
	Thinking    bool     `json:"thinking"`
	Efforts     []string `json:"efforts,omitempty"`

	// 以下是用户写的覆盖项（回填编辑表单用）。
	Alias            string             `json:"alias,omitempty"`
	Hidden           bool               `json:"hidden,omitempty"`
	ThinkingLevelMap map[string]*string `json:"thinking_level_map,omitempty"`
	UpdatedAt        string             `json:"updated_at,omitempty"`
	UpdatedBy        string             `json:"updated_by,omitempty"`
}

// handleModels 返回实时模型目录，带费用标记。
func (p *Panel) handleModels(w http.ResponseWriter, r *http.Request) {
	if p.deps.Upstream == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "当前模式没有可用的后端")
		return
	}
	models, err := p.deps.Upstream.Models(r.Context())
	if err != nil {
		p.fail(w, "拉取模型目录", err)
		return
	}
	// 编辑页要看得见被隐藏的模型，否则没法把它们放出来；
	// 对外转发（/v1/models、pi 配置）才过滤。
	entries := catalog.Build(models, p.overrides())
	list := make([]modelEntry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, modelEntry{
			ID:               entry.Model.ID,
			Name:             entry.Model.Name,
			DisplayName:      entry.DisplayName(),
			Free:             entry.Label.Free,
			Credits:          entry.Label.Credits,
			Badge:            entry.Label.Badge,
			FreeUntil:        entry.Label.FreeUntil,
			Note:             entry.Label.Note,
			Context:          entry.Model.MaxInputTokens,
			MaxOutput:        entry.Model.MaxOutputTokens,
			Images:           entry.HasImageInput(),
			Thinking:         entry.Model.SupportsThinking,
			Efforts:          entry.Model.Efforts,
			Alias:            entry.Alias,
			Hidden:           entry.Hidden,
			ThinkingLevelMap: entry.Thinking,
			UpdatedAt:        entry.UpdatedAt,
			UpdatedBy:        entry.UpdatedBy,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": list})
}

// handlePiModels 直接输出可落盘的 pi 配置，供面板上的「下载」按钮使用。
func (p *Panel) handlePiModels(w http.ResponseWriter, r *http.Request) {
	if p.deps.Upstream == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "当前模式没有可用的后端")
		return
	}
	models, err := p.deps.Upstream.Models(r.Context())
	if err != nil {
		p.fail(w, "生成 pi 配置", err)
		return
	}
	entries := catalog.Visible(catalog.Build(models, p.overrides()))

	baseURL := "http://" + p.deps.ListenAddr + "/v1"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="models.json"`)
	w.WriteHeader(http.StatusOK)
	if err := catalog.WritePiConfig(w, entries, baseURL, ""); err != nil {
		p.deps.Logger.Printf("✗ 输出 pi 配置失败：%v", err)
	}
}

// handleEvents 返回最近的运行事件。
//
// 只读保留期内的分片：更早的本来就被清理掉了，限定范围也让「用得越久读得越慢」
// 不会发生。默认条数由前端传，后端只负责给完整数据。
func (p *Panel) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = min(parsed, 5000)
		}
	}
	since := time.Time{}
	if p.deps.Settings != nil {
		days := p.deps.Settings.Get().LogRetentionDays
		since = time.Now().AddDate(0, 0, -(days - 1))
	}
	records, err := p.deps.Journal.Read(since)
	if err != nil {
		p.fail(w, "读取事件", err)
		return
	}
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": records})
}

// handleGetOverrides 返回模型覆盖表。每次从磁盘读：
// 用户可能在编辑器外面手改了这个文件，面板不该拿内存里那份骗人。
func (p *Panel) handleGetOverrides(w http.ResponseWriter, _ *http.Request) {
	table, err := catalog.LoadOverrides(p.deps.OverridesPath)
	if err != nil {
		p.fail(w, "读取模型覆盖表", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"overrides": table, "path": p.deps.OverridesPath,
	})
}

// handlePutOverrides 覆盖写入模型覆盖表。
//
// 写入前先校验结构：这个文件坏了会让别名、免费截止、思考档位整片失效，
// 而用户往往是手改出错的。宁可拒绝，也不要把坏文件落盘。
func (p *Panel) handlePutOverrides(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Models map[string]catalog.Override `json:"models"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"模型覆盖表格式不对："+err.Error())
		return
	}
	next := catalog.NewOverrides()
	next.Models = payload.Models
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	// 记一次写入：revision 递增，并给真的变了的条目打上时间与作者。
	// 这两样是日后与另一台机器做三方合并的依据。
	saved, err := p.deps.Overrides.Replace(next.Models, p.deviceID())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	p.deps.Logger.Printf("面板更新了模型覆盖表（revision %d）：%s", saved.Revision, p.deps.OverridesPath)

	writeJSON(w, http.StatusOK, map[string]any{
		"overrides": saved, "path": p.deps.OverridesPath,
	})
}

// deviceID 给出「这次改动是哪台机器做的」。
// 设备身份为 nil（极早期启动或测试）时不编造，如实留空。
func (p *Panel) deviceID() string {
	if p.deps.Device == nil {
		return ""
	}
	return p.deps.Device.ID
}
