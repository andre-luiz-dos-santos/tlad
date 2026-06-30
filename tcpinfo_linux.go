//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func tcpInfoForConn(conn *net.TCPConn) tcpStats {
	if conn == nil {
		return tcpStats{err: errors.New("tcp connection unavailable")}
	}

	rawConn, err := conn.SyscallConn()
	if err != nil {
		return tcpStats{err: fmt.Errorf("syscall conn: %w", err)}
	}

	var info *unix.TCPInfo
	var tcpInfoErr error
	if err := rawConn.Control(func(fd uintptr) {
		info, tcpInfoErr = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	}); err != nil {
		return tcpStats{err: fmt.Errorf("raw conn control: %w", err)}
	}
	if tcpInfoErr != nil {
		return tcpStats{err: fmt.Errorf("tcp_info: %w", tcpInfoErr)}
	}

	return tcpStatsFromInfo(info)
}

func tcpStatsFromInfo(info *unix.TCPInfo) tcpStats {
	if info == nil {
		return tcpStats{err: errors.New("tcp_info unavailable")}
	}

	return tcpStats{
		available:        true,
		txRetrans:        info.Total_retrans,
		txLostCurrent:    info.Lost,
		txRetransCurrent: info.Retrans,
		txRetransBytes:   info.Bytes_retrans,
		dsackDups:        info.Dsack_dups,
		rxOOO:            info.Rcv_ooopack,
		rxReordSeen:      info.Reord_seen,
		rxDataSegs:       info.Data_segs_in,
	}
}
