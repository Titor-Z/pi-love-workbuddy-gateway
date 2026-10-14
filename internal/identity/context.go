package identity

import "context"

// contextKey 是上下文键的私有类型，避免与其它包的键冲突。
type contextKey int

const (
	// keyDevice 保存发起请求的设备 ID。
	keyDevice contextKey = iota
	// keyRelay 标记本次请求来自局域网 relay 端口。
	keyRelay
)

// WithDevice 把设备 ID 放进上下文。
// relay 中间件在鉴权之后调用它；网关随后同把它写进事件记录。
func WithDevice(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, keyDevice, id)
}

// DeviceFrom 取出上下文里的设备 ID；本机直连时为空串。
func DeviceFrom(ctx context.Context) string {
	value, _ := ctx.Value(keyDevice).(string)
	return value
}

// MarkRelay 标记本次请求来自 relay 端口。
//
// 网关据此跳过 -api-key 校验：那个密钥保护的是本机 API 端口，
// relay 有自己的一套准入（PSK + TLS 指纹 + 设备吊销），两套彼此独立。
// 不跳过的话，配了 -api-key 的用户会看到所有客户端都 401。
func MarkRelay(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyRelay, true)
}

// FromRelay 判断本次请求是否来自 relay 端口。
func FromRelay(ctx context.Context) bool {
	value, _ := ctx.Value(keyRelay).(bool)
	return value
}
