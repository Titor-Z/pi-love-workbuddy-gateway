package relay

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relaytls"
)

// discardLogger 丢弃日志——relay 的日志不需要出现在测试输出里。
func discardLogger() Reporter { return testReporter{} }

type testReporter struct{}

func (testReporter) Printf(string, ...any) {}

// testCertificate 生成一张测试用自签证书。
func testCertificate(t *testing.T) (tls.Certificate, error) {
	t.Helper()
	certificate, _, err := relaytls.LoadOrCreate(t.TempDir()+"/cert.pem", t.TempDir()+"/key.pem")
	return certificate, err
}

// testFingerprint 算出证书的指纹。
func testFingerprint(certificate tls.Certificate) (string, error) {
	return relaytls.Fingerprint(certificate)
}

// testHTTPClient 构造一个按给定指纹钉扎的客户端。
// 与生产完全同口径（走 relaytls.Transport），避免测试与实跑行为不一致。
func testHTTPClient(t *testing.T, fingerprint string) *http.Client {
	t.Helper()
	return &http.Client{Transport: relaytls.Transport(fingerprint)}
}

// readBody 读出响应正文。
func readBody(response *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return string(raw)
}

// pingWith 发一次 ping，并把 PST 与设备信息带上。
func pingWith(t *testing.T, client *http.Client, baseURL, deviceID, deviceName string) error {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+pingPath, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set(HeaderPSK, "secret")
	request.Header.Set(HeaderDevice, deviceID)
	request.Header.Set(HeaderDeviceName, deviceName)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return io.EOF
	}
	return nil
}

// TestPingServesPlainJSON ping 要能被 curl 之类的工具直接看懂。
func TestPingServesPlainJSON(t *testing.T) {
	client, _, server := newTestServer(t, Deps{PSK: "secret", Version: "test", Logger: discardLogger()})
	request, err := http.NewRequest(http.MethodGet, server.URL+pingPath, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set(HeaderPSK, "secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer response.Body.Close()
	body := readBody(response)
	for _, want := range []string{`"ok":true`, `"version":"test"`, `"protocol":1`} {
		if !strings.Contains(strings.ReplaceAll(body, " ", ""), want) {
			t.Errorf("ping 响应缺少 %s：%s", want, body)
		}
	}
}

// TestSelfSignedCertIsPresent 服务端必须真的提供证书，
// 否则指纹钉扎无从谈起。
func TestSelfSignedCertIsPresent(t *testing.T) {
	certificate, err := testCertificate(t)
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	if len(certificate.Certificate) == 0 {
		t.Fatal("生成的证书里没有 DER 数据")
	}
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatalf("证书无法解析：%v", err)
	}
	if parsed.Subject.CommonName == "" {
		t.Error("证书缺少主体名")
	}
}

// callV1 打一次 /v1/chat/completions，让请求真的走到 relay 的转发分支里。
// ping 挂在 /relay/ 下、不进 Backend，所以验上下文必须走 /v1/。
func callV1(t *testing.T, client *http.Client, baseURL, deviceID, deviceName string) error {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		baseURL+"/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	request.Header.Set(HeaderPSK, "secret")
	request.Header.Set(HeaderDevice, deviceID)
	request.Header.Set(HeaderDeviceName, deviceName)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return nil
}
