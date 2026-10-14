// Package devices 记录网关侧见过的客户端设备。
//
// 它是**观测式**的：客户端带着 PSK 连过来就自动登记，不需要用户先加白名单。
// 因为真正的准入是 PSK——能用正确的 PSK 连上，说明它本来就被允许了；
// 注册表负责的是「谁在用、什么时候用过」，是人读的东西。
//
// 关于吊销的效力（重要）：设备 ID 由客户端**自报**，可以伪造。
// 所以吊销某个 ID 只是让那个 ID 暂时不再被接受——持有 PSK 的人换个 ID
// 就能绕过去。它整理列表、挡住误配置的机器，不是访问控制。
// 真正要踢人只能轮换 PSK。
package devices

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// flushInterval 是 LastSeen 的落盘节流间隔。
// 每个请求都写一次文件的话，一次长对话能把磁盘写上百次，
// 而「最后一次在线」精确到秒毫无意义。
const flushInterval = 30 * time.Second

// Entry 是一台设备的登记条目。
type Entry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Revoked   bool      `json:"revoked"`
}

// Registry 是设备登记表。可以在多个请求之间共享，自身是并发安全的。
type Registry struct {
	path string

	mu      sync.RWMutex
	entries map[string]*Entry
}

// Open 读取设备登记表；文件不存在时从空表开始。
//
// 表格本身不是凭据，读不出来就当作还没有设备登记过——让网关能照常起来，
// 而不是因为一个附属文件坏了就拒绝对外服务。
func Open(path string) (*Registry, error) {
	if path == "" {
		return nil, fmt.Errorf("设备注册表路径为空")
	}
	registry := &Registry{path: path, entries: map[string]*Entry{}}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var entries []Entry
		if json.Unmarshal(raw, &entries) != nil {
			// 坏文件：留着它不覆盖，等下次有人动表的时候再写，
			// 至少给用户一个从别处找回的机会。
			return registry, nil
		}
		for index := range entries {
			entry := entries[index]
			registry.entries[entry.ID] = &entry
		}
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("读取设备注册表失败：%w", err)
	}
	return registry, nil
}

// Observe 记录一次设备的到访。之前没见过就登记，见过就更新最后在线时间。
//
// 名字空的情况是客户端没报——留住原来的名字，不要把它抹成空。
func (r *Registry) Observe(id, name string) {
	if id == "" {
		return
	}
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	entry, known := r.entries[id]
	if !known {
		entry = &Entry{ID: id, Name: name, FirstSeen: now}
		r.entries[id] = entry
		// 新设备立刻落盘：它会在面板上出现，重启不该让它消失。
		defer func() { _ = r.save() }()
	}
	if name != "" {
		entry.Name = name
	}
	if now.Sub(entry.LastSeen) < flushInterval {
		return
	}
	entry.LastSeen = now
	_ = r.save()
}

// IsRevoked 判断某设备是否被吊销。未知设备不算吊销——它只是还没见过。
func (r *Registry) IsRevoked(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, known := r.entries[id]
	return known && entry.Revoked
}

// List 返回全部登记项，按最后在线时间从新到旧排。
// 顺序在这里定死，不要让调用方各自排——map 的遍历顺序不是排序。
func (r *Registry) List() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := make([]Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, *entry)
	}
	sortByLastSeenDesc(entries)
	return entries
}

// Revoke 吊销一台设备。id 不存在时返回 false，让调用方能据此报错。
func (r *Registry) Revoke(id string) bool {
	return r.setState(id, true)
}

// Unrevoke 撤销吊销——误操作时要能回头，否则用户只能去删文件。
func (r *Registry) Unrevoke(id string) bool {
	return r.setState(id, false)
}

func (r *Registry) setState(id string, revoked bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, known := r.entries[id]
	if !known {
		return false
	}
	entry.Revoked = revoked
	return r.save() == nil
}

// NameOf 补全设备名给日志显示用。
// 日志里只存 ID：名字会改，存进去的就是过期数据。
func (r *Registry) NameOf(id string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, known := r.entries[id]; known && entry.Name != "" {
		return entry.Name
	}
	return id
}

// save 落盘。调用方必须已持有写锁。
func (r *Registry) save() error {
	if r.path == "" {
		return nil
	}
	entries := make([]Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, *entry)
	}
	sortByLastSeenDesc(entries)
	raw, err := marshal(entries)
	if err != nil {
		return err
	}
	return replaceFile(r.path, raw)
}
