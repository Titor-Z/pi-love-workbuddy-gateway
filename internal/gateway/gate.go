package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// queueTimeout 是在闸门外面能等多久。
//
// 超时要由我们自己说了算，不能让请求排到后端去被随机 429 拒绝：
// 排队至少是有序的、可预期的，而「谁先撞上限流」取决于网络时序。
const queueTimeout = 60 * time.Second

// retryAfterSeconds 是闸门满时建议客户端等待的时间。
const retryAfterSeconds = 5

// Gate 限制同时打到后端去的请求数。
//
// 它存在的理由是多设备共享一个 WorkBuddy 账号：N 台机器同时发请求，
// 后端看到的并发是原来的 N 倍，而它本来就有限流（实测 HTTP 429）。
// 与其让后端随机拒绝，不如在这里排队——排队的结果是可预期的。
//
// 为什么它在 Server 这一层而不是 Backend 那一层：客户端模式的 Backend 就是
// relay 客户端，如果闸门跟着 Backend 走，一次请求会被客户端和网关各限一次，
// 表现出来是莫名其妙的超时与 503。
type Gate struct {
	sem chan struct{}

	mu       sync.Mutex
	waiting  int
	rejected int
	passed   int
}

// NewGate 构造闸门。limit <= 0 表示不限——不限时 Acquire 不阻塞，
// 也不该为此保留一个一百万容量的信号量。
func NewGate(limit int) *Gate {
	if limit <= 0 {
		return &Gate{sem: nil}
	}
	return &Gate{sem: make(chan struct{}, limit)}
}

// unlimited 判断是否未启用闸门。
func (g *Gate) unlimited() bool { return g.sem == nil }

// Acquire 占一个名额。返回的 release 必须被调用一次，且只能一次。
//
// 闸门满时不会立刻失败——等一会儿很可能就有空位了，而且后端可能正在处理
// 别人一个很长的流式响应。等超过 queueTimeout 才放弃。
func (g *Gate) Acquire(ctx context.Context) (func(), error) {
	if g.unlimited() {
		return noopRelease, nil
	}

	g.mu.Lock()
	g.waiting++
	g.mu.Unlock()

	waitCtx, cancel := context.WithTimeout(ctx, queueTimeout)
	defer cancel()

	select {
	case g.sem <- struct{}{}:
		g.mu.Lock()
		g.waiting--
		g.passed++
		g.mu.Unlock()
		return g.once(), nil
	case <-waitCtx.Done():
		g.mu.Lock()
		g.waiting--
		g.rejected++
		g.mu.Unlock()
		return noopRelease, fmt.Errorf("网关并发已满，等待 %.0f 秒仍未轮到", queueTimeout.Seconds())
	}
}

// once 返回一个幂等的释放函数：延迟执行时可能被调用两次，
// 第二次必须什么也不做，否则信号量会被凭空放回一个名额。
func (g *Gate) once() func() {
	var release sync.Once
	return func() {
		release.Do(func() {
			<-g.sem
			g.mu.Lock()
			g.passed--
			g.mu.Unlock()
		})
	}
}

// Snapshot 是闸门的当前状态，给面板看。
type Snapshot struct {
	Limit    int `json:"limit"`    // 上限，0 表示不限
	Active   int `json:"active"`   // 正在后端处理的数量
	Waiting  int `json:"waiting"`  // 正在排队的数量
	Rejected int `json:"rejected"` // 累计被闸门拒掉的数量
}

// snapshot 取下当前状态。
func (g *Gate) snapshot() Snapshot {
	if g.unlimited() {
		return Snapshot{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return Snapshot{
		Limit:    cap(g.sem),
		Waiting:  g.waiting,
		Rejected: g.rejected,
		Active:   g.passed,
	}
}

func noopRelease() {}

// writeBusy 是闸门满且等超时时的响应。
func writeBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))
	writeError(w, http.StatusServiceUnavailable, "rate_limit_error",
		fmt.Sprintf("网关并发已满，请 %d 秒后重试（多设备共用一个账号时更容易触发）", retryAfterSeconds))
}
