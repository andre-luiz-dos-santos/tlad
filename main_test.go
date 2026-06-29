package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing URL",
			args: nil,
			want: "missing required -url",
		},
		{
			name: "unsupported scheme",
			args: []string{"-url", "ftp://example.com/file"},
			want: "unsupported URL scheme",
		},
		{
			name: "invalid bytes",
			args: []string{"-url", "https://example.com/file", "-bytes", "0"},
			want: "-bytes must be greater than zero",
		},
		{
			name: "invalid start port",
			args: []string{"-url", "https://example.com/file", "-start-port", "0"},
			want: "-start-port must be between 1 and 65535",
		},
		{
			name: "invalid count",
			args: []string{"-url", "https://example.com/file", "-count", "0"},
			want: "-count must be greater than zero",
		},
		{
			name: "invalid step",
			args: []string{"-url", "https://example.com/file", "-step", "0"},
			want: "-step must be greater than zero",
		},
		{
			name: "port overflow",
			args: []string{"-url", "https://example.com/file", "-start-port", "65535", "-count", "2"},
			want: "port sequence exceeds 65535",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfig(tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestHTTPDownloadLimitsBytesAndSendsRange(t *testing.T) {
	var gotRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		_, _ = io.WriteString(w, strings.Repeat("a", 128))
	}))
	defer server.Close()

	port := freeLocalPort(t)
	res := download(context.Background(), config{
		url:       server.URL,
		bytes:     12,
		startPort: port,
		count:     1,
		step:      1,
		timeout:   5 * time.Second,
	}, port)

	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if res.bytes != 12 {
		t.Fatalf("read %d bytes, want 12", res.bytes)
	}
	if gotRange != "bytes=0-11" {
		t.Fatalf("Range header = %q, want %q", gotRange, "bytes=0-11")
	}
}

func TestHTTPSDownload(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello over tls")
	}))
	defer server.Close()

	port := freeLocalPort(t)
	res := download(context.Background(), config{
		url:       server.URL,
		bytes:     5,
		startPort: port,
		count:     1,
		step:      1,
		timeout:   5 * time.Second,
		tlsConfig: server.Client().Transport.(*http.Transport).TLSClientConfig,
	}, port)

	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if res.bytes != 5 {
		t.Fatalf("read %d bytes, want 5", res.bytes)
	}
}

func TestRunUsesExpectedPortSequence(t *testing.T) {
	ports := freeLocalPortSequence(t, 3)
	seen := make(chan int, len(ports))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, portText, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			t.Errorf("invalid remote addr %q: %v", r.RemoteAddr, err)
			return
		}
		var port int
		if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
			t.Errorf("invalid remote port %q: %v", portText, err)
			return
		}
		seen <- port
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	var out bytes.Buffer
	err := run(context.Background(), config{
		url:       server.URL,
		bytes:     2,
		startPort: ports[0],
		count:     len(ports),
		step:      1,
		timeout:   5 * time.Second,
	}, &out)
	if err != nil {
		t.Fatalf("run failed: %v\noutput:\n%s", err, out.String())
	}

	for i, want := range ports {
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("request %d used local port %d, want %d", i, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for request %d", i)
		}
	}
}

func freeLocalPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

func freeLocalPortSequence(t *testing.T, count int) []int {
	t.Helper()

	for attempts := 0; attempts < 100; attempts++ {
		start := freeLocalPort(t)
		if start+count-1 > 65535 {
			continue
		}

		listeners := make([]net.Listener, 0, count)
		var ok bool
		for port := start; port < start+count; port++ {
			listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				ok = false
				break
			}
			listeners = append(listeners, listener)
			ok = true
		}

		for _, listener := range listeners {
			if err := listener.Close(); err != nil {
				t.Fatalf("close reserved port: %v", err)
			}
		}

		if ok && len(listeners) == count {
			ports := make([]int, count)
			for i := range ports {
				ports[i] = start + i
			}
			return ports
		}
	}

	t.Fatalf("could not reserve %d contiguous local ports", count)
	return nil
}
