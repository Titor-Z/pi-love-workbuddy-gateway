package relaytls

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// startRelayServer 起一个走 TLS 的测试服务端，返回地址与真实证书指纹。
func startRelayServer(t *testing.T) (addr string, fingerprint string) {
	t.Helper()
	certificate, err := selfSigned(t)
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	fingerprint, err = Fingerprint(certificate)
	if err != nil {
		t.Fatalf("算指纹失败：%v", err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	server.TLS = ServerConfig(certificate)
	server.StartTLS()
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "https://"), fingerprint
}

func selfSigned(t *testing.T) (tls.Certificate, error) {
	t.Helper()
	dir := t.TempDir()
	certificate, _, err := LoadOrCreate(dir+"/cert.pem", dir+"/key.pem")
	return certificate, err
}

// TestLoadOrCreateIsStableAndPrivate 证书必须稳定（重启后同一把钥匙），
// 私钥必须私密——泄露它等于可以把指纹冒充过去。
func TestLoadOrCreateIsStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := dir+"/cert.pem", dir+"/key.pem"

	first, fp1, err := LoadOrCreate(certPath, keyPath)
	if err != nil {
		t.Fatalf("首次加载失败：%v", err)
	}
	_, fp2, err := LoadOrCreate(certPath, keyPath)
	if err != nil {
		t.Fatalf("再次加载失败：%v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("指纹应当在重启后不变：%q vs %q", fp1, fp2)
	}
	if !strings.Contains(fp1, ":") {
		t.Errorf("指纹应当分组显示便于人工核对：%q", fp1)
	}

	if info, statErr := os.Stat(keyPath); statErr != nil {
		t.Fatalf("无法 stat 私钥：%v", statErr)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("私钥权限 = %o，期望 600", perm)
	}
	_ = first
}

// TestPinnedFingerprintAccepted 指纹对得上就该放行。
func TestPinnedFingerprintAccepted(t *testing.T) {
	addr, fingerprint := startRelayServer(t)
	client := newClient(fingerprint)
	response, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("本该连接成功，实际：%v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d，期望 200", response.StatusCode)
	}
}

// TestChangedFingerprintRejected 指纹变了必须拒绝，且报错要带上期望与实际。
//
// 这条是 TOFU 的核心：网关重装过、证书被删过重签，指纹就会变。
// 静默接受等于把钉扎退化为不校验；所以要报错并让用户重新确认。
func TestChangedFingerprintRejected(t *testing.T) {
	addr, fingerprint := startRelayServer(t)
	other, err := selfSigned(t)
	if err != nil {
		t.Fatalf("生成第二张证书失败：%v", err)
	}
	otherFingerprint, err := Fingerprint(other)
	if err != nil {
		t.Fatalf("算第二张证书的指纹失败：%v", err)
	}
	if otherFingerprint == fingerprint {
		t.Fatal("两次生成的证书指纹相同，测试没有可比性")
	}

	client := newClient(otherFingerprint)
	_, err = client.Get("https://" + addr + "/")
	if err == nil {
		t.Fatal("指纹不同居然连上了")
	}
	message := err.Error()
	if !strings.Contains(message, "网关证书已变化") {
		t.Errorf("报错没有说明是证书变化：%v", message)
	}
	if !strings.Contains(message, otherFingerprint) || !strings.Contains(message, fingerprint) {
		t.Errorf("报错没有同时给出期望与实际指纹，用户没法核对：%v", message)
	}
}

// TestEmptyFingerprintRejected 未确认过指纹就连接 = 任何人都能冒充网关，
// 与明文没有区别。必须拦下。
func TestEmptyFingerprintRejected(t *testing.T) {
	addr, _ := startRelayServer(t)
	client := newClient("")
	_, err := client.Get("https://" + addr + "/")
	if err == nil {
		t.Fatal("未确认指纹竟然连上了")
	}
	if !strings.Contains(err.Error(), "未确认") {
		t.Errorf("报错应当说明原因：%v", err)
	}
}

