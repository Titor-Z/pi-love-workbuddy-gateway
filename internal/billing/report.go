package billing

import "time"

// Report 是余额的对外表示。
//
// 单独一份而不是直接把 Snapshot 序列化出去：Snapshot 的字段名是给 Go 看的，
// 而这份要同时给三个消费者用——网关控制台、经 relay 拉到它的客户端控制台、
// 以及将来别的客户端。放在这里是为了让「网关算好的东西」与「客户端拿到的
// 东西」是同一个类型，不至于两边各映射一次、慢慢长歪。
type Report struct {
	FetchedAt   time.Time     `json:"fetched_at"`
	TotalRemain float64       `json:"total_remain"`
	Packages    []PackageView `json:"packages"`
	// SettleHint 是给前端的提示文案。放在服务端算，免得同样的句子在前端
	// 再写一遍，两处会不一致。
	SettleHint string `json:"settle_hint"`
	// Source 说明这份数字是从哪读的："local"（本机直连账单接口）
	// 或 "gateway"（客户端模式下由网关那台机器读出后透传过来）。
	// 前端要如实告诉用户，否则他会以为是自己这台机器读的。
	Source string `json:"source"`
}

// PackageView 是一个资源包的对外表示。
type PackageView struct {
	Name        string     `json:"name"`
	Remain      float64    `json:"remain"`
	Size        float64    `json:"size"`
	Use         float64    `json:"use"`
	TotalRemain float64    `json:"total_remain"`
	CycleEnd    *time.Time `json:"cycle_end,omitempty"`
	Expire      *time.Time `json:"expire,omitempty"`
}

// settleHint 是余额延迟的提示。账单接口实测有 1～3 分钟延迟且按批到账，
// 不说清楚的话用户会以为扣费没生效。
const settleHint = "账单结算有 1～3 分钟延迟，刚发完的请求可能尚未计入"

// BuildReport 把一次读取结果转成对外表示。
func BuildReport(snapshot Snapshot) Report {
	report := Report{
		FetchedAt:   snapshot.FetchedAt,
		TotalRemain: snapshot.TotalRemain(),
		Packages:    make([]PackageView, 0, len(snapshot.Packages)),
		SettleHint:  settleHint,
		Source:      "local",
	}
	for _, item := range snapshot.Packages {
		view := PackageView{
			Name:        item.Name,
			Remain:      item.Remain,
			Size:        item.Size,
			Use:         item.Use,
			TotalRemain: item.TotalRemain,
		}
		if !item.CycleEndTime.IsZero() {
			cycleEnd := item.CycleEndTime
			view.CycleEnd = &cycleEnd
		}
		if !item.ExpireTime.IsZero() {
			expire := item.ExpireTime
			view.Expire = &expire
		}
		report.Packages = append(report.Packages, view)
	}
	return report
}
