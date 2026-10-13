//go:build windows

package daemon

import "syscall"

// Windows 下没有会话概念，改用进程创建标志：
//   - DETACHED_PROCESS：不继承控制台，关掉终端窗口不会影响它；
//   - CREATE_NEW_PROCESS_GROUP：新进程组，不受父进程的 Ctrl+C 波及。
const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// detachAttr 让子进程脱离控制台。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
}
