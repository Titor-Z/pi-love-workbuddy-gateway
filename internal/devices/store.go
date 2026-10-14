package devices

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// sortByLastSeenDesc 按最后在线时间从新到旧排序。
// 面板要展示的是「最近活跃的几台」，顺序在这里定死，
// 不让调用方各自去排，也不依赖 map 的遍历顺序。
func sortByLastSeenDesc(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].LastSeen.Equal(entries[j].LastSeen) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].LastSeen.After(entries[j].LastSeen)
	})
}

// marshal 序列化成人类可读的 JSON。
// < > & 必须保持原样：这些字符会出现在设备名里（主机名），
// 转义成 \u003c 之后打开文件就认不出是哪台机器了。
func marshal(entries []Entry) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(entries); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// replaceFile 先写临时文件再改名，避免写到一半留下一个坏文件。
func replaceFile(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}
