package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
)

// statusResponse 是概览页顶部的状态数据。
type statusResponse struct {
	Version        string         `json:"version"`
	StartedAt      time.Time      `json:"started_at"`
	UptimeSeconds  int64          `json:"uptime_seconds"`
	PID            int            `json:"pid"`
	ListenAddr     string         `json:"listen_addr"`
	ConsoleAddr    string         `json:"console_addr"`
	AuthFile       string         `json:"auth_file"`
	LogPath        string         `json:"log_path"`
	JournalPath    string         `json:"journal_path"`
	PromotionsPath string         `json:"promotions_path"`
	Account        map[string]any `json:"account"`
}

// handleStatus 返回网关自身的状态与账号摘要。
func (p *Panel) handleStatus(w http.ResponseWriter, _ *http.Request) {
	account := p.deps.Credential.Summary()
	delete(account, "error") // 账号读不出来时不要在面板上暴露内部错误文本
	if _, ok := account["error"]; !ok {
		// 保留原样
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Version:        p.deps.Version,
		StartedAt:      p.deps.StartedAt,
		UptimeSeconds:  int64(time.Since(p.deps.StartedAt).Seconds()),
		PID:            os.Getpid(),
		ListenAddr:     p.deps.ListenAddr,
		ConsoleAddr:    p.deps.ConsoleAddr,
		AuthFile:       p.deps.AuthFile,
		LogPath:        p.deps.LogPath,
		JournalPath:    p.deps.JournalPath,
		PromotionsPath: p.deps.PromotionsPath,
		Account:        account,
	})
}

// billingPackage 是一个资源包的对外表示。
type billingPackage struct {
	Name        string     `json:"name"`
	Remain      float64    `json:"remain"`
	Size        float64    `json:"size"`
	Use         float64    `json:"use"`
	TotalRemain float64    `json:"total_remain"`
	CycleEnd    *time.Time `json:"cycle_end,omitempty"`
	Expire      *time.Time `json:"expire,omitempty"`
}

// billingResponse 是余额页的数据。
type billingResponse struct {
	FetchedAt   time.Time        `json:"fetched_at"`
	TotalRemain float64          `json:"total_remain"`
	Packages    []billingPackage `json:"packages"`
	// SettleHint 告诉前端该提示用户什么，免得把提示文案写死在前端。
	SettleHint string `json:"settle_hint"`
}

// handleBilling 读取资源包余额。面板不做缓存：账单接口本身有分钟级延迟，
// 再叠一层缓存只会让数字更难解释。
func (p *Panel) handleBilling(w http.ResponseWriter, r *http.Request) {
	snapshot, err := p.deps.Billing.Fetch(r.Context())
	if err != nil {
		p.fail(w, "读取余额", err)
		return
	}
	response := billingResponse{
		FetchedAt:   snapshot.FetchedAt,
		TotalRemain: snapshot.TotalRemain(),
		Packages:    make([]billingPackage, 0, len(snapshot.Packages)),
		SettleHint:  "账单结算有 1～3 分钟延迟，刚发完的请求可能尚未计入",
	}
	for _, item := range snapshot.Packages {
		entry := billingPackage{
			Name:        item.Name,
			Remain:      item.Remain,
			Size:        item.Size,
			Use:         item.Use,
			TotalRemain: item.TotalRemain,
		}
		if !item.CycleEndTime.IsZero() {
			entry.CycleEnd = &item.CycleEndTime
		}
		if !item.ExpireTime.IsZero() {
			entry.Expire = &item.ExpireTime
		}
		response.Packages = append(response.Packages, entry)
	}
	writeJSON(w, http.StatusOK, response)
}

// modelEntry 是模型页的一行。
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
}

