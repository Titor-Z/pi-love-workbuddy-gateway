package relay

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/atrest"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/credentialsync"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/identity"
	"github.com/Titor-Z/pi-love-workbuddy-gateway/internal/relaytls"
)

// stubCredentialSource 是一个假凭证源，用来验链路而不碰真实登录态。
type stubCredentialSource struct {
	envelope credentialsync.Envelope
	calls    int
}

func (s *stubCredentialSource) CredentialEnvelope() (credentialsync.Envelope, error) {
	s.calls++
	return s.envelope, nil
}

// sealWith 返回一个用给定密钥加密 auth 段两个令牌字段的密封函数。
func sealWith(cipher *atrest.Cipher) func(map[string]any) (map[string]any, error) {
	return func(doc map[string]any) (map[string]any, error) {
		out := copyDoc(doc)
		auth, _ := out["auth"].(map[string]any)
		if auth == nil {
			return nil, errNoAuth
		}
		sealedAuth := copyDoc(auth)
		for _, key := range []string{"accessToken", "refreshToken"} {
			plain, _ := auth[key].(string)
			if plain == "" {
				continue
			}
			envelope, err := cipher.Encrypt(plain)
			if err != nil {
				return nil, err
			}
			sealedAuth[key] = envelope
		}
		out["auth"] = sealedAuth
		return out, nil
	}
}

// unsealWith 是 sealWith 的逆操作。
func unsealWith(cipher *atrest.Cipher, doc map[string]any) (map[string]any, error) {
	out := copyDoc(doc)
	auth, _ := out["auth"].(map[string]any)
	if auth == nil {
		return nil, errNoAuth
	}
	plainAuth := copyDoc(auth)
	for _, key := range []string{"accessToken", "refreshToken"} {
		value := auth[key]
		if !atrest.IsEncrypted(value) {
			plainAuth[key] = value
			continue
		}
		text, err := cipher.Decrypt(value)
		if err != nil {
			return nil, err
		}
		plainAuth[key] = text
	}
	out["auth"] = plainAuth
	return out, nil
}

func copyDoc(doc map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for key, value := range doc {
		out[key] = value
	}
	return out
}

var errNoAuth = &noAuthError{}

type noAuthError struct{}

func (*noAuthError) Error() string { return "凭证缺少 auth 段" }

// TestCredentialSyncEndToEnd 完整链路：
// 网关下发 → TLS → relay 客户端 → 本机加密副本 → 解密还原。
//
// 回答三个单测回答不了的问题：
//  1. 客户端真的能从网关拿到凭证（协议、路由、鉴权都对）；
//  2. 落盘的是**加密**内容（中间没有任何一刻写明文）；
//  3. 解密回来与网关交出的一致（密封/拆封没丢字段）。
func TestCredentialSyncEndToEnd(t *testing.T) {
	dir := t.TempDir()
	certificate, fingerprint, err := relaytls.LoadOrCreate(dir+"/cert.pem", dir+"/key.pem")
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	const psk = "CRED-SYNC-PSK-CRED-SYNC-PSK-CRED-SYNC"

	source := &stubCredentialSource{envelope: credentialsync.Envelope{
		Epoch: 5,
		Credential: map[string]any{
			"auth":    map[string]any{"accessToken": "access-token-plain"},
			"account": map[string]any{"uid": "42"},
		},
	}}
	server := newCredentialTestServer(t, certificate, psk, source)
	defer server.Close()

	clientCipher, err := atrest.NewCipher("client-secret-different-from-gateway")
	if err != nil {
		t.Fatalf("构造客户端密钥失败：%v", err)
	}
	store := filepath.Join(dir, "credentials.json")
	relayClient, err := NewClient(Config{
		BaseURL:     server.URL,
		PSK:         psk,
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "laptop", Name: "笔记本"},
	})
	if err != nil {
		t.Fatalf("构造转发客户端失败：%v", err)
	}

	syncer := &credentialsync.Syncer{
		Source:   relayClient,
		Store:    store,
		Interval: 30 * 60 * 1e9,
		Seal:     sealWith(clientCipher),
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatalf("同步失败：%v", err)
	}

	raw, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("读副本失败：%v", err)
	}
	if strings.Contains(string(raw), "access-token-plain") {
		t.Fatal("明文令牌被写进了磁盘")
	}
	stored, exists, err := credentialsync.Load(store)
	if err != nil || !exists {
		t.Fatalf("副本不存在：%v", err)
	}
	if stored.Epoch != 5 {
		t.Errorf("epoch = %d，期望 5", stored.Epoch)
	}
	if stored.Active {
		t.Error("客户端模式下的副本应当默认非活跃")
	}

	plain, err := unsealWith(clientCipher, stored.Credential)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	auth := plain["auth"].(map[string]any)
	if auth["accessToken"] != "access-token-plain" {
		t.Errorf("还原出的令牌 = %v", auth["accessToken"])
	}
	// 未识别字段必须在往返中存活——丢字段会让 WorkBuddy 客户端判定完整性错误。
	if plain["account"] == nil {
		t.Error("未识别字段在同步中被丢掉了")
	}
}

