package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewWithoutPath 不指定文件时只写标准输出，Close 也要能安全调用。
func TestNewWithoutPath(t *testing.T) {
	logger, err := New("")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if logger.file != nil {
		t.Error("未指定路径时不该打开文件")
	}
	if err := logger.Close(); err != nil {
		t.Errorf("Close 报错：%v", err)
	}
	// 重复关闭要幂等，退出路径上可能被调用多次。
	if err := logger.Close(); err != nil {
		t.Errorf("重复 Close 报错：%v", err)
	}
	logger.Printf("关闭后写入也不该 panic")
}

// TestPrintfWritesToFile 日志要真的落到文件里，并带时间戳。
func TestPrintfWritesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "converter.log")
	logger, err := New(path)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	logger.Printf("第一条 %s", "消息")
	logger.Printf("第二条")
	if err := logger.Close(); err != nil {
		t.Fatalf("Close 报错：%v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败：%v", err)
	}
	content := string(raw)
	for _, want := range []string{"第一条 消息", "第二条"} {
		if !strings.Contains(content, want) {
			t.Errorf("日志缺少 %q：\n%s", want, content)
		}
	}
	if !strings.HasPrefix(content, "[") {
		t.Errorf("日志行应以时间戳开头：\n%s", content)
	}
}

// TestPrintfAppends 追加而不是覆盖，重启后历史日志不能丢。
func TestPrintfAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "converter.log")
	for _, line := range []string{"第一次运行", "第二次运行"} {
		logger, err := New(path)
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		logger.Printf("%s", line)
		logger.Close()
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败：%v", err)
	}
	if !strings.Contains(string(raw), "第一次运行") || !strings.Contains(string(raw), "第二次运行") {
		t.Errorf("日志被覆盖了：\n%s", string(raw))
	}
}

// TestNewRejectsBadPath 路径不可用时要在启动阶段就报错，
// 而不是等服务跑起来后每写一条日志都静默失败。
func TestNewRejectsBadPath(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "不存在的目录", "out.log")); err == nil {
		t.Fatal("目录不存在时应当报错")
	}
}