// TestHTTP2IsDisabled 客户端必须关掉 HTTP/2。
// h2 会把 SSE 分帧缓冲起来，表现是「客户端模式下流式卡住、最后一次性吐完」。
func TestHTTP2IsDisabled(t *testing.T) {
	transport := Transport("ab")
	if transport.TLSNextProto == nil {
		t.Fatal("TLSNextProto 为 nil：Go 会自动协商 HTTP/2")
	}
	for _, protocol := range []string{"h2", "http/1.1"} {
		if handler := transport.TLSNextProto[protocol]; handler != nil {
			t.Errorf("%s 仍有注册的处理函数，应当在客户端置空", protocol)
		}
	}
}

// TestTransportHasDialAndHandshakeTimeouts 整体 Timeout 必须为零（否则会切断流式），
// 而拨号与握手必须有上限（否则网关离线会挂住）。
func TestTransportHasDialAndHandshakeTimeouts(t *testing.T) {
	transport := Transport("ab")
	if transport.TLSHandshakeTimeout <= 0 {
		t.Error("必须设置 TLS 握手超时")
	}
	if transport.DialContext == nil {
		t.Error("必须设置带超时的 DialContext")
	}
	if transport.ResponseHeaderTimeout <= 0 {
		t.Error("必须设置响应首字节超时，否则上游卡住时无法报错")
	}
}

// TestProbeFingerprintMatchesServer 探针要能拿到真实指纹，且不改动信任状态。
func TestProbeFingerprintMatchesServer(t *testing.T) {
	addr, fingerprint := startRelayServer(t)
	probed, err := ProbeFingerprint(t.Context(), addr)
	if err != nil {
		t.Fatalf("探针失败：%v", err)
	}
	if probed != fingerprint {
		t.Errorf("探针拿到的指纹 = %q，期望 %q", probed, fingerprint)
	}
}

// TestProbeFingerprintRejectsEmptyAddr 地址为空不该去连某个默认值。
func TestProbeFingerprintRejectsEmptyAddr(t *testing.T) {
	if _, err := ProbeFingerprint(t.Context(), ""); err == nil {
		t.Fatal("空地址应当报错")
	}
}

// TestProbeFingerprintFailsFast 网关不可达时要在几秒内报错，不能挂住。
func TestProbeFingerprintFailsFast(t *testing.T) {
	// 找一个没有监听的端口。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("无法占用端口：%v", err)
	}
	closed := listener.Addr().String()
	_ = listener.Close()

	start := time.Now()
	if _, err := ProbeFingerprint(t.Context(), closed); err == nil {
		t.Fatal("对端不存在时应当报错")
	}
	if elapsed := time.Since(start); elapsed > handshakeDialTimeout+2*time.Second {
		t.Errorf("失败用了 %v，应当很快返回", elapsed)
	}
}

// TestNormalizeFingerprint 用户可能粘贴带冒号的，也可能手打连着的一串。
func TestNormalizeFingerprint(t *testing.T) {
	cases := map[string]string{
		"ab:cd:ef":       "abcdef",
		"AB:CD:EF":       "abcdef",
		"ab cd ef":       "abcdef",
		"ab\ncd":         "abcd",
		"  ab:cd:ef  \n": "abcdef",
		"ab-cd":          "abcd",
	}
	for input, want := range cases {
		if got := normalizeFingerprint(input); got != want {
			t.Errorf("normalizeFingerprint(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestGroupHex 分组展示要可逆，别把最后一个字符丢了。
func TestGroupHex(t *testing.T) {
	if got := groupHex("abcdef"); got != "ab:cd:ef" {
		t.Errorf("偶数长度 = %q，期望 ab:cd:ef", got)
	}
	if got := groupHex("abc"); got != "ab:c" {
		t.Errorf("奇数长度 = %q，期望 ab:c", got)
	}
	if got := groupHex(""); got != "" {
		t.Errorf("空串 = %q，期望空", got)
	}
}

// newClient 构造一个按给定指纹钉扎的客户端，直接用生产同款 Transport。
func newClient(fingerprint string) *http.Client {
	return &http.Client{Transport: Transport(fingerprint)}
}
