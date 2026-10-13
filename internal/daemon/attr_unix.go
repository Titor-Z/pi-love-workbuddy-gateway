//go:build !windows

package daemon

import "syscall"

// detachAttr 让子进程脱离当前会话，不再持有控制终端。
// 没有这一步，关掉终端时会收到 SIGHUP，网关会被一起带走。
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