// handleModels 返回实时模型目录，带费用标记。
func (p *Panel) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := p.deps.Upstream.Models(r.Context())
	if err != nil {
		p.fail(w, "拉取模型目录", err)
		return
	}
	entries := catalog.Build(models, p.promotions())
	list := make([]modelEntry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, modelEntry{
			ID:          entry.Model.ID,
			Name:        entry.Model.Name,
			DisplayName: entry.DisplayName(),
			Free:        entry.Label.Free,
			Credits:     entry.Label.Credits,
			Badge:       entry.Label.Badge,
			FreeUntil:   entry.Label.FreeUntil,
			Note:        entry.Label.Note,
			Context:     entry.Model.MaxInputTokens,
			MaxOutput:   entry.Model.MaxOutputTokens,
			Images:      entry.HasImageInput(),
			Thinking:    entry.Model.SupportsThinking,
			Efforts:     entry.Model.Efforts,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": list})
}

// handlePiModels 直接输出可落盘的 pi 配置，供面板上的「下载」按钮使用。
func (p *Panel) handlePiModels(w http.ResponseWriter, r *http.Request) {
	models, err := p.deps.Upstream.Models(r.Context())
	if err != nil {
		p.fail(w, "生成 pi 配置", err)
		return
	}
	entries := catalog.Build(models, p.promotions())

	baseURL := "http://" + p.deps.ListenAddr + "/v1"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="models.json"`)
	w.WriteHeader(http.StatusOK)
	if err := catalog.WritePiConfig(w, entries, baseURL, ""); err != nil {
		p.deps.Logger.Printf("✗ 输出 pi 配置失败：%v", err)
	}
}

// handleEvents 返回最近的运行事件。数据量小，尾部截取即可。
func (p *Panel) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = min(parsed, 1000)
		}
	}
	records, err := p.deps.Journal.ReadSince(time.Time{})
	if err != nil {
		p.fail(w, "读取事件", err)
		return
	}
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": records})
}

// handleGetPromotions 返回促销配置文件的原文，让前端能直接编辑。
func (p *Panel) handleGetPromotions(w http.ResponseWriter, _ *http.Request) {
	raw, err := os.ReadFile(p.deps.PromotionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在是正常状态，返回空对象让前端从零开始填。
			writeJSON(w, http.StatusOK, map[string]any{"promotions": map[string]any{},
				"path": p.deps.PromotionsPath})
			return
		}
		p.fail(w, "读取促销配置", err)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		p.fail(w, "解析促销配置", fmt.Errorf("文件不是合法 JSON：%w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"promotions": parsed, "path": p.deps.PromotionsPath,
	})
}

// handlePutPromotions 覆盖写入促销配置。
//
// 写入前先校验结构：这个文件坏了会让模型列表里的「免费到哪天」整片消失，
// 而用户往往是手改出错的。宁可拒绝，也不要把坏文件落盘。
func (p *Panel) handlePutPromotions(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Promotions map[string]catalog.Promotion `json:"promotions"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			"促销配置格式不对："+err.Error())
		return
	}
	if payload.Promotions == nil {
		payload.Promotions = map[string]catalog.Promotion{}
	}

	raw, err := json.MarshalIndent(payload.Promotions, "", "  ")
	if err != nil {
		p.fail(w, "序列化促销配置", err)
		return
	}
	raw = append(raw, '\n')

	if err := os.MkdirAll(filepath.Dir(p.deps.PromotionsPath), 0o700); err != nil {
		p.fail(w, "创建配置目录", err)
		return
	}
	tmp := p.deps.PromotionsPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		p.fail(w, "写入促销配置", err)
		return
	}
	if err := os.Rename(tmp, p.deps.PromotionsPath); err != nil {
		p.fail(w, "替换促销配置", err)
		return
	}

	// 让面板立刻看到新配置，不必等重启。
	reloaded, err := catalog.LoadPromotions(p.deps.PromotionsPath)
	if err != nil {
		p.fail(w, "重新加载促销配置", err)
		return
	}
	p.setPromotions(reloaded)
	p.deps.Logger.Printf("面板更新了促销配置：%s", p.deps.PromotionsPath)

	writeJSON(w, http.StatusOK, map[string]any{
		"promotions": payload.Promotions, "path": p.deps.PromotionsPath,
	})
}
