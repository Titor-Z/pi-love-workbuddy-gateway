package panel

import (
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/journal"
)

// defaultUsageDays 是默认统计窗口。
const defaultUsageDays = 14

// usageDay 是「按天」的一个数据点，直接对应柱状图的一根柱子。
type usageDay struct {
	Date     string             `json:"date"`      // 形如 2026-10-14
	Requests int                `json:"requests"`  // 当天请求数
	Credit   float64            `json:"credit"`    // 当天消耗
	Blocked  int                `json:"blocked"`   // 当天确定被内容审核拦下的次数（结构信号或策略码）
	Refused  int                `json:"refused"`   // 当天疑似礼貌拒答的次数（文本启发式）
	In       int                `json:"in"`        // 当天输入 token
	Out      int                `json:"out"`       // 当天输出 token
	Cached   int                `json:"cached"`    // 其中命中缓存的输入 token
	ByModel  map[string]float64 `json:"by_model"`  // 按模型拆分的消耗，用于堆叠
	ByReason map[string]int     `json:"by_reason"` // 按确定拦截原因拆分的次数
}

// usageModel 是「按模型」的汇总，对应饼图或排行条。
type usageModel struct {
	Model    string  `json:"model"`
	Requests int     `json:"requests"`
	Credit   float64 `json:"credit"`
	In       int     `json:"in"`
	Out      int     `json:"out"`
	Cached   int     `json:"cached"`
	Blocked  int     `json:"blocked"`
	Refused  int     `json:"refused"`
}

// usageTotals 是窗口内的合计。
type usageTotals struct {
	Requests  int     `json:"requests"`
	Credit    float64 `json:"credit"`
	Blocked   int     `json:"blocked"`    // 确定被拦（含 11128 策略码与 content_filter/refusal）
	BlockRate float64 `json:"block_rate"` // 确定被拦数 ÷ 请求数
	Refused   int     `json:"refused"`    // 疑似拒答（启发式，不计入拦截率）
	// RateLimited 是窗口内被频率限制挡下的次数。
	RateLimited int `json:"rate_limited"`
	// ActiveLimits 是当前仍可能生效的限流（按模型去重，取最近一条）。
	// 这是概览页「还要等多久」的数据源；没有则为空。
	ActiveLimits []activeLimit `json:"active_limits,omitempty"`

	In     int `json:"in"`
	Out    int `json:"out"`
	Cached int `json:"cached"`
	// CacheRate 是命中缓存的输入占全部输入的比例。
	// 它回答「为什么这次的 credit 比上次高」——同样的对话，命中率掉下来就贵。
	CacheRate float64 `json:"cache_rate"`
}

// activeLimit 是一个模型当前可能仍生效的限流。
type activeLimit struct {
	Model string `json:"model"`
	// ResetAt 是恢复时刻的 RFC3339（本地时区），前端直接比较大小即可；
	// 解析不出就留空，前端宁可少显示也不要显示一个错的时间。
	ResetAt string `json:"reset_at"`
	Since   string `json:"since"` // 最近一次撞墙的时间，便于判断新旧
}

