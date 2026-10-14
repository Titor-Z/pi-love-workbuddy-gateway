package gateway

import (
	"context"
	"net/http"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/upstream"
)

// Backend 是网关真正取数据的地方。
//
// 两个实现：
//   - 网关模式：*upstream.Client，直连 WorkBuddy；
//   - 客户端模式：*relay.Client，把请求转发给局域网里的网关。
//
// 抽成接口而不是让 Server 直接持有 *upstream.Client，是为了让「这些请求
// 从哪里来」对 Server 透明——客户端模式下凭证不在本机，Server 必须能在
// 完全不知道这件事的情况下照常工作。
type Backend interface {
	// ChatStream 发起对话补全，返回尚未读取的原始响应。
	// 由调用方决定原样透传还是聚合成非流式响应，因此这里不读 body。
	ChatStream(ctx context.Context, body []byte) (*http.Response, error)

	// Models 拉取实时模型目录。
	Models(ctx context.Context) ([]upstream.ModelInfo, error)
}
