package atrest

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// 参考向量由 Python 参考实现（cryptography 库 + WorkBuddy 官方 AAD 规则）生成，
// 不是用本包自己算出来的。它的意义是：只要这些断言还通过，
// Go 实现与参考实现在字节层面就是一致的。
//
// 生成方式（secret 固定为 32 个零字节的 base64，nonce 固定为 0x00..0x0b）：
//
//	AESGCM(sha256(secret)).encrypt(nonce, plaintext, aad)
const (
	referenceSecret = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	referenceKeyID  = "c9534a8b5bbb49fe"
	referencePlain  = "hello-workbuddy-测试"
	referenceAADHex = "57422d41414400010000000557424556310000000673796d2d7631" +
		"000000010000001063393533346138623562626234396665020000"
	referenceEnvelope = `{"$wbEncrypted":1,"envelope":"eyJzdWl0ZSI6MSwia2V5SWQiOiJjOTUzNGE4YjViYmI0OWZlIiwibm9uY2UiOiJBQUVDQXdRRkJnY0lDUW9MIiwiYXV0aFRhZyI6IjdMbFNUN09GNUZXU0NUdVJHK1NjYlE9PSIsImNpcGhlcnRleHQiOiJFRzJoQnJpV2hRVk0rS1kweGNZY25tQTVPbU9mUVE9PSJ9"}`
)

// mustCipher 构造一个使用参考密钥的 Cipher，失败直接让测试终止。
func mustCipher(t *testing.T) *Cipher {
	t.Helper()
	cipher, err := NewCipher(referenceSecret)
	if err != nil {
		t.Fatalf("NewCipher 失败：%v", err)
	}
	return cipher
}

// TestKeyIDMatchesReference 钉住密钥派生算法：keyID 一变，
// 所有已存在的加密登录态都会解不开。
func TestKeyIDMatchesReference(t *testing.T) {
	if got := mustCipher(t).KeyID(); got != referenceKeyID {
		t.Fatalf("keyID = %q，期望 %q", got, referenceKeyID)
	}
}

// TestBuildFieldAADGolden 用十六进制金标准钉住 AAD 框的字节布局。
func TestBuildFieldAADGolden(t *testing.T) {
	got := hex.EncodeToString(buildFieldAAD(referenceKeyID))
	if got != referenceAADHex {
		t.Fatalf("AAD 字节不一致\n实际：%s\n期望：%s", got, referenceAADHex)
	}
}

// TestDecryptReferenceEnvelope 解开由参考实现生成的密文。
// 这是本包最关键的测试：它同时覆盖密钥派生、AAD 构造与 GCM 参数。
func TestDecryptReferenceEnvelope(t *testing.T) {
	var value any
	if err := json.Unmarshal([]byte(referenceEnvelope), &value); err != nil {
		t.Fatalf("解析参考信封失败：%v", err)
	}
	got, err := mustCipher(t).Decrypt(value)
	if err != nil {
		t.Fatalf("解密参考信封失败：%v", err)
	}
	if got != referencePlain {
		t.Fatalf("明文 = %q，期望 %q", got, referencePlain)
	}
}

// TestEncryptDecryptRoundTrip 保证自己封装的信封自己能解开，
// 且两种方向的格式都符合参考实现的约定。
func TestEncryptDecryptRoundTrip(t *testing.T) {
	cipher := mustCipher(t)
	for _, plain := range []string{"", "a", referencePlain, strings.Repeat("长", 4096)} {
		sealed, err := cipher.Encrypt(plain)
		if err != nil {
			t.Fatalf("加密 %q 失败：%v", plain, err)
		}
		if !IsEncrypted(sealed) {
			t.Fatalf("加密结果不是合法信封：%v", sealed)
		}
		got, err := cipher.Decrypt(sealed)
		if err != nil {
			t.Fatalf("解密 %q 失败：%v", plain, err)
		}
		if got != plain {
			t.Fatalf("往返后 = %q，期望 %q", got, plain)
		}
	}
}

// TestEncryptUsesFreshNonce 保证每次加密的 nonce 都不同。
// 复用 nonce 会直接摧毁 GCM 的安全性，这里做个回归防线。
func TestEncryptUsesFreshNonce(t *testing.T) {
	cipher := mustCipher(t)
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		sealed, err := cipher.Encrypt("same-plaintext")
		if err != nil {
			t.Fatalf("加密失败：%v", err)
		}
		nonce := nonceOf(t, sealed)
		if seen[nonce] {
			t.Fatalf("第 %d 次加密复用了 nonce %s", i, nonce)
		}
		seen[nonce] = true
	}
}

// envelopeFields 是信封内部结构的测试用视角。
type envelopeFields struct {
	Suite      int    `json:"suite"`
	KeyID      string `json:"keyId"`
	Nonce      string `json:"nonce"`
	AuthTag    string `json:"authTag"`
	Ciphertext string `json:"ciphertext"`
}

// parseEnvelope 从 $wbEncrypted 值里解出信封内部字段，仅用于测试。
func parseEnvelope(t *testing.T, sealed map[string]any) envelopeFields {
	t.Helper()
	text, ok := sealed["envelope"].(string)
	if !ok {
		t.Fatalf("信封缺少 envelope 字段：%v", sealed)
	}
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		t.Fatalf("信封不是合法 base64：%v", err)
	}
	var fields envelopeFields
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("信封不是合法 JSON：%v", err)
	}
	return fields
}

