// 签到记录：落盘只为展示与去重，**不作为判据**。
//
// 判据永远是后端返回的 today_checked_in。本地记录只是为了让控制台不必每次
// 都去问一次后端，以及让用户能看到「最近几天签没签」。
//
// 失败也会记一笔（带 Error 字段）：面板要能显示「今天试过但没签成」。
// 但「今天签过没有」只看 Done——失败记录的 Done 是 false，
// 所以不会把重试挡住；同一天成功时会把失败那条覆盖掉。
package workbuddy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record 是一次签到尝试的结论。
type Record struct {
	// Date 是这条记录属于哪一天，形如 2026-10-17（本地时区）。
	Date string `json:"date"`
	// Done 表示当天确认签到完成。
	Done bool `json:"done"`
	// Credit 是拿到的积分（已签过时为 0）。
	Credit float64 `json:"credit"`
	// StreakDays 是签到后的连续天数。
	StreakDays int `json:"streak_days"`
	// At 是这次尝试的时刻。
	At time.Time `json:"at"`
	// Message 是给用户看的一句话。
	Message string `json:"message"`
	// Error 非空表示这次没签成。
	Error string `json:"error,omitempty"`
}

// Store 是签到记录的读写器。
//
// 同时是「今天是不是刚签过」的短期缓存：同一天的重复调用不必再打后端。
type Store struct {
	path string

	mu      sync.RWMutex
	records []Record
}

// maxRecords 是保留的记录条数。签到一天一条，200 条够看大半年。
const maxRecords = 200

// OpenStore 打开签到记录文件；不存在或损坏时从空记录开始。
func OpenStore(path string) (*Store, error) {
	store := &Store{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		_ = json.Unmarshal(raw, &store.records)
	case os.IsNotExist(err):
		// 首次运行：不建文件，等真有记录再写。
	default:
		return nil, err
	}
	return store, nil
}

// Recent 返回最近的记录，最新的在前。
func (s *Store) Recent(limit int) []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.records) {
		limit = len(s.records)
	}
	out := make([]Record, 0, limit)
	for i := len(s.records) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.records[i])
	}
	return out
}

// Today 返回今天的记录；没有则返回 false。
func (s *Store) Today() (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	today := DayKey(time.Now())
	for i := len(s.records) - 1; i >= 0; i-- {
		if s.records[i].Date == today {
			return s.records[i], true
		}
	}
	return Record{}, false
}

// Put 记录一次结果并落盘。失败也会记录（带 Error），
// 以便面板显示「今天试过但失败了」——但成功的判断只看 Done。
func (s *Store) Put(record Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 同一天只留最新一条（成功会覆盖失败）。
	for i := len(s.records) - 1; i >= 0; i-- {
		if s.records[i].Date == record.Date {
			s.records = append(s.records[:i], s.records[i+1:]...)
			break
		}
	}
	s.records = append(s.records, record)
	if len(s.records) > maxRecords {
		s.records = s.records[len(s.records)-maxRecords:]
	}
	s.saveLocked()
}

// saveLocked 落盘。写失败不影响主流程——这只是展示用的记录。
func (s *Store) saveLocked() {
	if s.path == "" {
		return
	}
	raw, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

// DayKey 返回某个时刻所属的日期，本地时区，形如 2026-10-17。
//
// 签到按自然日算，且后端固定用 Asia/Shanghai。本工具与后端同在国内，
// 本机时区就是对的；不做时区换算，免得引入一个「看起来更严谨、
// 实际更可能是错的」的偏移。
func DayKey(at time.Time) string {
	return at.Format("2006-01-02")
}
