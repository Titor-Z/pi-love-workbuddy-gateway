package catalog

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ErrStaleRevision 表示推送方的基准 revision 与当前不一致：
// 对方在我们上次看过之后也改过，不能直接覆盖。调用方要重新拉取、重新合并。
var ErrStaleRevision = errors.New("模型覆盖表已被别处修改")

// Store 是模型覆盖表的读写门面。
//
// 为什么不让各处直接 LoadOverrides/SaveOverrides：同一进程里有两个写入口
// （控制台的面板、局域网 relay 的推送），各持一份内存副本的话，
// 一方写完之后另一方还在拿启动时那份算标签——表现为「面板改了，pi 那边没变」。
// 这里用一把锁把「读—改—写」串起来，并让所有读者都从同一份快照取。
type Store struct {
	mu    sync.Mutex
	path  string
	table *Overrides
}

// OpenStore 读取覆盖表并建一个可共享的 Store。
func OpenStore(path string) (*Store, error) {
	table, err := LoadOverrides(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, table: table}, nil
}

// Path 返回这个 Store 背后的文件路径。
func (s *Store) Path() string { return s.path }

// Snapshot 返回当前表的副本。返回副本而不是指针：
// 调用方（比如 /v1/models 的处理函数）会拿着它做渲染，
// 期间另一次写入不该改变正在渲染的内容，也不该被并发读。
func (s *Store) Snapshot() *Overrides {
	if s == nil {
		// 没有覆盖表（极早期启动或测试）时给空表：Build 对空表就是「没有覆盖项」。
		return NewOverrides()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.table.clone()
}

// Replace 用一份新的 models 映射替换整表，基准是当前内存里那份。
// 这是本地编辑（控制台面板）走的路径。
func (s *Store) Replace(models map[string]Override, by string) (*Overrides, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceLocked(models, by)
}

// Apply 带乐观并发检查地替换整表。
// base 与当前 revision 不一致就返回 ErrStaleRevision，一个字节都不写。
// 这是局域网推送（另一台机器的合并结果）走的路径。
func (s *Store) Apply(base int64, models map[string]Override, by string) (*Overrides, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.table.Revision != base {
		return nil, ErrStaleRevision
	}
	return s.replaceLocked(models, by)
}

func (s *Store) replaceLocked(models map[string]Override, by string) (*Overrides, error) {
	next := NewOverrides()
	next.Models = models
	if err := next.Validate(); err != nil {
		return nil, err
	}
	Touch(s.table, next, by, time.Now())
	if err := SaveOverrides(s.path, next); err != nil {
		return nil, err
	}
	s.table = next
	return next.clone(), nil
}

// Adopt 把权威那一份原样落地（工作副本 + 文件），**不递增 revision、
// 不重打时间戳**。这是「本机没有改动，直接跟上网关」走的路径：
// 用 Replace 会把本机版本号推高，与网关越差越远，下次合并的基准就错了。
func (s *Store) Adopt(table *Overrides) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := SaveOverrides(s.path, table); err != nil {
		return err
	}
	s.table = table.clone()
	return nil
}

// clone 深拷贝一份，避免调用方拿到的表被后续写入改掉。
func (o *Overrides) clone() *Overrides {
	if o == nil {
		return NewOverrides()
	}
	copied := NewOverrides()
	copied.Revision = o.Revision
	copied.ChangeAt = o.ChangeAt
	for id, entry := range o.Models {
		if entry.ThinkingLevelMap != nil {
			levels := make(map[string]*string, len(entry.ThinkingLevelMap))
			for level, value := range entry.ThinkingLevelMap {
				if value != nil {
					text := *value
					levels[level] = &text
					continue
				}
				levels[level] = nil
			}
			entry.ThinkingLevelMap = levels
		}
		copied.Models[id] = entry
	}
	return copied
}

// LoadOverridesFromJSON 从一段 JSON 解析覆盖表，用于网络另一端传来的一份。
func LoadOverridesFromJSON(raw []byte) (*Overrides, error) {
	table := NewOverrides()
	if err := json.Unmarshal(raw, table); err != nil {
		return nil, err
	}
	if table.Models == nil {
		table.Models = map[string]Override{}
	}
	return table, nil
}
