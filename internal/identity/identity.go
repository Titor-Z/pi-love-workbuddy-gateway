// Package identity 提供本设备的长期身份，以及请求链路上的设备上下文。
//
// 设备 ID 与运行模式无关：同一台机器切网关 / 客户端是同一个身份，
// 否则网关侧的设备表会因为一次模式切换就分裂出两个条目。
//
// 关于 device_id 的安全定位（重要）：它由客户端自报，**可以伪造**。
// 它不是访问控制手段，只是审计与归类用的标签。
// 真正的安全边界是 PSK——想踢人只能轮换 PSK。
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// idBytes 是设备 ID 的随机字节数。16 字节 = 128 位，碰撞概率可以忽略。
const idBytes = 16

// pskBytes 是预共享密钥的随机字节数。32 字节 = 256 位。
const pskBytes = 32

// Device 是本设备的长期身份。
type Device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LoadDevice 读取设备身份；文件不存在时生成并落盘。
//
// 为什么不在内存里现生成一个就算了：同一台机器换模式、重启、升级，
// 身份都必须稳定，否则网关侧「这台机器来过几次、什么时候来过」全是乱的。
func LoadDevice(path string) (*Device, error) {
	if path == "" {
		return nil, fmt.Errorf("设备文件路径为空")
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		var device Device
		if json.Unmarshal(raw, &device) == nil && device.ID != "" {
			if device.Name == "" {
				device.Name = defaultDeviceName()
			}
			return &device, nil
		}
		// 文件存在但坏了：宁可重新生成也不要启动失败，
		// 身份不是凭据，丢了不影响别的机器。
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取设备文件失败：%w", err)
	}

	device, err := NewDevice()
	if err != nil {
		return nil, err
	}
	if err := writeJSONFile(path, device); err != nil {
		return nil, err
	}
	return device, nil
}

// NewDevice 生成一个新的设备身份。名字默认取主机名。
func NewDevice() (*Device, error) {
	id := make([]byte, idBytes)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("生成设备 ID 失败：%w", err)
	}
	return &Device{ID: hex.EncodeToString(id), Name: defaultDeviceName()}, nil
}

// defaultDeviceName 取主机名；取不到就落到设备上不了的兜底名字。
func defaultDeviceName() string {
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "未命名设备"
}

// NewPSK 生成一枚预共享密钥，返回便于人工抄录的 base64url 文本。
func NewPSK() (string, error) {
	return randomText(pskBytes)
}

// LoadOrCreatePSK 读取已有的预共享密钥，没有则生成并落盘。
//
// 文件权限是 0600：这串字符就是客户端接入本机网关的唯一凭据，
// 落到别人可读的位置等于把账号交出去。
func LoadOrCreatePSK(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("预共享密钥文件路径为空")
	}
	existing, err := readTrimmedFile(path)
	switch {
	case err == nil:
		return existing, nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("读取预共享密钥失败：%w", err)
	}

	psk, err := NewPSK()
	if err != nil {
		return "", err
	}
	if err := writePrivateFile(path, []byte(psk+"\n")); err != nil {
		return "", err
	}
	return psk, nil
}

// ResetPSK 生成一枚新密钥并覆写文件。已有配对会全部失效。
func ResetPSK(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("预共享密钥文件路径为空")
	}
	psk, err := NewPSK()
	if err != nil {
		return "", err
	}
	if err := writePrivateFile(path, []byte(psk+"\n")); err != nil {
		return "", err
	}
	return psk, nil
}

// ConstantTimeEqual 比较两个密钥是否相同。
// 不能用 ==：字符串比较会按字节短路，长度差与首个不同字节的位置都会成为计时信息。
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomText(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机数失败：%w", err)
	}
	return base64URLEncode(buf), nil
}
