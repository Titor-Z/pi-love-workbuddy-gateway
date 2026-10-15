package piplugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExportWritesEmbeddedFiles 导出要把内嵌的文件真的写到目标目录。
func TestExportWritesEmbeddedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pi-workbuddy")
	result, err := Export(dir)
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	if !result.Created {
		t.Error("新目录应当报告为新建")
	}
	if result.Replaced {
		t.Error("新目录不该报告为覆盖")
	}
	for _, name := range exported {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s 没写出来：%v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s 是空的", name)
		}
	}
	// 内容必须与内嵌的一致，不能是个占位文件。
	written, err := os.ReadFile(filepath.Join(dir, "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := Files.ReadFile("index.ts")
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(embedded) {
		t.Error("写出来的内容与内嵌的不一致")
	}
	// 插件必须声明 provider 与命令，否则装进去也是白装。
	if !strings.Contains(string(written), "registerProvider") ||
		!strings.Contains(string(written), "workbuddy") {
		t.Error("扩展内容里没有注册 provider，导出的多半不是它")
	}
}

// TestExportOverwrites 重复导出是更新：这是「与主程序同步交付」的核心。
func TestExportOverwrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := Export(dir); err != nil {
		t.Fatalf("首次导出失败：%v", err)
	}
	// 手动改坏它，模拟旧版本。
	stale := filepath.Join(dir, "index.ts")
	if err := os.WriteFile(stale, []byte("STALE-MARKER-DO-NOT-KEEP"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Export(dir)
	if err != nil {
		t.Fatalf("再次导出失败：%v", err)
	}
	if !result.Replaced {
		t.Error("应当报告为覆盖")
	}
	content, _ := os.ReadFile(stale)
	if strings.Contains(string(content), "STALE-MARKER-DO-NOT-KEEP") {
		t.Error("旧内容没有被覆盖掉——那用户会一直跑着老插件")
	}
}

// TestExportExpandsHome 目标写成 ~/... 时要展开，不能真去建一个叫 ~ 的目录。
func TestExportExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	result, err := Export("~/plwg-export-test")
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	if !strings.HasPrefix(result.Dir, home) {
		t.Fatalf("~ 没有展开：%s", result.Dir)
	}
	if _, err := os.Stat(filepath.Join(result.Dir, "index.ts")); err != nil {
		t.Fatalf("文件没写到展开后的目录：%v", err)
	}
}

// TestDefaultDirHonoursPiEnv 必须认 pi 自己的配置目录变量，
// 否则用户把 pi 装到别处时我们会写到另一个地方。
func TestDefaultDirHonoursPiEnv(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "/tmp/custom-agent")
	want := filepath.Join("/tmp/custom-agent", "extensions", DirName)
	if got := DefaultDir(); got != want {
		t.Errorf("DefaultDir = %q，期望 %q", got, want)
	}
}

// TestDefaultDirFallsBackToHome 没设变量时落到 ~/.pi/agent/extensions/<名字>。
func TestDefaultDirFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	want := filepath.Join(home, ".pi", "agent", "extensions", DirName)
	if got := DefaultDir(); got != want {
		t.Errorf("DefaultDir = %q，期望 %q", got, want)
	}
}

// TestExportReportsWriteFailure 写不进去时必须报错并带上路径。
//
// 「目标目录里有一份同名目录」是真实会遇到的：用户手工改过 pi 的扩展目录。
// 此时安静地跳过等于告诉他装好了，而实际什么都没装。
func TestExportReportsWriteFailure(t *testing.T) {
	dir := t.TempDir()
	// 把 index.ts 做成目录，写入必然失败。
	if err := os.Mkdir(filepath.Join(dir, "index.ts"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Export(dir)
	if err == nil {
		t.Fatal("写不进去时应当报错")
	}
	if !strings.Contains(err.Error(), "index.ts") {
		t.Errorf("错误信息没说是哪个文件：%v", err)
	}
}

// TestExportNeedsHome 主目录取不到又没给目录时，要说清「指定一个」而不是报别的错。
func TestExportNeedsHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("USERPROFILE", "") // Windows 上 os.UserHomeDir 看这个
	_, err := Export("")
	if err == nil {
		t.Fatal("没有主目录又没有目标目录时应当报错")
	}
	if !strings.Contains(err.Error(), "指定") {
		t.Errorf("错误信息没说该怎么办：%v", err)
	}
}
