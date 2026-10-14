// Package paircode 生成并校验一次性配对码。
//
// 为什么需要它：真正的接入密钥是 43 位 base64（256 位），跨机器手抄不现实，
// 而它是整套方案唯一的访问边界，不能为了好打就缩短——一把能长期使用的短密钥
// 等于把账号交给任何愿意穷举的局域网邻居。
//
// 解法是把「配对」和「持有密钥」分成两步：
//
//	网关控制台显示一个 6 位短码（5 分钟内有效、用过即废、错 10 次作废）
//	客户端填这个短码 → 网关校验通过后**把长密钥交给它**（走 TLS）
//	此后客户端一直用长密钥，短码不再参与
//
// 短码只在几分钟的窗口里、在 TLS 通道内使用，穷举窗口极小；长密钥仍然是
// 256 位。用户要抄的只有 6 位。
//
// 码表去掉了 0 O 1 I L 这几个最容易看错、念错的字符，剩下 31 个符号。
// 6 位 = 约 29.6 位熵。
package paircode

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

const (
	// TTL 是配对码的有效期。够用户从一台机器走到另一台机器，又不给穷举留时间。
	TTL = 5 * time.Minute
	// length 是配对码的字符数。
	length = 6
	// alphabet 是可选字符表，去掉了 0 O 1 I L。
	alphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	// maxAttempts 是同一个码允许的失败次数，超过就作废。
	// 作废是**安全**的选择：宁可让用户重新生成一个，也不让一个码被反复试。
	maxAttempts = 10
	// maxGlobalAttempts 是一分钟内全局允许的失败次数。
	// 它防的不是猜中（29.6 位熵在 5 分钟里猜不中），而是有人拿它当免费的
	// CPU 消耗品刷。超了直接 429，连比较都不做。
	maxGlobalAttempts = 60
)

// Code 是当前有效的配对码。同一时刻只有一个。
//
// 不做持久化：配对码是短命的、一次性的东西，重启后应该失效而不是复活。
type Code struct {
	mu            sync.Mutex
	value         string
	expiresAt     time.Time
	attempts      int
	globalFails   int
	globalResetAt time.Time
}

// New 返回一个尚未生成码的 store。
func New() *Code { return &Code{} }

// Issue 生成一个新码，并作废此前那个。返回码本身与到期时间。
func (c *Code) Issue() (string, time.Time, error) {
	value, err := randomCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(TTL)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = value
	c.expiresAt = expiresAt
	c.attempts = 0
	return value, expiresAt, nil
}

// Current 返回当前仍然有效的码；没有或已过期时返回空串。
func (c *Code) Current() (string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.liveLocked(time.Now()) {
		return "", time.Time{}
	}
	return c.value, c.expiresAt
}

// Redeem 用一次尝试去兑换配对码。成功即作废（一次性）。
//
// 比较用常数时间：短路比较会把「前几位对了」这件事通过耗时泄漏出去，
// 而那正是穷举最需要的反馈。
func (c *Code) Redeem(input string) bool {
	normalized := Normalize(input)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.globalResetAt.Before(now) {
		c.globalFails = 0
		c.globalResetAt = now.Add(time.Minute)
	}
	if c.globalFails >= maxGlobalAttempts {
		return false
	}
	if !c.liveLocked(now) {
		c.globalFails++
		return false
	}

	if subtle.ConstantTimeCompare([]byte(normalized), []byte(c.value)) != 1 {
		c.attempts++
		c.globalFails++
		if c.attempts >= maxAttempts {
			// 错太多次就作废，逼用户重新生成。
			c.value = ""
			c.expiresAt = time.Time{}
		}
		return false
	}

	c.value = ""
	c.expiresAt = time.Time{}
	c.attempts = 0
	return true
}

// Throttled 表示全局失败次数已经用满，调用方应当直接返回 429。
func (c *Code) Throttled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.globalResetAt.Before(time.Now()) {
		return false
	}
	return c.globalFails >= maxGlobalAttempts
}

// liveLocked 判断当前码是否可用。调用方必须已持锁。
func (c *Code) liveLocked(now time.Time) bool {
	return c.value != "" && now.Before(c.expiresAt)
}

// Normalize 把用户输入规整成比较用的形式：去掉分隔符、空格，统一大写。
//
// 用户可能照着 `4F7-K92` 连分隔符一起抄，也可能抄成小写，都不该算错。
func Normalize(raw string) string {
	var builder strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		switch r {
		case ' ', '\t', '-', '_', '.', ':':
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// randomCode 生成一个长度为 length 的码。
func randomCode() (string, error) {
	var builder strings.Builder
	builder.Grow(length)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < length; i++ {
		index, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("生成配对码失败：%w", err)
		}
		builder.WriteByte(alphabet[index.Int64()])
	}
	return builder.String(), nil
}

// Format 把码显示成 `XXX-XXX`，念出来/抄下来都不容易串行。
func Format(value string) string {
	if len(value) != length {
		return value
	}
	return value[:3] + "-" + value[3:]
}
