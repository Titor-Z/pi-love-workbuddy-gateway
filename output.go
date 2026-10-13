package main

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/billing"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/catalog"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/config"
)

// writeTable 打印给人看的模型清单。
func writeTable(out io.Writer, entries []catalog.Entry) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\t名称（含费用标记）\t上下文\t最大输出\t思考\t图片")
	for _, entry := range entries {
		fmt.Fprintf(writer, "%s\t%s\t%d\t%d\t%s\t%s\n",
			entry.Model.ID, entry.DisplayName(),
			entry.Model.MaxInputTokens, entry.Model.MaxOutputTokens,
			yesNo(entry.Model.SupportsThinking), yesNo(entry.HasImageInput()))
	}
	return writer.Flush()
}

// writeBilling 打印资源包（余额）清单。
//
// 特意显示本轮已用而不只是剩余：整数剩余量在账单接口里是包的总量、不随消耗变化，
// 只看剩余会让人以为额度一直没少。
func writeBilling(out io.Writer, snapshot billing.Snapshot) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "资源包\t本轮剩余\t本轮总量\t本轮已用\t周期结束\t到期")
	for _, item := range snapshot.Packages {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			item.Name,
			formatCredits(item.Remain), formatCredits(item.Size), formatCredits(item.Use),
			formatTime(item.CycleEndTime), formatTime(item.ExpireTime))
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n合计剩余：%s\n", formatCredits(snapshot.TotalRemain()))
	// 这条提示很重要：结算有延迟，刚发完请求就来查很可能看到「没扣」。
	fmt.Fprintf(out, "读取于 %s（账单结算有 1～3 分钟延迟，刚发完的请求可能尚未计入）\n",
		snapshot.FetchedAt.Format("2006-01-02 15:04:05"))
	return nil
}

// formatCredits 去掉没有意义的小数位，避免打出 497.46000001 这种机械精度。
func formatCredits(value float64) string {
	return strconv.FormatFloat(math.Round(value*100)/100, 'f', -1, 64)
}

// formatTime 渲染时间；零值显示为短横线而不是 0001-01-01。
func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format("2006-01-02")
}

// yesNo 把布尔值渲染成中文标记，仅用于表格输出。
func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "-"
}

// piGatewayBaseURL 由监听地址推出 pi 该填的 baseUrl。
func piGatewayBaseURL(cfg *config.Config) string {
	addr := cfg.ListenAddr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr + "/v1"
}
