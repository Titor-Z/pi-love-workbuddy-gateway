package config

import "fmt"

// errUnknownArgument 是「多给了参数」的报错。
//
// 必须报错而不是忽略：静默忽略会让人以为生效了，
// 然后花半小时排查为什么没变化。
func errUnknownArgument(arg string) error {
	return fmt.Errorf("无法识别的参数：%s\n\n%s", arg, Usage())
}

// errClientNeedsAddress 是客户端模式缺网关地址。
//
// 没有它客户端不知道该把请求交给谁，启动后每个请求都会失败，
// 不如当场说清楚。
func errClientNeedsAddress() error {
	return fmt.Errorf("客户端模式必须指定网关地址：\n\n" +
		"  pi-love-workbuddy-gateway client -addr 192.168.1.5:8788\n\n" +
		"地址和端口从网关那台机器的启动日志或控制台里抄。")
}

// Usage 返回给用户看的用法。
func Usage() string {
	return "pi-love-workbuddy-gateway —— 把 WorkBuddy 的额度接给 pi\n" +
		"\n" +
		"用法：\n" +
		"  pi-love-workbuddy-gateway [gateway] -d -addr 0.0.0.0:8788\n" +
		"      网关模式（默认）。这台机器持有 WorkBuddy 凭证，\n" +
		"      其他电脑连它来用同一个账号。\n" +
		"\n" +
		"  pi-love-workbuddy-gateway client  -d -addr 192.168.1.5:8788\n" +
		"      客户端模式。这台机器不持有凭证，把请求转发给网关。\n" +
		"\n" +
		"  pi-love-workbuddy-gateway package --export [目标目录]\n" +
		"      把随二进制一起交付的 pi 扩展导出去（默认写进本机 pi 的扩展目录）。\n" +
		"      每次升级网关后重跑一次，插件就跟着更新。\n" +
		"\n" +
		"  pi-love-workbuddy-gateway checkin [--status]\n" +
		"      给这个账号签到一次，结果打一行 JSON。不需要网关在跑，\n" +
		"      外部定时任务直接 exec 它就行。\n" +
		"\n" +
		"参数：\n" +
		"  -addr   网关模式：对外服务地址，默认 0.0.0.0:" + fmt.Sprint(defaultRelayPort) + "\n" +
		"          客户端模式：要连的网关地址\n" +
		"  -d      后台运行（默认开）；在终端里看日志用 -d=false\n" +
		"\n" +
		"端口：\n" +
		"  " + localAddr() + "   本机 pi 连的（明文 HTTP），**所有机器都一样**\n" +
		"  <局域网IP>:" + fmt.Sprint(defaultRelayPort) + "   网关对外服务的（TLS + 密钥），客户端连它\n" +
		"  " + ConsoleAddr() + "   控制台，只绑本机\n" +
		"\n" +
		"pi 的配置里 baseUrl 填 http://" + localAddr() + "/v1 ，两种模式都不用改。\n" +
		"首次配对在控制台「多设备」页填密钥、比对指纹，不走命令行。\n" +
		"模型目录建议用扩展自动获取，见上面的 package --export。\n"
}
