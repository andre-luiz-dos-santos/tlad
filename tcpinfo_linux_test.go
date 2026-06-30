//go:build linux

package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestTCPStatsFromInfoMapsLinuxTCPInfo(t *testing.T) {
	stats := tcpStatsFromInfo(&unix.TCPInfo{
		Total_retrans: 7,
		Lost:          2,
		Retrans:       3,
		Bytes_retrans: 4096,
		Dsack_dups:    4,
		Rcv_ooopack:   5,
		Reord_seen:    6,
		Data_segs_in:  8,
	})

	if !stats.available {
		t.Fatalf("stats unavailable: %v", stats.err)
	}
	if stats.txRetrans != 7 {
		t.Fatalf("txRetrans = %d, want 7", stats.txRetrans)
	}
	if stats.txLostCurrent != 2 {
		t.Fatalf("txLostCurrent = %d, want 2", stats.txLostCurrent)
	}
	if stats.txRetransCurrent != 3 {
		t.Fatalf("txRetransCurrent = %d, want 3", stats.txRetransCurrent)
	}
	if stats.txRetransBytes != 4096 {
		t.Fatalf("txRetransBytes = %d, want 4096", stats.txRetransBytes)
	}
	if stats.dsackDups != 4 {
		t.Fatalf("dsackDups = %d, want 4", stats.dsackDups)
	}
	if stats.rxOOO != 5 {
		t.Fatalf("rxOOO = %d, want 5", stats.rxOOO)
	}
	if stats.rxReordSeen != 6 {
		t.Fatalf("rxReordSeen = %d, want 6", stats.rxReordSeen)
	}
	if stats.rxDataSegs != 8 {
		t.Fatalf("rxDataSegs = %d, want 8", stats.rxDataSegs)
	}
}
