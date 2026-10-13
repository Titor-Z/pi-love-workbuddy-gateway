// Package credential 负责定位、解析、刷新 WorkBuddy 登录态，
// 并在需要时把新令牌原子回写进登录态文件。
package credential

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// envAuthDir 是指定登录态目录的环境变量名。
const envAuthDir = "CODEBUDDY_AUTH_DIR"

// currentAuthFileName 是「当前」登录态文件名，探测时优先采用它。
const currentAuthFileName = "workbuddy-desktop.info"

// Locate 返回 WorkBuddy 登录态文件的路径。
//
// explicit 非空时直接采用；否则按平台默认目录探测：
// 优先使用无时间戳后缀的当前文件，没有则取修改时间最新的 *.info。
func Locate(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("指定的登录态文件不可用：%w", err)
		}
		return explicit, nil
	}
	for _, dir := range candidateDirs() {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		current := filepath.Join(dir, currentAuthFileName)
		if _, err := os.Stat(current); err == nil {
			return current, nil
		}
		if newest := newestInfoFile(dir); newest != "" {
			return newest, nil
		}
	}
	return "", fmt.Errorf("找不到 WorkBuddy 登录态文件，"+
		"请确认桌面端已登录，或用 -auth-file 指定路径（也可设置 %s）", envAuthDir)
}

// candidateDirs 按平台给出登录态目录的候选列表。
func candidateDirs() []string {
	if dir := os.Getenv(envAuthDir); dir != "" {
		return []string{dir}
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support",
			"CodeBuddyExtension", "Data", "Public", "auth")}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return []string{filepath.Join(local, "CodeBuddyExtension", "Data", "Public", "auth")}
	default:
		xdg := os.Getenv("XDG_DATA_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".local", "share")
		}
		return []string{filepath.Join(xdg, "CodeBuddyExtension", "Data", "Public", "auth")}
	}
}

// newestInfoFile 返回目录下修改时间最新的 *.info 文件；没有则返回空串。
func newestInfoFile(dir string) string {
	matches, err := filepath.Glob(filepath.Join(dir, "*.info"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		left, leftErr := os.Stat(matches[i])
		right, rightErr := os.Stat(matches[j])
		if leftErr != nil || rightErr != nil {
			return false
		}
		return left.ModTime().After(right.ModTime())
	})
	return matches[0]
}
