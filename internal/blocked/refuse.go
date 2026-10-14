package blocked

import "strings"

// RefusalPhrases 是「模型礼貌拒答」的启发式短语表。命中其中任意一条，且当前
// 响应又没有被结构信号/策略码确定为拦截时，标记为「疑似拒答」(Refused)。
//
// 重要边界：这是③号通道（文本猜测），永远是「疑似」，不是「确定被拦」。
// 它和 Blocked 分开放：正常回答里也可能出现「我不能保证…」这类说法，所以
// 这一栏只用于「提示用户可能没拿到真东西」，绝不能当拦截率统计进去。
//
// 维护方式：想识别更多拒答说法，往这张表追加短语即可（短、常见、低误报优先）。
// 不要为了「抓全」而放太泛的敏感词——泛词会把正常回答也误判成拒答，
// 那正是 v0.2.2 之前被删掉的误报来源，这里明确避免重蹈。
var RefusalPhrases = []string{
	"抱歉，我不能",
	"抱歉，我无法",
	"对不起，我不能",
	"对不起，我无法",
	"我无法提供",
	"我不能提供",
	"我无法协助",
	"我不能协助",
	"我无法帮助",
	"我不能帮助",
	"我不能参与",
	"我无法参与",
	"我无法生成",
	"我不能生成",
	"我无法创作",
	"我不能创作",
	"我无法提供相关内容",
	"我无法回答",
	"我不能回答",
	"这个问题超出了",
	"作为人工智能",
	"作为 AI",
	"我无法给出",
	"我不能给出",
	"我无法执行",
	"我不能执行",
	"我的能力有限",
	"我无法处理",
	"我不能处理",
	"我无法评论",
	"我不能评论",
	"我无法对此进行",
	"我无法帮助您进行",
}

// ClassifyText 判断一段（累积的助手）文本是否疑似礼貌拒答。
// 返回是否触发、以及命中的规则短语，方便日志与面板标注「按哪条规则猜的」。
//
// 不区分大小写；短语表里既有中文也有「作为 AI」这种，直接子串匹配即可。
// 命中多条时返回第一条（顺序即优先级，表越靠前越具体）。
func ClassifyText(text string) (refused bool, rule Reason) {
	if text == "" {
		return false, RefuseRuleNone
	}
	lower := strings.ToLower(text)
	for _, phrase := range RefusalPhrases {
		if phrase == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return true, Reason(phrase)
		}
	}
	return false, RefuseRuleNone
}
