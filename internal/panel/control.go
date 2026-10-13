// Package panel 提供本机的 Web 控制台：状态、余额、用量、模型、设置，
// 以及停止与重启网关。
//
// 面板与网关在同一个进程里，所以「停止」就是回完响应后优雅退出，
// 「重启」就是退出前把新的自己派生出来。不需要 pid 文件，也不需要守护进程。
package panel

// Action 是面板请求的进程控制动作。
type Action int

const (
	// ActionStop 表示停止网关。
	ActionStop Action = iota + 1
	// ActionRestart 表示重启网关。
	ActionRestart
)

// Control 把面板收到的停止/重启请求传给主循环。
//
// 为什么用通道而不是直接调用：HTTP 处理函数里不能同步关闭服务器——
// Shutdown 会等待所有在途连接结束，包括正在返回的这个响应本身，会死锁。
// 所以处理器只发出意图，由主循环在响应返回之后再动手。
type Control struct {
	actions chan Action
}

// NewControl 构造一个控制器。
func NewControl() *Control {
	// 容量 1 足够：同时来多个停止请求没有意义，后来的直接丢掉。
	return &Control{actions: make(chan Action, 1)}
}

// Stop 请求停止网关。
func (c *Control) Stop() { c.request(ActionStop) }

// Restart 请求重启网关。
func (c *Control) Restart() { c.request(ActionRestart) }

// Actions 返回主循环用来等待动作的通道。
func (c *Control) Actions() <-chan Action { return c.actions }

// request 投递一个动作；已有待处理动作时直接丢弃。
func (c *Control) request(action Action) {
	select {
	case c.actions <- action:
	default:
	}
}
