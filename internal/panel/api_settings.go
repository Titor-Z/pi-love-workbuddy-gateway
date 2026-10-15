package panel

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/settings"
)

// settingsResponse 是设置页「日志」区块的数据。
//
// 区间也一并下发：前端的校验提示要与后端一致，各写一份常量迟早会漂移。
type settingsResponse struct {
	Settings settings.Settings `json:"settings"`
	Path     string            `json:"path"`

	MinPageSize      int `json:"min_page_size"`
	MaxPageSize      int `json:"max_page_size"`
	MinRetentionDays int `json:"min_retention_days"`
	MaxRetentionDays int `json:"max_retention_days"`
}

// handleGetSettings 返回当前的运行偏好与允许区间。
func (p *Panel) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	if p.deps.Settings == nil {
		p.fail(w, "读取偏好", fmt.Errorf("当前模式没有可用的偏好存储"))
		return
	}
	p.writeSettings(w, p.deps.Settings.Get())
}

// handlePutSettings 保存运行偏好，并对新保留天数立刻清理一次。
//
// 立刻清理是有意的：用户把保留期从 30 天改成 1 天，就是想让旧日志现在消失，
// 而不是等下一次定时任务。
func (p *Panel) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if p.deps.Settings == nil {
		p.fail(w, "保存偏好", fmt.Errorf("当前模式没有可用的偏好存储"))
		return
	}
	var payload settings.Settings
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "偏好格式不对："+err.Error())
		return
	}
	if err := p.deps.Settings.Update(payload); err != nil {
		// 校验失败是用户填错了，回 400 让他改，而不是 502。
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if removed, err := p.deps.Journal.Prune(payload.LogRetentionDays); err != nil {
		p.deps.Logger.Printf("✗ 按新保留期清理日志失败：%v", err)
	} else if len(removed) > 0 {
		p.deps.Logger.Printf("按保留 %d 天清理了 %d 个日志分片：%v",
			payload.LogRetentionDays, len(removed), removed)
	}
	p.writeSettings(w, p.deps.Settings.Get())
}

// writeSettings 输出偏好与允许区间。
func (p *Panel) writeSettings(w http.ResponseWriter, current settings.Settings) {
	writeJSON(w, http.StatusOK, settingsResponse{
		Settings:         current,
		Path:             p.deps.SettingsPath,
		MinPageSize:      settings.MinPageSize,
		MaxPageSize:      settings.MaxPageSize,
		MinRetentionDays: settings.MinRetentionDays,
		MaxRetentionDays: settings.MaxRetentionDays,
	})
}
