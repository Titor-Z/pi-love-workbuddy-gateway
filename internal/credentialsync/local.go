package credentialsync

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// LocalFile 是客户端本地的凭证副本。
//
// 与 Envelope 的区别在 Credential 的含义：这里存的是**用本机密钥加密后的**
// 登录态文档，磁盘上不留明文。加密发生在写入前一刻（见 Save），
// 解密发生在读取后一刻（见 Load），中间不落地。
type LocalFile struct {
	// Epoch 是这份副本的版本。切过 gateway 模式后它会被抬高，
	// 回连时网关据此拒绝覆盖。
	Epoch int64 `json:"epoch"`
	// SyncedAt 是这份副本到手的时间。
	SyncedAt time.Time `json:"synced_at"`
	// Active 表示本机当前是否以它作为凭证来源。
	// 切回 client 模式后置假——副本留着（下次出门直接用），
	// 但不参与任何请求。
	Active bool `json:"active"`
	// Credential 是加密后的登录态文档。
	Credential map[string]any `json:"credential"`
}

// Save 把一份凭证写进本地文件。credential 必须是**已加密**的文档。
func Save(path string, file LocalFile) error {
	if file.Epoch <= 0 {
		return fmt.Errorf("epoch 必须为正，当前 %d", file.Epoch)
	}
	if len(file.Credential) == 0 {
		return fmt.Errorf("凭证文档为空")
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化凭证副本失败：%w", err)
	}
	if dir := parentDir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建目录失败：%w", err)
		}
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入凭证副本失败：%w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("替换凭证副本失败：%w", err)
	}
	return nil
}

// Load 读本地凭证副本。文件不存在时返回零值与 false，
// 让调用方能区分「没有副本」与「副本坏了」——前者要提示去同步，
// 后者要提示密钥可能丢了。
func Load(path string) (LocalFile, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return LocalFile{}, false, nil
	}
	if err != nil {
		return LocalFile{}, false, fmt.Errorf("读取凭证副本失败：%w", err)
	}
	var file LocalFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return LocalFile{}, false, fmt.Errorf("凭证副本已损坏：%w", err)
	}
	return file, true, nil
}

// SetActive 只改活跃标记，不动凭证本身。
// 切回 client 模式时调用——副本留着，下次出门直接可用。
func SetActive(path string, active bool) error {
	file, exists, err := Load(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("还没有凭证副本")
	}
	if file.Active == active {
		return nil
	}
	file.Active = active
	return Save(path, file)
}

// parentDir 取出目录部分；没有目录成分时返回空串。
func parentDir(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '/' || path[index] == os.PathSeparator {
			if index == 0 {
				return "/"
			}
			return path[:index]
		}
	}
	return ""
}
