// Package main 是 pi-love-workbuddy-gateway 的入口包。
//
// 本程序把腾讯 WorkBuddy（国内版）的订阅额度，转换成一个本地 OpenAI 兼容网关，
// 供 pi 通过 ~/.pi/agent/models.json 里的自定义 provider 接入。
//
// 三条设计约束（详见 AGENTS.md）：
//   - 只做国内版，后端固定 https://copilot.tencent.com
//   - 只做 pi 需要的协议：GET /v1/models 与 POST /v1/chat/completions
//   - 零第三方依赖，只用 Go 标准库，产出单文件二进制
package main
