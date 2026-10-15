package appdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPathsFollowXDG 钉住路径规则。
//
// macOS 上尤其重要：不能用 os.UserConfigDir()，它在 macOS 上返回
// ~/Library/Application Support，会让三平台路径不一致。
func TestPathsFollowXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test-root")
	wantDir := filepath.Join("/tmp/xdg-test-root", DirName)

	if got := Root(); got != "/tmp/xdg-test-root" {
		t.Errorf("Root = %q", got)
	}
	if got := Dir(); got != wantDir {
		t.Errorf("Dir = %q，期望 %q", got, wantDir)
	}
	if got, want := ModelOverridesPath(), filepath.Join(wantDir, ModelOverridesFileName); got != want {
		t.Errorf("ModelOverridesPath = %q，期望 %q", got, want)
	}
	if got, want := LogPath(), filepath.Join(wantDir, LogFileName); got != want {
		t.Errorf("LogPath = %q，期望 %q", got, want)
	}
}

// TestPathsFallBackToHome 未设 XDG_CONFIG_HOME 时落到 ~/.config。
func TestPathsFallBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("拿不到用户主目录，跳过")
	}
	want := filepath.Join(home, ".config", DirName)
	if got := Dir(); got != want {
		t.Errorf("Dir = %q，期望 %q", got, want)
	}
	// 三平台都不该出现 macOS 专有的 Application Support 路径。
	if strings.Contains(Dir(), "Application Support") {
		t.Errorf("路径落到了平台专有目录：%q", Dir())
	}
}

// TestEnsureDirCreatesDirectory 首次运行时目录还不存在，要能自动建出来。
func TestEnsureDirCreatesDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	// 先确认目标目录确实不存在。
	if _, err := os.Stat(filepath.Join(root, DirName)); err == nil {
		t.Fatal("测试前提不成立：目录已存在")
	}
	dir, err := EnsureDir()
	if err != nil {
		t.Fatalf("EnsureDir 失败：%v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("目录没建出来：%v", err)
	}
	// 重复调用要幂等，每次启动都会走一次。
	if _, err := EnsureDir(); err != nil {
		t.Errorf("重复 EnsureDir 失败：%v", err)
	}
}

// TestPathsEmptyWhenNoHome 拿不到主目录时返回空串，
// 让调用方回退到默认行为，而不是拼出一个相对路径。
func TestPathsEmptyWhenNoHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	// os.UserHomeDir 在多数环境会读 $HOME；拿不到时应当退化。
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		t.Skip("本环境下仍能拿到主目录，跳过")
	}
	if got := Dir(); got != "" {
		t.Errorf("Dir = %q，期望空串", got)
	}
	if got := LogPath(); got != "" {
		t.Errorf("LogPath = %q，期望空串", got)
	}
}
