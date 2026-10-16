// Package settings 保存面板可改的运行偏好。
//
// 为什么单独一个文件而不是塞进 model-overrides.json：那个文件是模型展示
// （别名、免费截止、思考档位），语义上跟本工具自己的运行参数是两回事，
// 混在一起会让用户编辑其中一个时不小心弄坏另一个。也与 relay.json 分开
// ——那是转发开关。
//
// 读不出来时不报错、退回默认值：偏好文件坏了不该让网关起不来。
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// 日志页的每页条数。上限远大于日常需要，下限留一点余量——
// 一页 1 条会让分页按钮比内容还长。
const (
	// DefaultPageSize 是每页条数的默认值。
	DefaultPageSize = 10
	// MinPageSize / MaxPageSize 是每页条数的允许区间。
	MinPageSize = 5
	MaxPageSize = 500
)

// 日志保留天数。它是**物理删除**的分界：超过这个天数的分片文件会被删掉。
const (
	// DefaultRetentionDays 是保留天数的默认值。
	DefaultRetentionDays = 7
	// MinRetentionDays / MaxRetentionDays 是保留天数的允许区间。
	// 下限 1 天：再短的话「今天写的日志明天就没了」，没有排查价值。
	MinRetentionDays = 1
	MaxRetentionDays = 365
)

// DefaultCheckinTime 是自动签到的默认时刻。
// 选上午 9 点：签到按自然日算，早签早安心，也不会因为机器晚开而错过。
const DefaultCheckinTime = "09:00"

// Settings 是面板可改的运行偏好。
type Settings struct {
	// LogPageSize 是日志页每页显示的条数。
	LogPageSize int `json:"log_page_size"`
	// LogRetentionDays 是事件日志的保留天数，超过的分片会被删除。
	LogRetentionDays int `json:"log_retention_days"`

	// CheckinEnabled 是是否每天自动签到。
	CheckinEnabled bool `json:"checkin_enabled"`
	// CheckinTime 是每天自动签到的时间，形如 "09:00"（本地时区）。
	CheckinTime string `json:"checkin_time"`
	// CheckinRemind 是「当天还没签到时，在首页通知区提醒我」。
	// 它只管「提不提醒」，不管「怎么补」——补签入口官方还没找到，
	// 所以这里不碰那个动作（见 docs/workbuddy-checkin.md）。
	CheckinRemind bool `json:"checkin_remind"`
}

// Default 返回默认偏好。
func Default() Settings {
	return Settings{
		LogPageSize:      DefaultPageSize,
		LogRetentionDays: DefaultRetentionDays,
		CheckinEnabled:   false,
		CheckinTime:      DefaultCheckinTime,
	}
}

// Normalize 把缺省或越界的值收敛到合法区间。
//
// 用「收敛」而不是「报错」是有意的：文件是用户可能手改的，
// 一个 0 或负数如果直接拒绝，网关就起不来了；收敛至少能跑。
// 面板上的显式提交（Update）另走 Validate 报错，让用户知道填错了。
func Normalize(value Settings) Settings {
	if value.LogPageSize < MinPageSize || value.LogPageSize > MaxPageSize {
		value.LogPageSize = DefaultPageSize
	}
	if value.LogRetentionDays < MinRetentionDays || value.LogRetentionDays > MaxRetentionDays {
		value.LogRetentionDays = DefaultRetentionDays
	}
	// 空串也当非法：读文件时要把缺失的这一项补成默认值（与 Default() 一致）。
	if value.CheckinTime == "" || !validClock(value.CheckinTime) {
		value.CheckinTime = DefaultCheckinTime
	}
	return value
}

// Validate 检查面板提交的值，越界时返回给用户看的错误。
func Validate(value Settings) error {
	if value.LogPageSize < MinPageSize || value.LogPageSize > MaxPageSize {
		return fmt.Errorf("每页条数要在 %d～%d 之间", MinPageSize, MaxPageSize)
	}
	if value.LogRetentionDays < MinRetentionDays || value.LogRetentionDays > MaxRetentionDays {
		return fmt.Errorf("日志保留天数要在 %d～%d 之间", MinRetentionDays, MaxRetentionDays)
	}
	if !validClock(value.CheckinTime) {
		return fmt.Errorf("签到时间要写成 HH:MM（如 09:00）")
	}
	return nil
}

// validClock 检查 "HH:MM" 形式的时刻是否合法。
// 不接受单独的分钟或带秒的写法——自动签到不需要那么精确，
// 放宽只会让用户以为它认了而其实没认。
//
// 空串算合法：签到时间是后加的字段，老的调用方不带它。
// 真正的默认值由 Normalize 补（读文件时）与调度器兜（NextRun 里）。
func validClock(value string) bool {
	if value == "" {
		return true
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return false
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return false
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return false
	}
	// 拒绝 "9:00" 这类非零填充写法，保证显示与输入一致。
	return len(parts[0]) == 2 && len(parts[1]) == 2
}

// Store 是带并发安全的偏好读写器。网关要边跑边读它（清理任务看保留天数，
// 面板看每页条数），所以不能只在启动时读一次。
type Store struct {
	path string

	mu      sync.RWMutex
	current Settings
}

// Open 读取偏好文件；文件不存在或损坏时使用默认值。
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("偏好文件路径为空")
	}
	store := &Store{path: path, current: Default()}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var parsed Settings
		if json.Unmarshal(raw, &parsed) == nil {
			// 缺省字段会在 Normalize 里补成默认值：旧版本的文件只有一部分字段，
			// 不能因为少了新加的那一项就把整个文件当损坏。
			store.current = Normalize(parsed)
		}
	case os.IsNotExist(err):
		// 首次运行：用默认值，不立刻建文件（用户没改过就不留空文件）。
	default:
		return nil, fmt.Errorf("读取偏好文件失败：%w", err)
	}
	return store, nil
}

// Get 返回当前偏好。
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// Update 校验并落盘新偏好，成功后立即生效。
func (s *Store) Update(next Settings) error {
	if err := Validate(next); err != nil {
		return err
	}
	// 缺失的时刻补默认值后再落盘：否则存盘的是空串，
	// 重开时被 Normalize 补成默认，往返就不一致了（写进去什么、
	// 读出来应该是同一个东西）。
	if next.CheckinTime == "" {
		next.CheckinTime = DefaultCheckinTime
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化偏好失败：%w", err)
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建配置目录失败：%w", err)
		}
	}
	// 先写临时文件再改名：写到一半被打断也不会留下半个坏文件。
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入偏好文件失败：%w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("替换偏好文件失败：%w", err)
	}

	s.mu.Lock()
	s.current = next
	s.mu.Unlock()
	return nil
}
