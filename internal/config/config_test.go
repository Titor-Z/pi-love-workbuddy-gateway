package config

import (
	"bytes"
	"strings"
	"testing"
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
	if cfg.APIKey != "" || cfg.AuthFile != "" || cfg.AtRestSecret != "" ||
		cfg.LogPath != "" || cfg.Promotions != "" {
		t.Errorf("默认配置不该有非空的可选字段：%+v", cfg)
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
