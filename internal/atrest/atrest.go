// Package atrest 处理 WorkBuddy 5.6+ 引入的 $wbEncrypted 加密登录态字段。
//
// 背景：5.6.0 起，登录态文件里的 accessToken / refreshToken 等字段不再是明文字符串，
// 而是形如 {"$wbEncrypted":1,"envelope":"<base64>"} 的加密信封。
// 本包负责派生密钥、解开封套，并在刷新令牌后把明文重新封装回去，
// 以免 WorkBuddy 客户端读到明文后判定完整性错误、重置登录态。
//
// 密钥链路（已实测验证）：
//  1. protectorKey = sha256(atRestSecretKey 的 UTF-8 字节)，32 字节
//  2. keyID        = sha256(protectorKey) 的十六进制前 16 位，全安装一致
//  3. AAD          = 长度前缀的自描述框，见 buildFieldAAD
//  4. AES-256-GCM，nonce 12 字节，authTag 16 字节，密文即明文 UTF-8
package atrest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// envelopeSuite 是本实现唯一支持的信封套件编号（AES-256-GCM / sym-v1）。
const envelopeSuite = 1

// framingField 是「字段级」封装的框类型编号。
const framingField = 2

// envSecretName 是可直接提供 at-rest 根密钥的环境变量名。
const envSecretName = "WORKBUDDY_AT_REST_SECRET"

// envElectronPath 是指定 WorkBuddy 可执行文件路径的环境变量名。
const envElectronPath = "WORKBUDDY_ELECTRON_PATH"

// Cipher 封装 at-rest 密钥派生与字段级加解密。构造后并发安全。
type Cipher struct {
	protectorKey []byte // sha256(atRestSecretKey)，32 字节，即 AES-256 密钥
	keyID        string // sha256(protectorKey) 的前 16 位十六进制
}

var (
	cipherOnce sync.Once
	cipherVal  *Cipher
	cipherErr  error
)

// NewCipher 由 at-rest 根密钥（44 字符 base64）派生出一个 Cipher。
func NewCipher(secret string) (*Cipher, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("at-rest 根密钥为空")
	}
	protectorKey := sha256.Sum256([]byte(secret))
	keyFingerprint := sha256.Sum256(protectorKey[:])
	return &Cipher{
		protectorKey: protectorKey[:],
		keyID:        hex.EncodeToString(keyFingerprint[:])[:16],
	}, nil
}

// Shared 返回进程级共享的 Cipher。explicit 非空时优先采用；
// 否则依次尝试环境变量与 WorkBuddy 自带的 Electron 原生层。
// 结果只解析一次并缓存，失败也会缓存，避免反复拉起子进程。
func Shared(explicit string) (*Cipher, error) {
	cipherOnce.Do(func() {
		secret, err := resolveSecret(explicit)
		if err != nil {
			cipherErr = err
			return
		}
		cipherVal, cipherErr = NewCipher(secret)
	})
	return cipherVal, cipherErr
}

// resolveSecret 按「显式参数 → 环境变量 → 原生层」的顺序取 at-rest 根密钥。
func resolveSecret(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return explicit, nil
	}
	if fromEnv := os.Getenv(envSecretName); strings.TrimSpace(fromEnv) != "" {
		return fromEnv, nil
	}
	return fetchSecretFromElectron()
}

// loggerGetScript 通过 Electron 的原生绑定取出 at-rest 密钥。
// 这是一段 JS，必须原样传给 WorkBuddy 自带的 Electron 可执行文件执行。
const loggerGetScript = "const b = process._linkedBinding('electron_browser_workbuddy_storage');" +
	"process.stdout.write(b.loggerGet().toString('utf8'));"

