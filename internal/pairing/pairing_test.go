package pairing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveIsPrivate 配对文件里存着网关密钥，权限必须 0600。
// 它间接持有另一台机器上账号的访问权。
func TestSaveIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "client.json")
	if err := Save(path, Info{URL: "https://h:8790", PSK: "k", Fingerprint: "ab"}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败：%v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限 = %o，期望 600", perm)
	}
}

// TestLoadMissingReportsAbsence 没配过不是错误，是「还没配对」这个正常状态。
func TestLoadMissingReportsAbsence(t *testing.T) {
	_, exists, err := Load(filepath.Join(t.TempDir(), "无.json"))
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if exists {
		t.Error("不存在的文件不该报告为存在")
	}
}

// TestRoundTrip 地址、密钥、指纹都要原样往返。
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	want := Info{URL: "https://192.168.1.5:8790", PSK: "secret", Fingerprint: "ab:cd"}
	if err := Save(path, want); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	got, exists, err := Load(path)
	if err != nil || !exists {
		t.Fatalf("读取失败：%v exists=%v", err, exists)
	}
	if got != want {
		t.Errorf("往返后 = %+v，期望 %+v", got, want)
	}
}

// TestNormalizeAddress 裸「主机:端口」补 https；带路径的原样返回让上层报错。
//
// 不能对带 / 的裸输入做补全：h:8790/x 会被曲解成 https://h:8790/x，
// 用户以为的路径其实成了主机的一部分。
func TestNormalizeAddress(t *testing.T) {
	cases := map[string]string{
		"192.168.1.5:8790": "https://192.168.1.5:8790",
		"https://h:8790":   "https://h:8790",
		"https://h:8790/":  "https://h:8790",
		"  h:8790  ":       "https://h:8790",
		"":                 "",
		"h:8790/x":         "h:8790/x",
		"h:8790?q=1":       "h:8790?q=1",
	}
	for input, want := range cases {
		if got := NormalizeAddress(input); got != want {
			t.Errorf("NormalizeAddress(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestNormalizeFingerprint 用户可能粘贴带冒号的，也可能手打连着的一串。
func TestNormalizeFingerprint(t *testing.T) {
	if got := NormalizeFingerprint("AB:CD:EF"); got != "abcdef" {
		t.Errorf("= %q，期望 abcdef", got)
	}
	if got := NormalizeFingerprint(" ab cd "); got != "abcd" {
		t.Errorf("= %q，期望 abcd", got)
	}
}

// TestUsableRequiresAllThree 三样缺一不可。
// 少了任何一样都不该去连——那样要么连不上，要么连上了却不安全。
func TestUsableRequiresAllThree(t *testing.T) {
	if err := (Info{PSK: "k", Fingerprint: "f"}).Usable(); err == nil {
		t.Error("缺地址应当不可用")
	}
	if err := (Info{URL: "u", Fingerprint: "f"}).Usable(); err == nil {
		t.Error("缺密钥应当不可用")
	}
	if err := (Info{URL: "u", PSK: "k"}).Usable(); err == nil {
		t.Error("缺指纹应当不可用——那等于不校验任何证书")
	}
	if err := (Info{URL: "u", PSK: "k", Fingerprint: "f"}).Usable(); err != nil {
		t.Errorf("三样齐全应当可用：%v", err)
	}
	if !strings.Contains((Info{}).Usable().Error(), "地址") {
		t.Error("错误信息应当指出缺的是哪一样")
	}
}
