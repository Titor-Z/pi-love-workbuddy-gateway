// Package pairing 保存客户端与网关之间的配对关系。
//
// 单独一个包而不是放在 main 里：面板要读写它，而面板不能反向依赖 main。
// 内容只有三样——网关地址、密钥、证书指纹。
//
// 这个文件间接持有另一台机器上账号的访问权限，所以权限要求
// 与主密钥文件相同（0600）。
package pairing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Info 是一份配对关系。
type Info struct {
	URL         string `json:"url"`
	PSK         string `json:"psk"`
	Fingerprint string `json:"fingerprint"`
}

// Load 读配对关系；不存在时返回零值与 false。
func Load(path string) (Info, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Info{}, false, nil
	}
	if err != nil {
		return Info{}, false, fmt.Errorf("读取配对信息失败：%w", err)
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil {
		return Info{}, false, fmt.Errorf("配对文件已损坏：%w（路径 %s）", err, path)
	}
	return info, true, nil
}

// Save 写配对关系。权限 0600：里面存着密钥。
func Save(path string, info Info) error {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配对信息失败：%w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入配对信息失败：%w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("替换配对信息失败：%w", err)
	}
	return nil
}

// NormalizeAddress 把用户输入补成 https://host:port。
//
// 只允许「主机:端口」这一种裸写法：带 / 或 ? 的裸输入会被 https:// 前缀
// 曲解成另一个地址（h:8790/x 会变成 https://h:8790/x，用户以为的路径
// 其实成了主机的一部分）。宁可让用户写全也不要猜。
func NormalizeAddress(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		return strings.TrimSuffix(value, "/")
	}
	if strings.ContainsAny(value, "/?") {
		return value // 无法安全推断，原样返回让上层报错
	}
	return "https://" + value
}

// NormalizeFingerprint 去掉分隔符统一成小写无分隔 hex。
// 用户可能粘贴带冒号的，也可能手打连着的一串。
func NormalizeFingerprint(raw string) string {
	value := strings.NewReplacer(":", "", " ", "", "\n", "", "\t", "", "\r", "", "-", "").Replace(raw)
	return strings.ToLower(value)
}

// Usable 判断一份配对是否足以发起连接。
func (i Info) Usable() error {
	switch {
	case i.URL == "":
		return fmt.Errorf("尚未指定网关地址")
	case i.PSK == "":
		return fmt.Errorf("尚未填入网关密钥")
	case i.Fingerprint == "":
		return fmt.Errorf("尚未确认网关证书指纹")
	}
	return nil
}