// fetchSecretFromElectron 以 ELECTRON_RUN_AS_NODE 模式调用 WorkBuddy 自带的 Electron，
// 从原生层的 loggerGet() 拿到 atRestSecretKey。
func fetchSecretFromElectron() (string, error) {
	candidates := electronCandidates()
	if len(candidates) == 0 {
		return "", fmt.Errorf(
			"找不到 WorkBuddy 可执行文件，无法获取 at-rest 根密钥；"+
				"可设置 %s 直接提供（44 字符 base64）", envSecretName)
	}
	var lastErr error
	for _, exe := range candidates {
		cmd := exec.Command(exe, "-e", loggerGetScript)
		cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1")
		out, err := cmd.Output()
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", exe, err)
			continue
		}
		var payload struct {
			AtRestSecretKey string `json:"atRestSecretKey"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			lastErr = fmt.Errorf("%s: 解析 loggerGet 输出失败: %w", exe, err)
			continue
		}
		if payload.AtRestSecretKey == "" {
			lastErr = fmt.Errorf("%s: loggerGet 未返回 atRestSecretKey", exe)
			continue
		}
		return payload.AtRestSecretKey, nil
	}
	return "", fmt.Errorf("获取 at-rest 根密钥失败：%w", lastErr)
}

// electronCandidates 按平台给出 WorkBuddy 可执行文件的候选路径。
func electronCandidates() []string {
	if env := os.Getenv(envElectronPath); env != "" {
		if isFile(env) {
			return []string{env}
		}
		return nil
	}
	home, _ := os.UserHomeDir()
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		paths = []string{"/Applications/WorkBuddy.app/Contents/MacOS/Electron"}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		paths = []string{filepath.Join(local, "Programs", "WorkBuddy", "WorkBuddy.exe")}
	default:
		paths = []string{"/opt/WorkBuddy/workbuddy", "/usr/bin/workbuddy"}
	}
	var found []string
	for _, p := range paths {
		if isFile(p) {
			found = append(found, p)
		}
	}
	return found
}

// isFile 判断路径是否存在且为普通文件。
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// KeyID 返回本机派生出的密钥指纹，可用于与信封里的 keyId 比对。
func (c *Cipher) KeyID() string { return c.keyID }

// IsEncrypted 判断一个登录态字段值是否为 $wbEncrypted 加密信封。
func IsEncrypted(value any) bool {
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if suite, ok := obj["$wbEncrypted"].(float64); !ok || int(suite) != 1 {
		return false
	}
	envelope, ok := obj["envelope"].(string)
	return ok && envelope != ""
}

// Decrypt 解开字段值：明文字符串原样返回，加密信封解密后返回明文。
func (c *Cipher) Decrypt(value any) (string, error) {
	if !IsEncrypted(value) {
		if text, ok := value.(string); ok {
			return text, nil
		}
		return "", nil
	}
	obj := value.(map[string]any)
	raw, err := base64.StdEncoding.DecodeString(obj["envelope"].(string))
	if err != nil {
		return "", fmt.Errorf("信封不是合法 base64：%w", err)
	}
	var envelope struct {
		Suite      int    `json:"suite"`
		KeyID      string `json:"keyId"`
		Nonce      string `json:"nonce"`
		AuthTag    string `json:"authTag"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("信封不是合法 JSON：%w", err)
	}
	if envelope.Suite != envelopeSuite {
		return "", fmt.Errorf("不支持的加密套件：%d", envelope.Suite)
	}
	if envelope.KeyID != c.keyID {
		return "", fmt.Errorf("信封 keyId(%s) 与本机派生 keyId(%s) 不一致，WorkBuddy 可能已更换密钥",
			envelope.KeyID, c.keyID)
	}
	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return "", fmt.Errorf("nonce 不是合法 base64：%w", err)
	}
	authTag, err := base64.StdEncoding.DecodeString(envelope.AuthTag)
	if err != nil {
		return "", fmt.Errorf("authTag 不是合法 base64：%w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("ciphertext 不是合法 base64：%w", err)
	}
	// Go 的 GCM 要求密文与认证标签拼接在一起传入。
	sealed := append(append([]byte{}, ciphertext...), authTag...)
	block, err := aes.NewCipher(c.protectorKey)
	if err != nil {
		return "", fmt.Errorf("构造 AES 失败：%w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("构造 GCM 失败：%w", err)
	}
	plain, err := aead.Open(nil, nonce, sealed, buildFieldAAD(envelope.KeyID))
	if err != nil {
		return "", fmt.Errorf("解密失败（密钥不匹配或信封被改动）：%w", err)
	}
	return string(plain), nil
}

// Encrypt 把明文重新封装成 $wbEncrypted 信封，供刷新令牌后回写登录态使用。
func (c *Cipher) Encrypt(plain string) (map[string]any, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败：%w", err)
	}
	block, err := aes.NewCipher(c.protectorKey)
	if err != nil {
		return nil, fmt.Errorf("构造 AES 失败：%w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("构造 GCM 失败：%w", err)
	}
	sealed := aead.Seal(nil, nonce, []byte(plain), buildFieldAAD(c.keyID))
	// GCM 输出末尾 16 字节是认证标签，信封里要求两者分开存放。
	split := len(sealed) - 16
	envelope := map[string]any{
		"suite":      envelopeSuite,
		"keyId":      c.keyID,
		"nonce":      base64.StdEncoding.EncodeToString(nonce),
		"authTag":    base64.StdEncoding.EncodeToString(sealed[split:]),
		"ciphertext": base64.StdEncoding.EncodeToString(sealed[:split]),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("序列化信封失败：%w", err)
	}
	return map[string]any{
		"$wbEncrypted": float64(1),
		"envelope":     base64.StdEncoding.EncodeToString(raw),
	}, nil
}

// buildFieldAAD 构造「字段级」封装的附加认证数据。
//
// 结构是一个自描述的、每个变长段都带 4 字节大端长度的框：
//
//	"WB-AAD\x00" + 版本字节 1
//	+ lp("WBEV1")   字段格式标识
//	+ lp("sym-v1")  加密方案
//	+ uint32(1)     套件编号
//	+ lp(keyID)     密钥指纹
//	+ 框类型字节 2   字段级
//	+ 0x00          序列号缺省
//	+ 0x00          结束标记缺省
func buildFieldAAD(keyID string) []byte {
	var aad []byte
	aad = append(aad, []byte("WB-AAD\x00")...)
	aad = append(aad, 0x01)
	aad = append(aad, lengthPrefixed([]byte("WBEV1"))...)
	aad = append(aad, lengthPrefixed([]byte("sym-v1"))...)
	aad = append(aad, uint32BE(1)...)
	aad = append(aad, lengthPrefixed([]byte(keyID))...)
	aad = append(aad, framingField)
	aad = append(aad, 0x00)
	aad = append(aad, 0x00)
	return aad
}

// lengthPrefixed 把变长字节段写成「4 字节大端长度 + 内容」。
func lengthPrefixed(b []byte) []byte {
	out := make([]byte, 0, 4+len(b))
	out = append(out, uint32BE(len(b))...)
	return append(out, b...)
}

// uint32BE 把整数编码成 4 字节大端序。
func uint32BE(v int) []byte {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, uint32(v))
	return buf
}
