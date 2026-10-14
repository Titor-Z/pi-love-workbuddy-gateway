package blocked

// Reason 是拦截/拒答判定的细分原因，用于面板按原因分组统计，也写进日志。
type Reason string

const (
	// ReasonNone 表示没有任何拦截信号。
	ReasonNone Reason = ""
	// ReasonContentFilter 是流式通道里 finish_reason=content_filter。
	ReasonContentFilter Reason = "content_filter"
	// ReasonRefusal 是流式通道里非空 refusal。
	ReasonRefusal Reason = "refusal"
	// ReasonPolicy 是硬阻断通道里命中了策略码，具体码拼在后缀（如 policy:11128）。
	ReasonPolicy Reason = "policy"

	// RefuseRuleNone 表示没有触发拒答猜测。
	RefuseRuleNone Reason = ""
)

// Verdict 是一次响应里关于「有没有被拦」的完整结论。
//
// 两栏分开：Blocked 只看确定信号（结构信号或策略码），Refused 看文本猜测。
// 两者可以同时为真（既被硬拦、又探测到拒答文案），也可以只有一个。
// 但它们语义不同，面板和日志必须分开呈现，绝不能把 Refused 当 Blocked 用。
type Verdict struct {
	Blocked bool   // 是否确定被拦（来源 ① ②）
	Reason  Reason // 确定拦截的具体原因；Blocked 为假时恒为 ReasonNone

	Refused    bool   // 是否疑似礼貌拒答（来源 ③，启发式）
	RefuseRule Reason // 触发拒答猜测的规则（短语索引或 policy 名）；未触发时为空
}

// Empty 返回一个默认的「未被拦」结论，便于代码里先声明再按需填。
func Empty() Verdict { return Verdict{} }

// FromStructure 由流式通道的结构信号构造结论。
// 两种结构信号（content_filter / 非空 refusal）任意命中即确定被拦。
func FromStructure(finishReason string, refusal string) Verdict {
	reason := ReasonNone
	switch {
	case finishReason == "content_filter":
		reason = ReasonContentFilter
	case refusal != "":
		reason = ReasonRefusal
	}
	return Verdict{
		Blocked: reason != ReasonNone,
		Reason:  reason,
	}
}

// FromPolicy 由硬阻断通道的策略码构造结论。code 命中策略码表时确定被拦，
// 并记下具体码；未命中时返回「未被拦」（那只是普通上游错误，不是内容审核）。
func FromPolicy(code int, msg string) Verdict {
	if PolicyCodes[code] {
		return Verdict{
			Blocked: true,
			Reason:  ReasonPolicy,
		}
	}
	return Verdict{}
}

// Merge 把三个通道的结论合并成一条最终结论。
//
// 优先级：结构信号 / 策略码（确定的 Blocked）> 文本拒答猜测（Refused）。
// 两者独立成栏：即使结构信号已确定被拦，文本猜测仍可单独记录「拒答文案」；
// 若只有文本猜测命中、没有任何确定信号，则 Blocked=false、Refused=true。
// 这是把「确定被拦」与「疑似拒答」分开呈现的关键入口。
func Merge(structural, policy Verdict, refused bool, refuseRule Reason) Verdict {
	result := Verdict{}
	if structural.Blocked {
		result.Blocked = true
		result.Reason = structural.Reason
	} else if policy.Blocked {
		result.Blocked = true
		result.Reason = policy.Reason
	}
	result.Refused = refused
	result.RefuseRule = refuseRule
	return result
}
