package paircode

import (
	"strings"
	"testing"
	"time"
)

// TestIssueThenRedeem 正常路径：发一个码，用同一个码兑掉。
func TestIssueThenRedeem(t *testing.T) {
	store := New()
	value, expiresAt, err := store.Issue()
	if err != nil {
		t.Fatalf("Issue 失败：%v", err)
	}
	if len(value) != length {
		t.Fatalf("码长 = %d，期望 %d", len(value), length)
	}
	if !expiresAt.After(time.Now()) {
		t.Error("到期时间不在将来")
	}
	if !store.Redeem(value) {
		t.Fatal("正确的码没有通过")
	}
}

// TestRedeemIsSingleUse 用过即废。否则一个码泄漏一次就等于长期有效。
func TestRedeemIsSingleUse(t *testing.T) {
	store := New()
	value, _, _ := store.Issue()
	if !store.Redeem(value) {
		t.Fatal("第一次应当成功")
	}
	if store.Redeem(value) {
		t.Fatal("同一个码第二次仍然通过了——它不是一次性的")
	}
	if current, _ := store.Current(); current != "" {
		t.Error("兑换之后仍然留着一个有效的码")
	}
}

// TestRedeemRejectsWrongCode 错误的码不能通过。
func TestRedeemRejectsWrongCode(t *testing.T) {
	store := New()
	value, _, _ := store.Issue()
	wrong := "AAAAAA"
	if value == wrong {
		wrong = "BBBBBB"
	}
	if store.Redeem(wrong) {
		t.Fatal("错误的码通过了")
	}
	if !store.Redeem(value) {
		t.Error("错一次之后正确的码也不认了（应当还认）")
	}
}

// TestTooManyFailuresBurnsTheCode 连续错够次数就把码作废。
//
// 宁可让用户重新生成一个，也不让同一个码被反复试——
// 反复试正是穷举需要的条件。
func TestTooManyFailuresBurnsTheCode(t *testing.T) {
	store := New()
	value, _, _ := store.Issue()
	wrong := "AAAAAA"
	if value == wrong {
		wrong = "BBBBBB"
	}
	for i := 0; i < maxAttempts; i++ {
		store.Redeem(wrong)
	}
	if store.Redeem(value) {
		t.Fatal("错满次数后正确的码仍然可用——码没有被作废")
	}
	if current, _ := store.Current(); current != "" {
		t.Error("作废后 Current 仍然返回了码")
	}
}

// TestExpiredCodeIsRejected 过期的码不能用。
func TestExpiredCodeIsRejected(t *testing.T) {
	store := New()
	value, _, _ := store.Issue()
	// 直接把到期时间拨到过去，不 sleep。
	store.mu.Lock()
	store.expiresAt = time.Now().Add(-time.Second)
	store.mu.Unlock()

	if store.Redeem(value) {
		t.Fatal("过期的码通过了")
	}
	if current, _ := store.Current(); current != "" {
		t.Error("过期后 Current 仍然返回了码")
	}
}

// TestIssueReplacesPrevious 换一个码，旧码立即失效。
func TestIssueReplacesPrevious(t *testing.T) {
	store := New()
	first, _, _ := store.Issue()
	second, _, _ := store.Issue()
	if first == second {
		t.Skip("两次生成撞了同一个码（概率极低），跳过")
	}
	if store.Redeem(first) {
		t.Fatal("换码之后旧码仍然可用")
	}
	if !store.Redeem(second) {
		t.Error("新码不可用")
	}
}

// TestNormalizeAcceptsHumanInput 用户可能带分隔符、小写、空格抄过来。
func TestNormalizeAcceptsHumanInput(t *testing.T) {
	cases := map[string]string{
		"4F7-K92":  "4F7K92",
		" 4f7 k92": "4F7K92",
		"4F7_K92":  "4F7K92",
		"4f7k92":   "4F7K92",
	}
	for input, want := range cases {
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestRedeemAcceptsFormattedCode 控制台显示的是 `XXX-XXX`，用户照抄不该算错。
func TestRedeemAcceptsFormattedCode(t *testing.T) {
	store := New()
	value, _, _ := store.Issue()
	if !store.Redeem(Format(value)) {
		t.Errorf("带分隔符的 %q 没有通过", Format(value))
	}
}

// TestAlphabetAvoidsConfusableCharacters 码表里不能出现看错/念错的字符。
func TestAlphabetAvoidsConfusableCharacters(t *testing.T) {
	for _, bad := range []string{"0", "O", "1", "I", "L"} {
		if strings.Contains(alphabet, bad) {
			t.Errorf("码表里出现了容易混淆的 %q", bad)
		}
	}
}

// TestThrottleStopsBruteForce 全局失败次数用满后要能拦住。
func TestThrottleStopsBruteForce(t *testing.T) {
	store := New()
	value, _, _ := store.Issue()
	wrong := "AAAAAA"
	if value == wrong {
		wrong = "BBBBBB"
	}
	for i := 0; i < maxGlobalAttempts; i++ {
		store.Redeem(wrong)
	}
	if !store.Throttled() {
		t.Error("失败次数用满后仍然不设限")
	}
}
