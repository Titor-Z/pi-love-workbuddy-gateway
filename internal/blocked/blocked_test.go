package blocked

import "testing"

func TestFromStructure(t *testing.T) {
	cases := []struct {
		finish  string
		refusal string
		blocked bool
		reason  Reason
	}{
		{"content_filter", "", true, ReasonContentFilter},
		{"", "我无法提供该内容", true, ReasonRefusal},
		{"stop", "", false, ReasonNone},
		{"tool_calls", "", false, ReasonNone},
		{"stop", "", false, ReasonNone},
	}
	for i, c := range cases {
		got := FromStructure(c.finish, c.refusal)
		if got.Blocked != c.blocked || got.Reason != c.reason {
			t.Errorf("用例 %d: 得到 blocked=%v reason=%q，期望 %v/%q",
				i, got.Blocked, got.Reason, c.blocked, c.reason)
		}
	}
}

func TestFromPolicy(t *testing.T) {
	if v := FromPolicy(11128, "安全策略拦截"); !v.Blocked || v.Reason != ReasonPolicy {
		t.Errorf("11128 应确定拦截，得到 %+v", v)
	}
	// 非策略码（普通错误）不应算拦截。
	if v := FromPolicy(40001, "模型不存在"); v.Blocked {
		t.Errorf("普通错误码不应计入拦截，得到 %+v", v)
	}
	if v := FromPolicy(0, ""); v.Blocked {
		t.Errorf("缺 code 不应计入拦截，得到 %+v", v)
	}
}

func TestMerge(t *testing.T) {
	// 仅文本拒答命中：Blocked 为假，Refused 为真。
	got := Merge(Verdict{}, Verdict{}, true, Reason("抱歉，我不能"))
	if got.Blocked || !got.Refused {
		t.Errorf("仅拒答猜测时应为 blocked=false/refused=true，得到 %+v", got)
	}
	// 结构 + 拒答同时命中：两栏都记。
	got = Merge(FromStructure("content_filter", ""), Verdict{}, true, Reason("抱歉，我不能"))
	if !got.Blocked || !got.Refused || got.Reason != ReasonContentFilter {
		t.Errorf("结构+拒答应都记，得到 %+v", got)
	}
	// 策略码优先于空结构。
	got = Merge(Verdict{}, FromPolicy(11128, ""), false, Reason(""))
	if !got.Blocked || got.Reason != ReasonPolicy {
		t.Errorf("策略码应确定拦截，得到 %+v", got)
	}
}

func TestClassifyText(t *testing.T) {
	cases := []struct {
		text    string
		refused bool
	}{
		{"抱歉，我不能提供这个内容", true},
		{"作为 AI，我无法保证", true},
		{"我无法协助你完成该操作", true},
		{"今天天气不错，可以出去走走", false},
		{"我的能力有限，但试试看", true},
		{"", false},
	}
	for i, c := range cases {
		refused, rule := ClassifyText(c.text)
		if refused != c.refused {
			t.Errorf("用例 %d: %q 得到 refused=%v rule=%q，期望 %v", i, c.text, refused, rule, c.refused)
		}
	}
}

func TestClassifyTextIsHeuristic(t *testing.T) {
	// 误报边界：正常回答里出现「我无法处理」这类说法也应被标疑似——
	// 这正是「拒答猜测」与「确定拦截」分开存放的理由。
	if refused, _ := ClassifyText("步骤太多了我无法处理，请分步提问"); !refused {
		t.Error("带「我无法处理」的正常回答也应被标为疑似拒答")
	}
}
