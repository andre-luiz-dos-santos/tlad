//go:build !linux

package main

import (
	"errors"
	"net"
)

func tcpInfoForConn(conn *net.TCPConn) tcpStats {
	if conn == nil {
		return tcpStats{err: errors.New("tcp connection unavailable")}
	}
	return tcpStats{err: errors.New("tcp_info unsupported on this platform")}
}
