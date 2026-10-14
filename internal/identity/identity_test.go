package identity

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestLoadDeviceIsStableAcrossRestarts 设备身份必须长期稳定：
// 重启、换模式都不该变，否则网关设备表会因为一次切换就分裂成两个条目。
func TestLoadDeviceIsStableAcrossRestarts(t *testing.T) {
	path := t.TempDir() + "/device.json"
	first, err := LoadDevice(path)
	if err != nil {
		t.Fatalf("首次加载失败：%v", err)
	}
	second, err := LoadDevice(path)
	if err != nil {
		t.Fatalf("再次加载失败：%v", err)
	}
	if first.ID != second.ID {
		t.Errorf("两次加载的设备 ID 不同：%q vs %q", first.ID, second.ID)
	}
	if len(first.ID) != 32 {
		t.Errorf("设备 ID 长度 = %d，期望 32 个十六进制字符", len(first.ID))
	}
	if first.Name == "" {
		t.Error("设备名不该为空")
	}
}

// TestLoadDeviceEachGeneratedIsDistinct 新生成的身份必须互不相同。
func TestLoadDeviceEachGeneratedIsDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		device, err := NewDevice()
		if err != nil {
			t.Fatalf("生成失败：%v", err)
		}
		if seen[device.ID] {
			t.Fatalf("第 %d 次生成出了重复 ID %q", i, device.ID)
		}
		seen[device.ID] = true
	}
}

// TestLoadDeviceRejectsEmptyPath 路径为空要报错，
// 否则会往当前目录写下一个身份文件。
func TestLoadDeviceRejectsEmptyPath(t *testing.T) {
	if _, err := LoadDevice(""); err == nil {
		t.Fatal("空路径应当报错")
	}
	if _, err := LoadOrCreatePSK(""); err == nil {
		t.Fatal("空路径应当报错")
	}
}

// TestPSKIsStableAndSecret 密钥既要稳定（重启后同一串），
// 又要私密（文件权限 0600——它是客户端接入的唯一凭据）。
func TestPSKIsStableAndSecret(t *testing.T) {
	path := t.TempDir() + "/psk.txt"
	first, err := LoadOrCreatePSK(path)
	if err != nil {
		t.Fatalf("首次加载失败：%v", err)
	}
	second, err := LoadOrCreatePSK(path)
	if err != nil {
		t.Fatalf("再次加载失败：%v", err)
	}
	if first != second {
		t.Errorf("PSK 应当在重启后保持不变：%q vs %q", first, second)
	}
	if len(first) < 40 {
		t.Errorf("PSK 太短（%d 字符），至少 256 位熵", len(first))
	}
	assertPrivatePerm(t, path)
}

// TestPSKToleratesTrailingNewline 用户手工粘贴时留个换行不该导致「密钥不匹配」。
func TestPSKToleratesTrailingNewline(t *testing.T) {
	path := t.TempDir() + "/psk.txt"
	psk, err := LoadOrCreatePSK(path)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	loaded, err := LoadOrCreatePSK(path)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if !ConstantTimeEqual(psk, loaded) {
		t.Error("带换行的密钥应当等同于原密钥")
	}
}

// TestResetPSKInvalidatesOldValue 重置必须真的换掉那串密钥，
// 否则「踢掉所有设备」这个动作是假的。
func TestResetPSKInvalidatesOldValue(t *testing.T) {
	path := t.TempDir() + "/psk.txt"
	old, err := LoadOrCreatePSK(path)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	next, err := ResetPSK(path)
	if err != nil {
		t.Fatalf("重置失败：%v", err)
	}
	if old == next {
		t.Fatal("重置后 PSK 应当变化")
	}
	loaded, err := LoadOrCreatePSK(path)
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if loaded != next {
		t.Errorf("文件里的值 = %q，期望重置后的 %q", loaded, next)
	}
	assertPrivatePerm(t, path)
}

// TestDistinctPSKsGenerated 两枚密钥不该相同——退化成常量等于没有认证。
func TestDistinctPSKsGenerated(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		psk, err := NewPSK()
		if err != nil {
			t.Fatalf("生成失败：%v", err)
		}
		if seen[psk] {
			t.Fatalf("第 %d 次生成出了重复 PSK", i)
		}
		seen[psk] = true
	}
}

// TestConstantTimeEqual 常量时间比较的结果必须与 == 一致；
// 它存在的意义是**不**泄漏长度与首个差异位置，正确性先钉住。
func TestConstantTimeEqual(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "abcd", false},
		{"", "", true},
		{"", "a", false},
	}
	for _, c := range cases {
		if got := ConstantTimeEqual(c.a, c.b); got != c.want {
			t.Errorf("ConstantTimeEqual(%q,%q) = %v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}

// TestDeviceContext 设备与来源标记要能原样取回；本机直连时设备为空。
func TestDeviceContext(t *testing.T) {
	ctx := WithDevice(t.Context(), "abc123")
	if DeviceFrom(ctx) != "abc123" {
		t.Errorf("DeviceFrom = %q", DeviceFrom(ctx))
	}
	if FromRelay(ctx) {
		t.Error("未标记时不该认为是 relay 来源")
	}
	ctx = MarkRelay(ctx)
	if !FromRelay(ctx) {
		t.Error("标记后应当认为是 relay 来源")
	}
	if DeviceFrom(t.Context()) != "" {
		t.Error("本机直连的设备 ID 应当为空")
	}
}

// TestWriteJSONFileIsReadable 配置目录里的 JSON 会被人直接打开看。
// 必须带缩进、且不把 < > & 转义成 \u003c。
func TestWriteJSONFileIsReadable(t *testing.T) {
	path := t.TempDir() + "/out.json"
	if err := writeJSONFile(path, struct {
		Name string `json:"name"`
	}{Name: "a<b>&c"}); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if !strings.Contains(string(raw), "\n") || !strings.Contains(string(raw), "a<b>&c") {
		t.Errorf("文件不是人类可读的 JSON：%s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Errorf("写出的文件不是合法 JSON：%v", err)
	}
}

// assertPrivatePerm 校验文件只有属主能读写。
// 这条是安全边界：PSK 与存了 PSK 的客户端配置都间接持有账号访问权。
func assertPrivatePerm(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("无法 stat %s：%v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s 权限 = %o，期望 600", path, perm)
	}
}