// resetAtOffset 匹配后端给出的重置时间："2026-10-14 23:13:09 UTC+8" 这类
// 「本地时间 + 时区缩写 + 数字偏移」的写法。
//
// 为什么必须自己拆：time.Parse 的 MST 只认时区缩写本身（"UTC"、"CST"），
// 不认识紧跟其后的 "+8"——按 "2006-01-02 15:04:05 MST" 解析真实数据会得到
// "extra text: \"+8\""。这一个解析失败就是概览页那张限流卡片一直显示
// 「有模型正在频率限制中」的原因：时间判不了新旧，限流便永远「仍然生效」。
// 偏移量宁可按本机时区解读（后端固定给 UTC+8，而网关跑在国内），也不猜。
var resetAtOffset = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\s+[A-Za-z]{1,5}\s*[+-]\d{1,2}(?::\d{2})?$`)

// parseResetAt 解析后端给出的重置时间；解析不出返回零值。
func parseResetAt(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	if matched := resetAtOffset.FindStringSubmatch(raw); matched != nil {
		if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", matched[1], time.Local); err == nil {
			return parsed, true
		}
	}
	// 兜底：标准库自己能吃下的几种形状（RFC3339、"... UTC"、"... CST"）。
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse("2006-01-02 15:04:05 MST", raw); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}

// usageResponse 是用量页的数据。
type usageResponse struct {
	From   time.Time    `json:"from"`
	To     time.Time    `json:"to"`
	Days   []usageDay   `json:"days"`
	Models []usageModel `json:"models"`
	Totals usageTotals  `json:"totals"`
}

// handleUsage 把事件文件聚合成看板需要的样子。
//
// 数据源是 journal 而不是账单接口：journal 是实时的，账单有分钟级延迟；
// 两者的 credit 口径一致（见 AGENTS 的计费对账记录），所以这里累加出来的数
// 会在几分钟后与余额的变化对上。
func (p *Panel) handleUsage(w http.ResponseWriter, r *http.Request) {
	days := defaultUsageDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			days = min(parsed, 365)
		}
	}

	// 从「days-1 天前的零点」开始，这样窗口是整天的，
	// 图表上不会出现一根只有半天的柱子。
	now := time.Now()
	from := startOfDay(now).AddDate(0, 0, -(days - 1))

	records, err := p.deps.Journal.Read(from)
	if err != nil {
		p.fail(w, "读取事件", err)
		return
	}
	writeJSON(w, http.StatusOK, aggregate(records, from, now, days))
}

// aggregate 把事件聚合成按天与按模型两个维度。
//
// 只有对话事件计入统计：模型目录拉取、令牌续期这些不是用户发起的用量，
// 混进来会让「请求数」这个数字失去意义。
func aggregate(records []journal.Record, from, to time.Time, days int) usageResponse {
	response := usageResponse{
		From: from,
		To:   to,
		Days: make([]usageDay, 0, days),
	}

	// 先按天铺好槽位，缺数据的那天也要出现在图上，否则横轴会跳。
	index := map[string]int{}
	for offset := 0; offset < days; offset++ {
		date := from.AddDate(0, 0, offset).Format("2006-01-02")
		index[date] = len(response.Days)
		response.Days = append(response.Days, usageDay{
			Date:     date,
			ByModel:  map[string]float64{},
			ByReason: map[string]int{},
		})
	}

	byModel := map[string]*usageModel{}
	for _, record := range records {
		if record.Kind != journal.KindChat {
			continue
		}
		date := startOfDay(record.Time).Format("2006-01-02")
		slot, ok := index[date]
		if !ok {
			// 落在窗口之外（读取时的边界），忽略。
			continue
		}

		model := record.Model
		if model == "" {
			model = "未知"
		}

		day := &response.Days[slot]
		day.Requests++
		day.Credit += record.Credit
		day.In += record.In
		day.Out += record.Out
		day.Cached += record.Cached
		day.ByModel[model] += record.Credit
		if record.Blocked {
			day.Blocked++
			reason := record.BlockedReason
			if reason == "" {
				reason = "other"
			}
			day.ByReason[reason]++
		}
		if record.Refused {
			day.Refused++
		}

		entry, ok := byModel[model]
		if !ok {
			entry = &usageModel{Model: model}
			byModel[model] = entry
		}
		entry.Requests++
		entry.Credit += record.Credit
		entry.In += record.In
		entry.Out += record.Out
		entry.Cached += record.Cached
		if record.Blocked {
			entry.Blocked++
		}
		if record.Refused {
			entry.Refused++
		}

		response.Totals.Requests++
		response.Totals.Credit += record.Credit
		response.Totals.In += record.In
		response.Totals.Out += record.Out
		response.Totals.Cached += record.Cached
		if record.Blocked {
			response.Totals.Blocked++
		}
		if record.Refused {
			response.Totals.Refused++
		}
		if record.RateLimited {
			response.Totals.RateLimited++
		}
	}

	response.Totals.ActiveLimits = collectActiveLimits(records, time.Now())

	response.Models = make([]usageModel, 0, len(byModel))
	for _, entry := range byModel {
		entry.Credit = roundCredit(entry.Credit)
		response.Models = append(response.Models, *entry)
	}
	// 按消耗排序：面板上的排行条要稳定的顺序，map 遍历顺序不能直接用。
	sort.Slice(response.Models, func(i, j int) bool {
		if response.Models[i].Credit != response.Models[j].Credit {
			return response.Models[i].Credit > response.Models[j].Credit
		}
		return response.Models[i].Model < response.Models[j].Model
	})

	for i := range response.Days {
		response.Days[i].Credit = roundCredit(response.Days[i].Credit)
		for model, value := range response.Days[i].ByModel {
			response.Days[i].ByModel[model] = roundCredit(value)
		}
	}
	response.Totals.Credit = roundCredit(response.Totals.Credit)
	if response.Totals.Requests > 0 {
		response.Totals.BlockRate = round4(
			float64(response.Totals.Blocked) / float64(response.Totals.Requests))
	}
	if response.Totals.In > 0 {
		response.Totals.CacheRate = round4(
			float64(response.Totals.Cached) / float64(response.Totals.In))
	}
	return response
}

// collectActiveLimits 从事件里挑出「可能仍然生效」的限流，按模型去重取最近一条。
//
// 判定依据是后端给出的重置时间：解析不出时间的事件一律不纳入——
// 宁可显示为空，也不要告诉用户一个错的恢复时间。
// 这是概览页「还要等多久」的唯一数据源。
func collectActiveLimits(records []journal.Record, now time.Time) []activeLimit {
	latest := map[string]journal.Record{}
	for _, record := range records {
		if !record.RateLimited || record.Model == "" {
			continue
		}
		if existing, ok := latest[record.Model]; !ok || record.Time.After(existing.Time) {
			latest[record.Model] = record
		}
	}
	if len(latest) == 0 {
		return nil
	}

	limits := make([]activeLimit, 0, len(latest))
	for model, record := range latest {
		resetTime, ok := parseResetAt(record.RateResetAt)
		if !ok {
			continue // 解析不出恢复时间，宁可少显示也不要显示错的
		}
		if resetTime.Before(now) {
			continue // 已经过了恢复时间，不算「仍然生效」
		}
		limits = append(limits, activeLimit{
			Model:   model,
			ResetAt: resetTime.Format(time.RFC3339),
			Since:   record.Time.Format("15:04"),
		})
	}
	if len(limits) == 0 {
		return nil
	}
	sort.Slice(limits, func(i, j int) bool { return limits[i].Model < limits[j].Model })
	return limits
}

// startOfDay 返回某一天的零点（本地时区）。
func startOfDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

// roundCredit 抹掉浮点累加产生的尾数。credit 本身只到小数点后两三位，
// 但一次一次加下来会出现 1.3400000000000003 这种东西。
func roundCredit(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}

// round4 把比率保留四位小数，够用且好读。
func round4(value float64) float64 {
	return math.Round(value*1e4) / 1e4
}
