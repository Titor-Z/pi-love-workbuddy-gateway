package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestIsChild 靠环境变量识别「自己是不是后台子进程」，
// 这是防止无限自我重启的唯一开关。
func TestIsChild(t *testing.T) {
	t.Setenv(EnvMarker, "")
	if IsChild() {
		t.Error("未设置标记时不该认为自己是子进程")
	}

	t.Setenv(EnvMarker, "1")
	if !IsChild() {
		t.Error("设置了标记时应当认为自己是子进程")
	}

	// 只认精确的 "1"，避免被其它值误触发。
	t.Setenv(EnvMarker, "true")
	if IsChild() {
		t.Error("标记值不是 1 时不该认为自己是子进程")
	}
}

// TestOpenSinkWithoutLogPath 不指定日志文件时退到系统空设备，
// 而不是继承当前终端——否则关掉终端仍会把子进程带走。
func TestOpenSinkWithoutLogPath(t *testing.T) {
	sink, err := openSink("")
	if err != nil {
		t.Fatalf("打开空设备失败：%v", err)
	}
	defer sink.Close()
	if sink.Name() != os.DevNull {
		t.Errorf("名字 = %q，期望 %q", sink.Name(), os.DevNull)
	}
}

// TestOpenSinkCreatesLogFile 指定了日志文件时要能自动建目录并追加写入。
func TestOpenSinkCreatesLogFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "gateway.log")

	first, err := openSink(path)
	if err != nil {
		t.Fatalf("打开日志失败：%v", err)
	}
	first.Close()

	second, err := openSink(path)
	if err != nil {
		t.Fatalf("再次打开日志失败：%v", err)
	}
	defer second.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("日志文件没建出来：%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
	// 日志可能含请求细节，权限不该放开给同机其他用户。
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Errorf("权限 = %o，期望 600", permission)
	}
}

// TestOpenSinkRejectsBadPath 目录建不出来时要报错，而不是静默丢弃日志。
func TestOpenSinkRejectsBadPath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备测试介质失败：%v", err)
	}
	if _, err := openSink(filepath.Join(blocker, "gateway.log")); err == nil {
		t.Fatal("父路径不是目录时应当报错")
	}
}

// TestSpawnRunsDetached 真正派生一个进程，确认 PID 有效且进程活着。
// 用系统自带的长睡进程，避免拉起测试二进制本身。
func TestSpawnRunsDetached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上没有 sleep 这类通用命令，跳过")
	}

	pid, err := spawn("/bin/sleep", []string{"30"}, filepath.Join(t.TempDir(), "out.log"))
	if err != nil {
		t.Fatalf("派生进程失败：%v", err)
	}
	if pid <= 0 {
		t.Fatalf("PID = %d，应当为正数", pid)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("找不到刚派生的进程：%v", err)
	}
	// 信号 0 只做存在性检查，不真的发信号。
	if err := process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("派生的进程应当还活着：%v", err)
	}
	t.Cleanup(func() { _ = process.Kill() })
}

// TestSpawnReportsImmediateExit 子进程立刻退出时要把失败报出来。
// 否则用户只会看到一句「已转入后台」，然后对着一个没起来的服务发呆。
func TestSpawnReportsImmediateExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上没有 true 这类通用命令，跳过")
	}
	_, err := spawn("/usr/bin/true", nil, filepath.Join(t.TempDir(), "out.log"))
	if err == nil {
		t.Fatal("子进程立刻退出时应当报错")
	}
	if !strings.Contains(err.Error(), "立即退出") {
		t.Errorf("错误信息应说明是立即退出：%v", err)
	}
}

// TestSpawnUsesMarkerEnv 派生的进程必须带上标记环境变量，
// 否则它会再派生一次，无限循环。
func TestSpawnUsesMarkerEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上没有 sh，跳过")
	}
	out := filepath.Join(t.TempDir(), "env.log")
	// 子进程把标记写进日志文件——它同时也是子进程的标准输出。
	// 写完再睡一会，否则进程立刻退出会被当成启动失败。
	pid, err := spawn("/bin/sh",
		[]string{"-c", "echo marker=${" + EnvMarker + "}; sleep 10"}, out)
	if err != nil {
		t.Fatalf("派生进程失败：%v", err)
	}
	t.Cleanup(func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	})

	// 给它一点时间把内容刷进去。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(out); err == nil && strings.Contains(string(raw), "marker=1") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	raw, _ := os.ReadFile(out)
	t.Errorf("子进程没拿到标记环境变量，实际内容：%q", string(raw))
}
