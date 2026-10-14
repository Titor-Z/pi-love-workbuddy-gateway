package credentialsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ReadEpoch 读出本地 epoch。文件不存在表示还没同步过，返回 0 而不是错误——
// 首次启动必然走到这里。文件损坏则报错：默默当成 0 会让客户端
// 以为自己从没同步过，从而接受一份更旧的凭证。
func ReadEpoch(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读取 epoch 失败：%w", err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return 0, nil
	}
	epoch, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("epoch 文件已损坏（%s）：%w", path, err)
	}
	if epoch < 0 {
		return 0, fmt.Errorf("epoch 不能为负：%d", epoch)
	}
	return epoch, nil
}

// WriteEpoch 落盘 epoch。先写临时文件再改名，避免写一半留下坏文件；
// 权限 0600——它是「哪份凭证更新」的唯一依据。
func WriteEpoch(path string, epoch int64) error {
	if epoch < 0 {
		return fmt.Errorf("epoch 不能为负：%d", epoch)
	}
	return writePrivateFile(path, []byte(strconv.FormatInt(epoch, 10)+"\n"))
}

// NextEpoch 把 epoch 加一并落盘，返回新值。
func NextEpoch(path string) (int64, error) {
	current, err := ReadEpoch(path)
	if err != nil {
		return 0, err
	}
	next := current + 1
	if err := WriteEpoch(path, next); err != nil {
		return 0, err
	}
	return next, nil
}

// ClientEpochAfterGoingGateway 客户端切 gateway 模式时的 epoch。
//
// 取「同步来的 epoch + 1」，此后每次本地成功刷新再 +1。
// 含义是「这台机器出去自己刷过几次」——回连时报给网关，
// 网关据此知道该拒绝覆盖而不是拿旧的副本盖掉新的。
func ClientEpochAfterGoingGateway(syncedEpoch int64) int64 {
	if syncedEpoch < 0 {
		syncedEpoch = 0
	}
	return syncedEpoch + 1
}

// backoffCapMinutes 是退避上限。封顶很重要：一次长时间断网若不带上限，
// 客户端可能几小时都不再尝试，用户回家后会以为同步坏了。
const backoffCapMinutes = 30

// BackoffMinutes 给出第 attempt 次失败后要等多少分钟。
//
// 1 → 2 → 4 → 8 → 16 → 30 然后一直 30。
// 成功一次立即恢复正常周期，不保留退避状态（状态只在进程内，不落盘：
// 重启就是全新开始，没必要记住「上次失败过」）。
func BackoffMinutes(attempt int) int64 {
	if attempt < 0 {
		attempt = 0
	}
	// 从 1 分钟起，每次翻倍，封顶 30。
	minutes := int64(1) << uint(min(attempt, 5))
	if minutes > backoffCapMinutes {
		return backoffCapMinutes
	}
	return minutes
}

// NormalizeRefreshedAt 把网关给的时间戳统一成 UTC 的 RFC3339Nano。
//
// 不统一就没法比：一个带 +08:00 一个带 Z，直接比字符串会得出错误答案。
// 解析不了时返回空串，调用方据此当作「不知道」而不是「很新」。
func NormalizeRefreshedAt(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

// RefreshedAtIsNewer 判断网关是否比上次拉取时又刷新过。
//
// 这是「刷新后立刻推」的替代实现：P1 不加长连接，
// 客户端在下一次轮询时看这个标志，若网关刷新过就立刻再拉一次。
// 令牌有效期是小时级，最多晚一个轮询周期，代价可接受。
func RefreshedAtIsNewer(gatewayRefreshedAt, lastPulledAt string) bool {
	normalized := NormalizeRefreshedAt(gatewayRefreshedAt)
	if normalized == "" {
		return false
	}
	previous := NormalizeRefreshedAt(lastPulledAt)
	if previous == "" {
		// 从没拉过，当然算新。
		return true
	}
	return normalized > previous
}

// writePrivateFile 落盘 0600 的私有文件，原子替换。
func writePrivateFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建目录失败：%w", err)
		}
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return fmt.Errorf("写入文件失败：%w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("替换文件失败：%w", err)
	}
	return nil
}
