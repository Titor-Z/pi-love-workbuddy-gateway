package blocked

import "strings"

// RateLimitCodes 是「非 200 + JSON 错误」里表示用量/频率限制的 code 集合。
//
// 实测确认：6004 = 「使用量已超出频率限制，将在 <时间> 重置，您也可以切换其他
// 模型继续使用」。这是**滚动窗口**式的限流（实测约 3 小时），按模型生效、
// 可自愈，与内容审核无关。
//
// 为什么单独一张表、不并进 PolicyCodes：
//   - PolicyCodes 表示「内容被拦」，进拦截率统计；
//   - 本表表示「太频繁被挡」，是排队问题不是内容问题，绝不能算拦截。
//
// 两者混在一起会让「拦截率」变成一个说不清的数字。
//
// 维护方式：发现新的限流 code 往这里加一行即可。
var RateLimitCodes = map[int]bool{
	6004: true, // 用量/频率限制（实测确认）
}

// RateLimit 是一条限流判定的结果。
type RateLimit struct {
	Limited bool   // 是否是限流
	ResetAt string // 后端给出的重置时间原文（形如 "2026-10-14 23:13:09 UTC+8"）；未给出时为空
}

// DetectRateLimit 判定一个上游错误码是否为频率限制，并尽量取出重置时间。
//
// 只认 code：不靠扫 msg 猜，避免把普通错误的文案误判成限流。
// 重置时间从 msg 里按固定格式提取，取不到就留空——宁可没有，也不要给一个错的时间。
func DetectRateLimit(code int, msg string) RateLimit {
	if !RateLimitCodes[code] {
		return RateLimit{}
	}
	return RateLimit{Limited: true, ResetAt: extractResetTime(msg)}
}

// extractResetTime 从限流文案里提取重置时间。
// 后端格式固定为「将在 <时间> 重置」，取「将在」与「重置」之间的部分。
// 格式若变（后端改文案），就取不到而返回空——宁可没有，也不要给一个错的时间。
func extractResetTime(msg string) string {
	start := strings.Index(msg, "将在")
	if start < 0 {
		return ""
	}
	rest := msg[start+len("将在"):]
	end := strings.Index(rest, "重置")
	if end < 0 {
		return ""
	}
	candidate := strings.TrimSpace(rest[:end])
	// 只接受看起来像日期时间的值（够长且含数字），
	// 避免把「将在后续版本支持」这类文案误当成时间。
	if len(candidate) >= 16 && strings.ContainsAny(candidate, "0123456789") {
		return candidate
	}
	return ""
}
