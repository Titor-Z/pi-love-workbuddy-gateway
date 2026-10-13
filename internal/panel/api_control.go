package panel

import (
	"net/http"
)

// handleStop 请求停止网关。
//
// 这里只发出意图，不真的关闭：Shutdown 会等待所有在途连接结束，
// 而我们正处在其中一个连接里，同步关会死锁。
// 主循环会在响应返回之后动手。
func (p *Panel) handleStop(w http.ResponseWriter, _ *http.Request) {
	p.deps.Logger.Printf("面板请求停止网关")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "stopping",
		"message": "网关正在停止，这个页面很快就无法访问了",
	})
	p.deps.Control.Stop()
}

// handleRestart 请求重启网关。做法是退出前把新的自己派生出来，
// 所以 PID 会变（不像 exec 那样保持不变），但配置与事件文件都是接着用的。
func (p *Panel) handleRestart(w http.ResponseWriter, _ *http.Request) {
	p.deps.Logger.Printf("面板请求重启网关")
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "restarting",
		"message": "网关正在重启，稍等几秒后刷新本页",
	})
	p.deps.Control.Restart()
}
