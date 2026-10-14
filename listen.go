package main

import (
	"fmt"
	"net"
)

// listenOne 在指定地址上建监听器。
func listenOne(addr string) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("监听 %s 失败：%w", addr, err)
	}
	return listener, nil
}
