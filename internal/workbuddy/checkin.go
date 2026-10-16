// Package workbuddy 是 WorkBuddy 账号级操作（签到等）的领域层。
//
// 它与网关的转发能力完全无关：转发把请求转给后端，这里是「替这个账号做一件
// 它自己的事」。所以它不 import gateway / panel / journal，只依赖一个能给出
// 请求头的窄接口，将来单独抽成工具包时一起搬走的只有 wbapi 那几个常量。
//
// **本包只提供「执行一次」的动作，不提供调度。** 调度器只能有一个：
// 网关自己的定时器、控制台按钮、以及外部的 MCP 看板都是调用方，
// 它们各自决定什么时候调。包自己带 interval 会与看板打架。
package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/wbapi"
)

// TokenSource 给出带最新访问令牌的请求头。
//
// 抽成接口而不是直接依赖 credential.Manager：测试可以塞 httptest，
// 将来换个进程持有凭证也不必改这个包。
type TokenSource interface {
	Headers() (map[string]string, error)
}

// requestTimeout 是元信息接口的超时。签到很快，不该拖住调用方。
const requestTimeout = 20 * time.Second

// Client 是 WorkBuddy 账号操作客户端。
type Client struct {
	http    *http.Client
	tokens  TokenSource
	baseURL string // 根地址，留空用官方地址；测试可注入
}

// NewClient 构造客户端。baseURL 留空时指向官方签到域名。
func NewClient(tokens TokenSource, baseURL string) *Client {
	if baseURL == "" {
		baseURL = wbapi.BillingBaseURL
	}
	return &Client{
		http:    &http.Client{Timeout: requestTimeout},
		tokens:  tokens,
		baseURL: baseURL,
	}
}

// Status 是一次签到状态查询的结果。
type Status struct {
	// Active 表示签到活动是否在期内。不在期时其余字段都无意义。
	Active bool `json:"active"`
	// CheckedInToday 是今天是否已签到。它才是判断「还要不要签」的依据。
	CheckedInToday bool `json:"checked_in_today"`
	// StreakDays 是当前连续签到天数。
	StreakDays int `json:"streak_days"`
	// DailyCredit 是当天签到给多少积分。
	DailyCredit float64 `json:"daily_credit"`
	// TotalCredit 是本次活动期内累计拿到的积分。
	TotalCredit float64 `json:"total_credit"`
	// CheckinDates 是已签到的日期，倒序（最新在前）。
	CheckinDates []string `json:"checkin_dates"`
	// WeekProgress 是本周一到周日的签到情况。
	WeekProgress []bool `json:"week_progress"`

	// 展示用：活动名、主题、起止时间。
	ActivityName string `json:"activity_name"`
	ThemeName    string `json:"theme_name"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
}

// Status 查一次签到状态。
//
// 只读，可随时调用。判断「要不要签到」用它，不要用 daily-checkin 的返回——
// 后者会真的签到。
func (c *Client) Status(ctx context.Context) (Status, error) {
	body, err := c.post(ctx, wbapi.CheckinStatusPath)
	if err != nil {
		return Status{}, err
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Active         bool     `json:"active"`
			TodayCheckedIn bool     `json:"today_checked_in"`
			StreakDays     int      `json:"streak_days"`
			DailyCredit    float64  `json:"daily_credit"`
			TotalCredits   float64  `json:"total_credits"`
			CheckinDates   []string `json:"checkin_dates"`
			WeekProgress   []bool   `json:"week_progress"`
			ActivityName   string   `json:"activity_name"`
			ThemeName      string   `json:"theme_name"`
			StartTime      string   `json:"start_time"`
			EndTime        string   `json:"end_time"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Status{}, fmt.Errorf("签到状态响应不是合法 JSON：%w", err)
	}
	if payload.Code != 0 {
		return Status{}, fmt.Errorf("签到状态接口被拒绝：%s", payload.Msg)
	}
	data := payload.Data
	return Status{
		Active:         data.Active,
		CheckedInToday: data.TodayCheckedIn,
		StreakDays:     data.StreakDays,
		DailyCredit:    data.DailyCredit,
		TotalCredit:    data.TotalCredits,
		CheckinDates:   data.CheckinDates,
		WeekProgress:   data.WeekProgress,
		ActivityName:   data.ActivityName,
		ThemeName:      data.ThemeName,
		StartTime:      data.StartTime,
		EndTime:        data.EndTime,
	}, nil
}

// Result 是一次签到的结果。
//
// **「今天已经签过了」是成功，不是失败。** 后端对重复签到返回 400 + 10001，
// 这里把它归一到 Done=true/Already=true 且 err=nil——调用方因此不需要
// 先查状态再决定签不签，无脑调 Do 就行。
type Result struct {
	// Done 表示「今天确认签到了」：本次签的，或本来就已经签过。
	Done bool `json:"done"`
	// Already 表示在本次调用之前就已经签到过。
	Already bool `json:"already"`
	// Credit 是这次拿到的积分（已经签过时为 0）。
	Credit float64 `json:"credit"`
	// StreakDays 是签到后的连续天数。
	StreakDays int `json:"streak_days"`
	// Code 是后端原始返回码。
	Code int `json:"code"`
	// Message 是给用户看的一句话。
	Message string `json:"message"`
}

// codeAlreadyCheckedIn 是「今天已签到」的后端返回码。
//
// 它伴随的是 HTTP 400，所以不能靠状态码判断——400 在这里是正常业务分支。
const codeAlreadyCheckedIn = 10001

// Do 执行一次签到。
//
// 幂等：今天已签到时返回 Done=true/Already=true 且 err=nil。
// 只有「确实没能确认签到」时才返回 error。
func (c *Client) Do(ctx context.Context) (Result, error) {
	body, status, err := c.postRaw(ctx, wbapi.CheckinPath)
	if err != nil {
		return Result{}, err
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Credit     float64 `json:"credit"`
			StreakDays int     `json:"streak_days"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, fmt.Errorf("签到响应不是合法 JSON：%w", err)
	}
	if payload.Code == codeAlreadyCheckedIn {
		return Result{Done: true, Already: true, Code: payload.Code, Message: "今天已经签过了"}, nil
	}
	if payload.Code != 0 {
		message := payload.Msg
		if message == "" {
			message = fmt.Sprintf("签到被拒绝（HTTP %d，code %d）", status, payload.Code)
		}
		return Result{Code: payload.Code, Message: message}, fmt.Errorf("%s", message)
	}
	return Result{
		Done:       true,
		Credit:     payload.Data.Credit,
		StreakDays: payload.Data.StreakDays,
		Code:       payload.Code,
		Message:    "签到成功",
	}, nil
}

// post 发一个 POST 并校验业务码，返回 data 原文。
func (c *Client) post(ctx context.Context, path string) ([]byte, error) {
	body, _, err := c.postRaw(ctx, path)
	return body, err
}

// postRaw 发一个 POST，返回响应体与 HTTP 状态码。
//
// 非 2xx 不直接报错：签到的「今天已签到」正是 400 + 10001，
// 交回调用方按业务码判断。只有网络失败与读不出响应体才算错误。
func (c *Client) postRaw(ctx context.Context, path string) ([]byte, int, error) {
	headers, err := c.tokens.Headers()
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, 0, fmt.Errorf("构造请求失败：%w", err)
	}
	for name, value := range headers {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-client-platform", "web")
	req.Header.Set("accept", "application/json, text/plain, */*")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("请求失败：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取响应失败：%w", err)
	}
	return body, resp.StatusCode, nil
}
