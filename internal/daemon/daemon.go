// Package daemon 提供「脱离终端在后台运行」的支持。
//
// 做法是把自己重新拉起一个进程，并让它脱离当前会话：
// 这样关掉终端、断开 SSH 都不会把网关一起带走。
// 子进程里带上标记环境变量，用来防止无限自我重启。
package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// EnvMarker 是标记「本进程已是后台子进程」的环境变量名。
const EnvMarker = "PILOVEWORKBUDDY_DAEMON"

// startupGrace 是拉起子进程后的观察窗口：若它在这段时间内就退出，
// 说明启动失败（最常见的是端口被占用），要把失败报给用户，
// 而不是让父进程假装一切正常地退出。
const startupGrace = 700 * time.Millisecond

// IsChild 判断当前进程是不是由 Start 拉起的后台子进程。
func IsChild() bool { return os.Getenv(EnvMarker) == "1" }

// Start 以脱离终端的方式重新拉起自己，返回子进程 PID。
//
// args 原样传给子进程（包含 -d 也无妨：子进程会通过 IsChild 认出自己，
// 不会再递归派生）。logPath 非空时子进程的 stdout/stderr 都指向它，
// 这样连崩溃信息也留得下来；为空则丢弃。
func Start(args []string, logPath string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("拿不到自身可执行文件路径：%w", err)
	}
	return spawn(executable, args, logPath)
}

// spawn 是实现主体，单独拆出来便于用别的可执行文件做测试。
func spawn(executable string, args []string, logPath string) (int, error) {
	sink, err := openSink(logPath)
	if err != nil {
		return 0, err
	}

	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(), EnvMarker+"=1")
	cmd.Stdin = nil
	cmd.Stdout = sink
	cmd.Stderr = sink
	cmd.SysProcAttr = detachAttr()

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("启动后台进程失败：%w", err)
	}
	pid := cmd.Process.Pid

	// 观察一小段时间：子进程若立刻退出，多半是端口被占用或配置有误。
	// 这里把失败如实报出来，比让用户对着一句「已在后台运行」白等要好。
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case waitErr := <-exited:
		return 0, fmt.Errorf(
			"后台进程启动后立即退出（%v）。常见原因是端口已被占用；详情见日志：%s",
			waitErr, describeSink(logPath))
	case <-time.After(startupGrace):
	}
	// 父进程马上就会退出，子进程由 init 接管；这里只需把等待协程放掉。
	go func() { <-exited }()

	return pid, nil
}

// openSink 打开子进程的标准输出/错误去向。
// 不指定日志文件时退到系统空设备，而不是继承当前终端——否则它仍会跟着终端一起死。
func openSink(logPath string) (*os.File, error) {
	if logPath == "" {
		return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	// 目录必须在这里建：后台模式是先派生再启动日志器，
	// 父进程根本不会走到创建目录那一步，首次 -d 运行会直接失败。
	if dir := filepath.Dir(logPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建日志目录失败：%w", err)
		}
	}
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开日志文件失败：%w", err)
	}
	return file, nil
}

// describeSink 给出用户该去哪里看输出。
func describeSink(logPath string) string {
	if logPath == "" {
		return "（未指定 -log，输出已丢弃）"
	}
	return logPath
}
