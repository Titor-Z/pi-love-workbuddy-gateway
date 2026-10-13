package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/appdir"
)

// TestParseDefaults 不传参数时的默认值必须与 README 写的一致，
// 否则文档和实际行为会悄悄分叉。
func TestParseDefaults(t *testing.T) {
	cfg, err := Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8788" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.APIKey != "" || cfg.AuthFile != "" || cfg.AtRestSecret != "" || cfg.Promotions != "" {
		t.Errorf("默认配置不该有非空的可选字段：%+v", cfg)
	}
	// 日志默认写到配置目录，而不是只进标准输出；
	// 否则重启后历史就没了，拦截率之类的统计无从谈起。
	if cfg.LogPath != appdir.LogPath() {
		t.Errorf("LogPath = %q，期望默认路径 %q", cfg.LogPath, appdir.LogPath())
	}
	if cfg.PiOutput {
		t.Error("默认不该输出 pi 配置")
	}
}

// TestParseFlags 所有参数都要能被命令行覆盖。
func TestParseFlags(t *testing.T) {
	cfg, err := Parse([]string{
		"-addr", "127.0.0.1:9999",
		"-api-key", "secret",
		"-auth-file", "/tmp/a.info",
		"-at-rest-secret", "key",
		"-promotions", "/tmp/p.json",
		"-log", "/tmp/out.log",
		"-pi",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9999" || cfg.APIKey != "secret" ||
		cfg.AuthFile != "/tmp/a.info" || cfg.AtRestSecret != "key" ||
		cfg.Promotions != "/tmp/p.json" || cfg.LogPath != "/tmp/out.log" || !cfg.PiOutput {
		t.Errorf("参数没有全部生效：%+v", cfg)
	}
}

// TestParseRejectsUnknownFlag 写错的参数要报错，不能静默忽略。
func TestParseRejectsUnknownFlag(t *testing.T) {
	if _, err := Parse([]string{"-nope"}, &bytes.Buffer{}); err == nil {
		t.Fatal("未知参数应当报错")
	}
}

// TestParseRejectsEmptyAddr 监听地址为空要拦下，否则会起在一个随机端口上。
func TestParseRejectsEmptyAddr(t *testing.T) {
	_, err := Parse([]string{"-addr", ""}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "监听地址") {
		t.Fatalf("应当报监听地址不能为空，实际：%v", err)
	}
}

// TestParseAllowsSilencingLog 显式传空串表示不落盘，只写标准输出。
func TestParseAllowsSilencingLog(t *testing.T) {
	cfg, err := Parse([]string{"-log", ""}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.LogPath != "" {
		t.Errorf("LogPath = %q，期望空串", cfg.LogPath)
	}
}

// TestParseDaemonFlag 默认前台运行，-d 才转后台。
func TestParseDaemonFlag(t *testing.T) {
	cfg, err := Parse([]string{"-d"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !cfg.Daemon {
		t.Error("-d 应当开启后台模式")
	}

	cfg, err = Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if cfg.Daemon {
		t.Error("默认应当前台运行")
	}
}
