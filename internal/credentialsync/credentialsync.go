// Package credentialsync 实现凭证冷同步：网关把账号凭证单向推给客户端，
// 客户端在外出时切到 gateway 模式即可独立工作。
//
// 三条地基（不可动摇）：
//
//  1. **单向**：网关 → 客户端。客户端绝不刷新、绝不回写。
//     一旦客户端能刷新，多设备共享就退化成互相踢——
//     那正是 P0 用整个架构消除的问题。
//  2. **epoch 单调递增**：网关每成功同步一次 +1，客户端拒收更小的。
//  3. **不自动覆盖**：冲突时告警并交给用户，不静默合并。
//
// 关于「为什么同步的是明文而不是加密信封」：
// atRestSecretKey 绑定在网关那台机器上，客户端机器没有它，
// 信封搬不过来。所以同步明文，客户端落地时用**自己的**密钥重新封装。
// 由此推出最重要的一条：客户端切 gateway 后走 credential.Manager 读本地文件，
// 与「装过一次 WorkBuddy 的机器」完全同一条代码路径，
// **不新增刷新逻辑**——刷新是项目里最脆弱、测试最多的一段，不该在这里被复制。
package credentialsync

import (
	"errors"
	"fmt"
)

// Envelope 是网关下发、客户端回传的那一份凭证。
//
// Credential 是**明文**登录态文档的 JSON（结构与 WorkBuddy 的 *.info 一致，
// 含 account 等未识别字段，照搬不裁剪）。它在 TLS 通道里传输，
// 客户端落地时立即用本机密钥重新加密——不在磁盘上留明文。
type Envelope struct {
	// Epoch 是网关侧的同步计数器，每次成功同步 +1。
	Epoch int64 `json:"epoch"`
	// GatewayRefreshedAt 是网关上次刷新令牌的时间（RFC3339Nano）。
	// 客户端发现它比自己上次拉取的时间新，就立刻再拉一次——
	// 这是「刷新后立刻推」的替代实现，不需要长连接。
	GatewayRefreshedAt string `json:"gateway_refreshed_at,omitempty"`
	// Credential 是明文登录态文档。
	Credential map[string]any `json:"credential"`
}

// Decision 是客户端收到一份凭证后的处置结论。
type Decision int

const (
	// Accept 表示接受覆盖本地。
	Accept Decision = iota
	// AcceptIdempotent 表示 epoch 相同，接受（重复同步无副作用）。
	AcceptIdempotent
	// Conflict 表示本地比网关新，拒绝覆盖并交由用户决定。
	Conflict
	// RejectStale 表示来的一份比本地旧，直接丢弃。
	RejectStale
)

// String 让日志与面板能直接读这个结论。
func (d Decision) String() string {
	switch d {
	case Accept:
		return "接受覆盖"
	case AcceptIdempotent:
		return "重复同步"
	case Conflict:
		return "本地更新，需人工决定"
	case RejectStale:
		return "丢弃过期的副本"
	}
	return "未知"
}

// Decide 判定该不该接受网关发来的凭证。
//
// localEpoch 为 -1 表示本地还没有凭证（首次同步）。
// 分三种结果，每一种都有明确理由：
//
//	localEpoch < remoteEpoch  → Accept        网关更新，覆盖
//	localEpoch == remoteEpoch → AcceptIdempotent 幂等，重复同步无害
//	localEpoch > remoteEpoch  → Conflict      本地更可能新（切过 gateway 自己刷过），
//	                                      拒绝覆盖，让用户选
//
// 为什么本地更大时**不**自动采用本地、也不自动让网关赢：自动合并永远有边界情况
// 会静默丢东西；总是网关赢会丢掉外出期间的成功刷新。交给人最多点一下，
// 但任何时候都不会悄悄丢凭证。
func Decide(localEpoch, remoteEpoch int64) Decision {
	switch {
	case localEpoch < remoteEpoch:
		return Accept
	case localEpoch == remoteEpoch:
		return AcceptIdempotent
	default:
		return Conflict
	}
}

// Validate 检查一份下发凭证是否可用。
//
// 只查结构，不查内容有效性（令牌是否真能用在请求里由 credential 包负责）。
func (e Envelope) Validate() error {
	if e.Epoch <= 0 {
		return fmt.Errorf("epoch 必须为正，当前 %d", e.Epoch)
	}
	if len(e.Credential) == 0 {
		return errors.New("凭证文档为空")
	}
	auth, ok := e.Credential["auth"].(map[string]any)
	if !ok {
		return errors.New("凭证缺少 auth 段")
	}
	if text(auth, "accessToken") == "" {
		return errors.New("凭证缺少访问令牌")
	}
	return nil
}

// text 取一个字符串字段；缺失或类型不对都算空。
func text(obj map[string]any, key string) string {
	value, _ := obj[key].(string)
	return value
}