// TestCredentialSyncDisabledByDefault 网关没开凭证同步时必须返回 403，
// 且错误信息要说清「去控制台开」——那是用户唯一要照着做的一步。
func TestCredentialSyncDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	certificate, fingerprint, err := relaytls.LoadOrCreate(dir+"/cert.pem", dir+"/key.pem")
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	const psk = "CRED-SYNC-PSK-CRED-SYNC-PSK-CRED-SYNC"

	// 刻意不设 CredentialSource。注意传 nil 接口而不是 (*T)(nil)——
	// 后者会让接口非空，踩到 nil 指针解引用（生产里同样会中招）。
	server := newCredentialTestServer(t, certificate, psk, nil)
	defer server.Close()

	relayClient, err := NewClient(Config{
		BaseURL:     server.URL,
		PSK:         psk,
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "laptop"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	if _, err := relayClient.Fetch(context.Background()); err == nil {
		t.Fatal("未开启同步竟然拿到了凭证")
	} else if !strings.Contains(err.Error(), "控制台") {
		t.Errorf("错误没有指出去控制台开：%v", err)
	}
}

// TestCredentialSyncRejectsWrongPSK 凭证端点与转发端点共用同一套准入。
// 密钥不对时必须挡在门外——它比转发请求更敏感。
func TestCredentialSyncRejectsWrongPSK(t *testing.T) {
	dir := t.TempDir()
	certificate, _, err := relaytls.LoadOrCreate(dir+"/cert.pem", dir+"/key.pem")
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	const psk = "CRED-SYNC-PSK-CRED-SYNC-PSK-CRED-SYNC"
	source := &stubCredentialSource{envelope: credentialsync.Envelope{
		Epoch:      1,
		Credential: map[string]any{"auth": map[string]any{"accessToken": "t"}},
	}}
	server := newCredentialTestServer(t, certificate, psk, source)
	defer server.Close()

	fingerprint, err := relaytls.Fingerprint(certificate)
	if err != nil {
		t.Fatalf("算指纹失败：%v", err)
	}
	relayClient, err := NewClient(Config{
		BaseURL:     server.URL,
		PSK:         "WRONG-PSK-WRONG-PSK-WRONG-PSK-WRONG-",
		Fingerprint: fingerprint,
		Device:      &identity.Device{ID: "laptop"},
	})
	if err != nil {
		t.Fatalf("构造客户端失败：%v", err)
	}
	if _, err := relayClient.Fetch(context.Background()); err == nil {
		t.Fatal("密钥不对也拿到了凭证")
	}
}

// newCredentialTestServer 起一个走 TLS 的 relay 服务端。
func newCredentialTestServer(t *testing.T, certificate tls.Certificate,
	psk string, source *stubCredentialSource) *httptest.Server {

	t.Helper()
	deps := Deps{PSK: psk}
	if source != nil {
		deps.CredentialSource = source
	}
	handler := NewServer(deps).Handler()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = serverTLS(certificate)
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}
