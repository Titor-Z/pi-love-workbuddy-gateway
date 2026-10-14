// Package relaytls 管理客户端模式与网关之间的 TLS 材料。
//
// 局域网里它只能是自签证书：没有一个双方都信任的 CA 会给这种临时场景发证，
// 走第三方签发（哪怕是免费的）意味着这台机器要能出公网、还要定期续期，
// 对一个「只在家里两台机器之间用」的通道来说不成立。
//
// 真正提供保证的是**证书指纹钉扎**：客户端第一次连的时候把指纹确认一次，
// 之后每次都比对同一个值。这等价于「我只认这一把钥匙」，
// 与证书链上写了什么无关。
package relaytls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// certValidity 是自签证书的有效期。
// 10 年：局域网工具不该因为证书过期而突然连不上，
// 而指纹钉扎意味着续期这件事本来也不提供额外安全。
const certValidity = 10 * 365 * 24 * time.Hour

// 为什么选 ECDSA P-256 而不是 RSA：握手的 CPU 开销与密钥体积都小得多，
// 而局域网通道每次发请求都要建立 TLS 连接，握手是在热路径上。

// LoadOrCreate 读取已有的证书与私钥，缺了就生成一对并落盘。
// 返回值里的指纹是证书 DER 的 SHA-256，给用户看、也给客户端钉扎用。
func LoadOrCreate(certPath, keyPath string) (tls.Certificate, string, error) {
	if certPath == "" || keyPath == "" {
		return tls.Certificate{}, "", fmt.Errorf("证书或私钥路径为空")
	}
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		fingerprint, ferr := Fingerprint(certificate)
		if ferr != nil {
			return tls.Certificate{}, "", ferr
		}
		return certificate, fingerprint, nil
	}
	if !os.IsNotExist(err) {
		// 文件在但读不出来（比如私钥被改坏了）——这种也说清楚，
		// 让用户可以删掉重生成，而不是得到一句「无效密钥」。
		return tls.Certificate{}, "", fmt.Errorf(
			"读取证书失败：%w（若已损坏，删掉 %s 与 %s 后重新启动会自动重签）",
			err, certPath, keyPath)
	}
	return generate(certPath, keyPath)
}

// generate 自签一张服务端证书并落盘。
func generate(certPath, keyPath string) (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("生成私钥失败：%w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("生成序列号失败：%w", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "pi-love-workbuddy-gateway relay"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
	}
	// 自签：签发者与主体是同一张证书。
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("签发证书失败：%w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("序列化私钥失败：%w", err)
	}

	if err := writePEM(certPath, "CERTIFICATE", der); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}

	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("读回刚写的证书失败：%w", err)
	}
	fingerprint, err := Fingerprint(certificate)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return certificate, fingerprint, nil
}

// Fingerprint 算出证书 DER 的 SHA-256，返回便于人工核对的十六进制。
//
// 用冒号分组是为了让人能一段一段比对——一长串连着的 hex 肉眼对不出错。
func Fingerprint(certificate tls.Certificate) (string, error) {
	if len(certificate.Certificate) == 0 {
		return "", fmt.Errorf("证书里没有 DER 数据")
	}
	sum := sha256.Sum256(certificate.Certificate[0])
	return groupHex(hex.EncodeToString(sum[:])), nil
}

// ServerConfig 是网关侧的 TLS 配置。
func ServerConfig(certificate tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
}

// DialerConfig 是客户端侧的 TLS 配置。
//
// 不做常规的证书链校验：局域网自签没有可信签发方，链一定验不过。
// 改用 VerifyPeerCertificate 比对指纹，效果等价于「我只认这一把钥匙」。
//
// pinned 必须是已确认过的指纹；留空则任何人的证书都能连上，
// 那与明文通信没有区别，所以调用方必须在确认前先拿指纹（见 ProbeFingerprint）。
func DialerConfig(pinned string) *tls.Config {
	normalized := normalizeFingerprint(pinned)
	return &tls.Config{
		InsecureSkipVerify: true, // 由下面的指纹比对接管
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if normalized == "" {
				return fmt.Errorf("未确认过网关指纹，拒绝连接")
			}
			if len(rawCerts) == 0 {
				return fmt.Errorf("网关没有提供证书")
			}
			sum := sha256.Sum256(rawCerts[0])
			if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(normalized)) != 1 {
				return fmt.Errorf("网关证书已变化（期望 %s，实际 %s），"+
					"请回到控制台重新确认指纹；网关若确实重装过，这是正常的",
					groupHex(normalized), groupHex(hex.EncodeToString(sum[:])))
			}
			return nil
		},
	}
}

