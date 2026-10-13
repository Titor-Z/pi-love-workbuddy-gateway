// Package logging 提供同时写标准输出与可选文件的简单日志。
// 抽成独立包是为了让网关、凭据、后端各层共用同一份输出策略。
package logging

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Logger 是并发安全的日志器。未指定文件时只写标准输出。
type Logger struct {
	mu   sync.Mutex
	sink io.Writer
	file *os.File
}

// New 构造日志器。path 为空时只写标准输出。
func New(path string) (*Logger, error) {
	logger := &Logger{sink: os.Stdout}
	if path == "" {
		return logger, nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开日志文件失败：%w", err)
	}
	logger.file = file
	logger.sink = io.MultiWriter(os.Stdout, file)
	return logger, nil
}

// Printf 写一行带时间戳的日志。
func (l *Logger) Printf(format string, args ...any) {
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"),
		fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	// 日志失败不应影响主流程，因此这里刻意忽略写入错误。
	_, _ = io.WriteString(l.sink, line)
}

// Close 关闭日志文件。
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
