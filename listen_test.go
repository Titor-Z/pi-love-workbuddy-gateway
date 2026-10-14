package main

import (
	"net"
	"strings"
	"testing"
)

// TestExpandExplicitAddressStaysExplicit 显式写了某个 IP 就只绑那一个。
// 那是在明确要求「只在这块网卡上提供服务」，不该被展开成全部网卡。
func TestExpandExplicitAddressStaysExplicit(t *testing.T) {
	got, err := expandListenAddrs("192.168.1.5:8788")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(got) != 1 || got[0] != "192.168.1.5:8788" {
		t.Errorf("= %v，期望只有 192.168.1.5:8788", got)
	}
}

// TestExpandWildcardSkipsLoopback 通配地址展开时**必须跳过 loopback**。
//
// 这是整个端口布局的关键：127.0.0.1 要留给本机的 pi 走明文，
// 展开时把它一起绑上会让 pi 连不上（loopback 被对外那个 TLS 口占了）。
func TestExpandWildcardSkipsLoopback(t *testing.T) {
	got, err := expandListenAddrs("0.0.0.0:8788")
	if err != nil {
		// 无网卡（离线机器）时报错是预期行为，不算测试失败。
		if strings.Contains(err.Error(), "没有找到可用的局域网网卡") {
			t.Skip("本机没有局域网网卡")
		}
		t.Fatalf("解析失败：%v", err)
	}
	for _, address := range got {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			t.Fatalf("%q 不是合法的 host:port：%v", address, splitErr)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			t.Errorf("%q 的主机部分不是 IP", address)
			continue
		}
		if ip.IsLoopback() {
			t.Errorf("展开结果里出现了 loopback（%q）：那会把本机 pi 的口占掉", address)
		}
		if ip.To4() == nil {
			t.Errorf("%q 不是 IPv4：局域网里手抄地址的场景下 IPv6 不好用", address)
		}
	}
}

// TestExpandRejectsBadAddress 地址写错要当场说清楚，不要留到监听时才报。
func TestExpandRejectsBadAddress(t *testing.T) {
	for _, bad := range []string{"192.168.1.5", "192.168.1.5:abc", "::"} {
		if _, err := expandListenAddrs(bad); err == nil {
			t.Errorf("%q 应当报错", bad)
		}
	}
}

// TestLanAddrsAllUsable 枚举出来的地址必须都能真的绑上，
// 否则启动时会因为某块网卡而整体失败。
func TestLanAddrsAllUsable(t *testing.T) {
	addresses, err := lanAddrs("0")
	if err != nil {
		t.Fatalf("枚举失败：%v", err)
	}
	for _, address := range addresses {
		listener, listenErr := listenOne(address)
		if listenErr != nil {
			t.Errorf("%s 绑不上：%v", address, listenErr)
			continue
		}
		_ = listener.Close()
	}
}
