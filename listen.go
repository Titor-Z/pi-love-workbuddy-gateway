package main

import (
	"fmt"
	"net"
	"strconv"
)

// expandListenAddrs 把用户给的对外地址展开成真正要绑的地址列表。
//
// 为什么不能直接绑 0.0.0.0：本机的 pi 要用**同一个端口号**走 loopback 明文连
// 本机服务，而 0.0.0.0:8788 已经把 127.0.0.1:8788 占掉了，两者会冲突。
//
// 所以对外地址是「逐个局域网网卡各绑一个」，loopback 留给 pi。
// 用户写的仍然是 0.0.0.0:8788，不用理解这件事。
//
// 显式写了某个 IP 时就只绑那一个，不展开——那种写法是在明确要求
// 「只在这块网卡上提供服务」。
func expandListenAddrs(addr string) ([]string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("对外地址 %q 无法解析：%w", addr, err)
	}
	if port == "" {
		return nil, fmt.Errorf("对外地址 %q 缺少端口", addr)
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("对外地址 %q 的端口不是数字", addr)
	}

	if host != "" && host != "0.0.0.0" && host != "::" {
		return []string{net.JoinHostPort(host, port)}, nil
	}

	// 通配地址：换成每一块能用的局域网网卡。
	addresses, err := lanAddrs(port)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("没有找到可用的局域网网卡。" +
			"其他电脑现在连不到这台机器；接上网线或连上 WiFi 后重启即可")
	}
	return addresses, nil
}

// lanAddrs 列出所有能用的非 loopback 地址，各配给定端口。
//
// 只取 IPv4：局域网里手抄地址的场景下 IPv4 好写得多，
// 而 IPv6 的链路本地地址还要带 scope，不适合让人填。
func lanAddrs(port string) ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("枚举网卡失败：%w", err)
	}
	var addresses []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		assigned, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, assigned := range assigned {
			ipNet, ok := assigned.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			addresses = append(addresses, net.JoinHostPort(ip.String(), port))
		}
	}
	return addresses, nil
}

// listenOne 在指定地址上建监听器。
func listenOne(addr string) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败：%w", addr, err)
	}
	return listener, nil
}
