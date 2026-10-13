// Package billing 读取 WorkBuddy 账号的资源包（余额）。
//
// 与对话接口不同，账单接口在另一个域名下（codebuddy.cn 而非 copilot），
// 且返回的是腾讯云的标准资源包结构：外层 Response.Data.Accounts。
//
// **一个必须记住的坑**：Accounts 里的整数 CapacityRemain 是资源包的**总容量**，
// 不随消耗变化；真正反映消耗的是带 Precise 后缀的字符串字段，
// 它们有 8 位小数精度。只读整数会永远看到一个不变的数。
//
// 另一个坑：账单结算有 1～3 分钟延迟，且按批到账。
// 刚发完请求就查余额，很可能看到「没扣」。这不是 bug。
package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credential"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/wbapi"
)

// requestTimeout 是账单接口的超时。它是个元信息接口，不该拖住面板。
const requestTimeout = 15 * time.Second

// Client 是账单客户端。
type Client struct {
	http    *http.Client
	cred    *credential.Manager
	baseURL string // 根地址，留空表示用官方地址；测试可注入
}

// NewClient 构造账单客户端。baseURL 留空时指向官方账单域名。
func NewClient(cred *credential.Manager, baseURL string) *Client {
	if baseURL == "" {
		baseURL = wbapi.BillingBaseURL
	}
	return &Client{
		http:    &http.Client{Timeout: requestTimeout},
		cred:    cred,
		baseURL: baseURL,
	}
}

// Package 是一个资源包（余额项）。
type Package struct {
	Name string // 包名，如「CodeBuddy个人体验版」

	// Remain 是本轮周期的剩余量，Use 是本轮已用量，Size 是本轮总量。
	// 三者都取自 Precise 字段，保留小数精度。
	Remain float64
	Use    float64
	Size   float64

	// TotalRemain 是资源包的剩余总量（不随周期重置）。
	TotalRemain float64
	// CycleEndTime 是本轮周期的结束时间；到点后 Use / Remain 会重置。
	CycleEndTime time.Time
	// ExpireTime 是资源包本身的失效时间；到点后整包作废。
	ExpireTime time.Time
}

// Snapshot 是一次账单读取的结果。
type Snapshot struct {
	Packages []Package
	// FetchedAt 是本次读取的时刻。面板要用它提示「数据可能滞后」。
	FetchedAt time.Time
}

// TotalRemain 汇总所有资源包的剩余量。
func (s Snapshot) TotalRemain() float64 {
	total := 0.0
	for _, item := range s.Packages {
		total += item.Remain
	}
	return total
}

// Fetch 读取当前账号的资源包。注意结算延迟：刚发完的请求可能还没计入。
func (c *Client) Fetch(ctx context.Context) (Snapshot, error) {
	headers, err := c.cred.Headers()
	if err != nil {
		return Snapshot{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+wbapi.BillingResourcePath, bytes.NewReader([]byte("{}")))
	if err != nil {
		return Snapshot{}, fmt.Errorf("构造账单请求失败：%w", err)
	}
	for name, value := range headers {
		if value != "" {
			req.Header.Set(name, value)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("账单请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("账单接口返回 HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Response struct {
				Data struct {
					Accounts []rawAccount `json:"Accounts"`
				} `json:"Data"`
			} `json:"Response"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Snapshot{}, fmt.Errorf("账单响应不是合法 JSON：%w", err)
	}
	if payload.Code != 0 {
		return Snapshot{}, fmt.Errorf("账单接口被拒绝：%s", payload.Msg)
	}

	// 容错：官方的读取端允许 Accounts 出现在 data.Response.Data 或 data.Data，
	// 这里只认实测到的那一种，但空结果要报错而不是返回一个「余额为 0」的假象。
	accounts := payload.Data.Response.Data.Accounts
	if len(accounts) == 0 {
		return Snapshot{}, fmt.Errorf("账单响应里没有资源包，无法判断余额")
	}

	snapshot := Snapshot{Packages: make([]Package, 0, len(accounts)), FetchedAt: time.Now()}
	for _, item := range accounts {
		snapshot.Packages = append(snapshot.Packages, item.toPackage())
	}
	return snapshot, nil
}

// rawAccount 是账单接口的原始行结构，字段名沿用腾讯云的大驼峰。
type rawAccount struct {
	PackageName string `json:"PackageName"`

	// 整数口径：总量，不随消耗变化。仅作兜底。
	CapacityRemain float64 `json:"CapacityRemain"`
	CapacityUsed   float64 `json:"CapacityUsed"`
	CapacitySize   float64 `json:"CapacitySize"`

	// 精确口径：带 8 位小数，才是真正会动的数字。
	// 后端把它们编码成字符串，不能直接声明成 float64。
	CycleCapacityRemainPrecise string `json:"CycleCapacityRemainPrecise"`
	CycleCapacityUsedPrecise   string `json:"CycleCapacityUsedPrecise"`
	CycleCapacitySizePrecise   string `json:"CycleCapacitySizePrecise"`
	CapacityRemainPrecise      string `json:"CapacityRemainPrecise"`

	CycleEndTime     string `json:"CycleEndTime"`     // 形如 "2026-10-31 23:59:59"
	DeductionEndTime int64  `json:"DeductionEndTime"` // 毫秒时间戳
}

// toPackage 把原始行转换成本项目的资源包结构。
func (a rawAccount) toPackage() Package {
	item := Package{
		Name:        a.PackageName,
		Remain:      parsePrecise(a.CycleCapacityRemainPrecise, a.CapacityRemain),
		Use:         parsePrecise(a.CycleCapacityUsedPrecise, a.CapacityUsed),
		Size:        parsePrecise(a.CycleCapacitySizePrecise, a.CapacityRemain+a.CapacityUsed),
		TotalRemain: parsePrecise(a.CapacityRemainPrecise, a.CapacityRemain),
	}
	item.CycleEndTime = parseCycleEnd(a.CycleEndTime)
	item.ExpireTime = parseMillis(a.DeductionEndTime)
	return item
}

// parsePrecise 优先取精确字段；它缺失或解析失败时退回整数口径。
// 退回而不是报错：整数至少还能量级正确，比直接丢掉这一项好。
func parsePrecise(precise string, fallback float64) float64 {
	if precise == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(precise, 64)
	if err != nil {
		return fallback
	}
	return value
}

// parseCycleEnd 解析周期结束时间。它不带时区，按本地时区理解。
func parseCycleEnd(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", raw, time.Local)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// parseMillis 把毫秒时间戳转成时间。0 表示没有这个时间。
func parseMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	// 有些字段会给到一个远超 int64 纳秒范围的值，截断保护一下。
	if value > math.MaxInt64/int64(time.Millisecond) {
		return time.Time{}
	}
	return time.UnixMilli(value)
}
