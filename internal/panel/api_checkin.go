package panel

import (
	"fmt"
	"net/http"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/workbuddy"
)

// checkinResponse 是签到页要的全部数据：当前状态 + 本地记录。
//
// 两样都给是因为它们回答不同的问题：状态回答「今天签没签」，
// 记录回答「前几天签得怎么样」。面板顶部显示前者，列表显示后者。
type checkinResponse struct {
	// Available 为假表示当前模式不支持签到（客户端模式没有凭证）。
	Available bool               `json:"available"`
	Status    *workbuddy.Status  `json:"status,omitempty"`
	Recent    []workbuddy.Record `json:"recent"`
	// Error 非空表示这次查状态失败。状态字段可能过期，但记录仍然可用。
	Error string `json:"error,omitempty"`
	// Reminder 非空表示首页通知区该提醒签到了。
	//
	// 「什么时候该提醒」是策略，放服务端：前端不该自己拿设置与状态
	// 拼一遇条件（那样每多一个展示入口就要重写一通），它只管把这句话画出来。
	Reminder string `json:"reminder,omitempty"`
}

// handleCheckin 返回签到状态与最近记录。
//
// 状态查询失败不当作整体失败：本机记录仍然有价值，
// 前端能显示「今天签没签 + 查不到实时状态」而不是一整块报错。
func (p *Panel) handleCheckin(w http.ResponseWriter, r *http.Request) {
	if p.deps.Checkin == nil {
		writeJSON(w, http.StatusOK, checkinResponse{
			Available: false,
			Error:     "当前模式没有签到能力：签到需要账号凭证，而客户端模式按设计不持有凭证。",
		})
		return
	}
	response := checkinResponse{Available: true}
	if status, err := p.deps.Checkin.CheckinStatus(r.Context()); err != nil {
		response.Error = err.Error()
	} else {
		response.Status = &status
	}
	if store := p.deps.Checkin; store != nil {
		response.Recent = store.CheckinRecent(30)
	}
	response.Reminder = p.checkinReminder(response.Status)
	writeJSON(w, http.StatusOK, response)
}

// checkinReminder 决定首页要不要提醒签到，并给出那句提醒。
//
// 四个条件缺一不可：用户开了提醒开关、活动在期、今天还没签、状态拿得到。
// 任何一条不满足就静默——宁可少提醒一次，也不要拿一个查不到的状态去吓人。
func (p *Panel) checkinReminder(status *workbuddy.Status) string {
	if status == nil || p.deps.Settings == nil {
		return ""
	}
	if !p.deps.Settings.Get().CheckinRemind {
		return ""
	}
	if !status.Active || status.CheckedInToday {
		return ""
	}
	return fmt.Sprintf("今天的签到还没领（连续 %d 天）。签到页可以立刻签。", status.StreakDays)
}

// handleCheckinRun 执行一次签到。
//
// 幂等：今天已经签过也返回 200，只是 result.already 为真。
// 这里**不用 POST 语义上的错误码**区分「已签过」——那不是错误，
// 用户点了按钮看到「今天已经签过了」才是对的。
func (p *Panel) handleCheckinRun(w http.ResponseWriter, r *http.Request) {
	if p.deps.Checkin == nil {
		p.fail(w, "签到", fmt.Errorf("当前模式没有签到能力：签到需要账号凭证"))
		return
	}
	result, err := p.deps.Checkin.CheckinNow(r.Context())
	if err != nil {
		p.fail(w, "签到", err)
		return
	}
	if !result.Done {
		// 「没签成」不是 HTTP 错误，是业务结果：给 200，让前端展示原因。
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "result": result})
		return
	}
	p.deps.Logger.Printf("手动签到：%s（连续 %d 天，+%v）", result.Message, result.StreakDays, result.Credit)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}
