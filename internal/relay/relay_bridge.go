package relay

import (
	"context"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
)

// MarkRelay 标记本次请求来自 relay 端口。
//
// 单独包一层不是为了省几个字符，是为了让 relay 的意图读起来是本地的：
// 「这条请求来自 relay 端口、设备是 X」，而不是两个孤立的上下文操作。
func MarkRelay(ctx context.Context) context.Context { return identity.MarkRelay(ctx) }

// WithDevice 把设备 ID 放进上下文。
func WithDevice(ctx context.Context, id string) context.Context {
	return identity.WithDevice(ctx, id)
}

// FromRelay 取出上下文里的 relay 标记。
func FromRelay(ctx context.Context) bool { return identity.FromRelay(ctx) }

// DeviceFrom 取出上下文里的设备 ID。
func DeviceFrom(ctx context.Context) string { return identity.DeviceFrom(ctx) }

// PSKMatches 比较客户端送来的密钥与网关持有的密钥。
// 走 identity 的常量时间比较：字符串 == 会按字节短路，
// 长度差与首个不同字节的位置都是可以测出来的信息。
func PSKMatches(expected, actual string) bool {
	return identity.ConstantTimeEqual(expected, actual)
}