// base64Of 把任意值序列化成 JSON 再做一层 base64，仅用于构造测试输入。
func base64Of(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// nonceOf 从信封里取出 nonce，仅用于测试。
func nonceOf(t *testing.T, sealed map[string]any) string {
	t.Helper()
	return parseEnvelope(t, sealed).Nonce
}

// TestDecryptRejectsWrongSecret 用另一把密钥解不开，且报错可读。
func TestDecryptRejectsWrongSecret(t *testing.T) {
	other, err := NewCipher("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=")
	if err != nil {
		t.Fatalf("NewCipher 失败：%v", err)
	}
	if other.KeyID() == referenceKeyID {
		t.Fatal("两把不同的密钥派生出同一个 keyId，密钥派生有问题")
	}
	var value any
	if err := json.Unmarshal([]byte(referenceEnvelope), &value); err != nil {
		t.Fatalf("解析参考信封失败：%v", err)
	}
	_, err = other.Decrypt(value)
	if err == nil {
		t.Fatal("用错误的密钥竟然解密成功了")
	}
	if !strings.Contains(err.Error(), "keyId") {
		t.Fatalf("错误信息应指出 keyId 不一致，实际：%v", err)
	}
}

// TestDecryptRejectsUnsupportedSuite 保证不认识的套件号会被明确拒绝，
// 而不是被静默当成能解。
func TestDecryptRejectsUnsupportedSuite(t *testing.T) {
	cipher := mustCipher(t)
	sealed := map[string]any{
		"$wbEncrypted": float64(1),
		"envelope": base64Of(t, map[string]any{
			"suite": 9, "keyId": referenceKeyID,
			"nonce": "AAECAwQFBgcICQoL", "authTag": "AAAA", "ciphertext": "AAAA",
		}),
	}
	_, err := cipher.Decrypt(sealed)
	if err == nil || !strings.Contains(err.Error(), "套件") {
		t.Fatalf("应报「不支持的加密套件」，实际：%v", err)
	}
}

// TestDecryptRejectsMalformedEnvelope 垃圾输入必须报错，不能 panic。
func TestDecryptRejectsMalformedEnvelope(t *testing.T) {
	cipher := mustCipher(t)
	cases := map[string]map[string]any{
		"envelope 不是 base64": {"$wbEncrypted": float64(1), "envelope": "!!!"},
		"envelope 不是 JSON": {"$wbEncrypted": float64(1),
			"envelope": base64Of(t, "just-a-string")},
		"nonce 不是 base64": {"$wbEncrypted": float64(1),
			"envelope": base64Of(t, map[string]any{"suite": 1, "keyId": referenceKeyID,
				"nonce": "!!!", "authTag": "AAAA", "ciphertext": "AAAA"})},
	}
	for name, value := range cases {
		if _, err := cipher.Decrypt(value); err == nil {
			t.Errorf("%s：应当报错，但通过了", name)
		}
	}
}

// TestIsEncrypted 覆盖信封识别的各种输入。
func TestIsEncrypted(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"正常信封", map[string]any{"$wbEncrypted": float64(1), "envelope": "x"}, true},
		{"明文 token", "eyJhbGciOi", false},
		{"空串", "", false},
		{"nil", nil, false},
		{"缺少 envelope", map[string]any{"$wbEncrypted": float64(1)}, false},
		{"envelope 为空", map[string]any{"$wbEncrypted": float64(1), "envelope": ""}, false},
		{"版本号不对", map[string]any{"$wbEncrypted": float64(2), "envelope": "x"}, false},
		{"数组", []any{1, 2}, false},
	}
	for _, testCase := range cases {
		if got := IsEncrypted(testCase.value); got != testCase.want {
			t.Errorf("%s：IsEncrypted = %v，期望 %v", testCase.name, got, testCase.want)
		}
	}
}

// TestDecryptPassesThroughPlainText 5.6 之前的登录态是明文，必须原样返回。
func TestDecryptPassesThroughPlainText(t *testing.T) {
	got, err := mustCipher(t).Decrypt("plain-token-value")
	if err != nil {
		t.Fatalf("明文不该报错：%v", err)
	}
	if got != "plain-token-value" {
		t.Fatalf("明文被改动了：%q", got)
	}
}

// TestResolveSecretPrefersArgumentThenEnv 保证取值顺序：
// 显式参数 > 环境变量 > WorkBuddy 原生层。前两级都不该触发子进程。
func TestResolveSecretPrefersArgumentThenEnv(t *testing.T) {
	t.Setenv(envSecretName, "from-env")

	got, err := resolveSecret("from-argument")
	if err != nil {
		t.Fatalf("显式参数应当直接采用：%v", err)
	}
	if got != "from-argument" {
		t.Fatalf("got %q，期望 from-argument", got)
	}

	got, err = resolveSecret("")
	if err != nil {
		t.Fatalf("环境变量应当被采用：%v", err)
	}
	if got != "from-env" {
		t.Fatalf("got %q，期望 from-env", got)
	}
}

// TestNewCipherRejectsEmptySecret 空密钥必须立刻失败，
// 而不是派生出一个人人可用的固定密钥。
func TestNewCipherRejectsEmptySecret(t *testing.T) {
	for _, secret := range []string{"", "   "} {
		if _, err := NewCipher(secret); err == nil {
			t.Errorf("密钥 %q 应当被拒绝", secret)
		}
	}
}

// TestFetchSecretFromElectronRespectsEnvPath 指定了不存在的可执行文件时，
// 应当快速失败并指向环境变量这个替代方案，而不是卡住。
func TestFetchSecretFromElectronRespectsEnvPath(t *testing.T) {
	t.Setenv(envElectronPath, "/nonexistent/workbuddy")
	_, err := fetchSecretFromElectron()
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "找不到") || !strings.Contains(err.Error(), envSecretName) {
		t.Fatalf("错误信息应指出找不到可执行文件、并给出环境变量这个替代方案，实际：%v", err)
	}
}