// handshakeDialTimeout 是建立 TCP 连接与完成 TLS 握手的上限。
// 网关离线时不该让客户端挂在那里等系统默认超时——用户会觉得它卡死了。
const handshakeDialTimeout = 5 * time.Second

// responseHeaderTimeout 是等待响应首字节的上限。
// 网关在线但上游卡住时用它报错；它不影响已经开始的流式响应。
const responseHeaderTimeout = 30 * time.Second

// Transport 构造客户端用的传输层，所有该设的都在这里设好。
//
// 做成一个函数而不是让调用方各自拼 http.Transport，是为了让两件容易忘的事
// 不可能被忘掉：
//
//  1. **必须关掉 HTTP/2**。置空 TLSNextProto 才能阻止 ALPN 协商到 h2。
//     h2 会把 SSE 分帧缓冲起来，表现是「客户端模式下流式卡住、最后一次性吐完」，
//     而且只在通过 relay 时才出现，极难联想到这里。
//  2. **拨号与握手必须有超时**，但不能设 http.Client 的 Timeout——
//     那会切断正在进行的流式响应。
func Transport(pinned string) *http.Transport {
	return &http.Transport{
		TLSClientConfig:       DialerConfig(pinned),
		DialContext:           (&net.Dialer{Timeout: handshakeDialTimeout}).DialContext,
		TLSHandshakeTimeout:   handshakeDialTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

// dialer 是握手用的 dialer，超时由调用方交给 ctx。
var dialer = &net.Dialer{Timeout: handshakeDialTimeout}

// ProbeFingerprint 连一次并取回证书指纹，仅此而已。
//
// 它**刻意不校验也不发业务数据**：这一步的语义是「让用户看看这把钥匙长什么样」，
// 而不是「建立信任」。信任是用户按下确认之后、存进配置文件的那一步才成立的。
func ProbeFingerprint(ctx context.Context, addr string) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("网关地址为空")
	}
	dialCtx, cancel := context.WithTimeout(ctx, handshakeDialTimeout)
	defer cancel()
	_ = dialCtx

	tcpConn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("连接网关失败：%w", err)
	}
	defer tcpConn.Close()

	conn := tls.Client(tcpConn, &tls.Config{
		InsecureSkipVerify: true, // 只为了看清证书，不建立信任
		MinVersion:         tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return nil // 探针放行：指纹由调用方展示给用户比对
		},
	})
	if err := conn.HandshakeContext(dialCtx); err != nil {
		return "", fmt.Errorf("握手失败：%w", err)
	}
	defer conn.Close()

	certificates := conn.ConnectionState().PeerCertificates
	if len(certificates) == 0 {
		return "", fmt.Errorf("网关没有提供证书")
	}
	sum := sha256.Sum256(certificates[0].Raw)
	return groupHex(hex.EncodeToString(sum[:])), nil
}

// normalizeFingerprint 去掉分隔符与空白，统一成小写无分隔的 hex。
// 用户可能从控制台粘贴带冒号的、也可能手打一串连着的。
func normalizeFingerprint(value string) string {
	value = strings.NewReplacer(":", "", " ", "", "\n", "", "\t", "", "\r", "", "-", "").Replace(value)
	return strings.ToLower(value)
}

// groupHex 每两个字符插一个冒号，方便人工比对。
func groupHex(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index += 2 {
		if index > 0 {
			builder.WriteByte(':')
		}
		end := index + 2
		if end > len(value) {
			end = len(value)
		}
		builder.WriteString(value[index:end])
	}
	return builder.String()
}

// writePEM 写一段 PEM；perm 为 0 时按 0644 处理。
func writePEM(path, blockType string, der []byte, perm ...os.FileMode) error {
	mode := os.FileMode(0o644)
	for _, p := range perm {
		mode = p
	}
	payload := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if payload == nil {
		return fmt.Errorf("编码 PEM 失败")
	}
	return os.WriteFile(path, payload, mode)
}
