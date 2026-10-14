package identity

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeJSONFile 把值写成带缩进的 JSON 文件。
// 这些文件是会被人直接打开看的（配置目录里），可读比省几个字节重要。
func writeJSONFile(path string, value any) error {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("序列化失败：%w", err)
	}
	return writeFile(path, []byte(buffer.String()), 0o644)
}

// writePrivateFile 写只有本用户能读的文件。
// 用于一切间接持有凭据的内容：PSK、以及存了 PSK 的客户端配对配置。
func writePrivateFile(path string, data []byte) error {
	return writeFile(path, data, 0o600)
}

// writeFile 先落再改名，避免写到一半留下一个坏文件。
func writeFile(path string, data []byte, perm os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建目录失败：%w", err)
		}
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, perm); err != nil {
		return fmt.Errorf("写入文件失败：%w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("替换文件失败：%w", err)
	}
	return nil
}

// readTrimmedFile 读文件并去掉首尾空白。
// 密钥文件允许用户在末尾留个换行，不该让那成为「密钥不匹配」的原因。
func readTrimmedFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// base64URLEncode 编码成便于抄录的 URL 安全 base64（不带填充）。
func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// trimPadding 是历史兼容：RawURLEncoding 本就不带填充。
func trimPadding(s string) string {
	return strings.TrimRight(s, "=")
}
