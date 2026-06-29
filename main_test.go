package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig([]string{"-url", "https://example.com/file"})
	if err != nil {
		t.Fatalf("parseConfig failed: %v", err)
	}
	if cfg.bytes != 256*1024 {
		t.Fatalf("bytes = %d, want %d", cfg.bytes, 256*1024)
	}
	if cfg.count != 100 {
		t.Fatalf("count = %d, want 100", cfg.count)
	}
	if cfg.minInterval != time.Second {
		t.Fatalf("minInterval = %s, want 1s", cfg.minInterval)
	}
}

func TestRunCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"-h"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	help := stdout.String()
	for _, want := range []string{"Usage of tlad:", "-url", "http, https, or quic", "-bytes", "-count", "-min-interval", "(default 262144)", "(default 100)", "(default 1s)"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help output %q does not contain %q", help, want)
		}
	}
}

func TestParseConfigAcceptsQUIC(t *testing.T) {
	if _, err := parseConfig([]string{"-url", "quic://example.com/file"}); err != nil {
		t.Fatalf("parseConfig failed for quic URL: %v", err)
	}
}

func TestParseConfigHelp(t *testing.T) {
	_, err := parseConfig([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
}

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
		{
			name: "zero min interval",
			args: []string{"-url", "https://example.com/file", "-min-interval", "0"},
			want: "-min-interval must be greater than zero",
		},
		{
			name: "negative min interval",
			args: []string{"-url", "https://example.com/file", "-min-interval", "-1s"},
			want: "-min-interval must be greater than zero",
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

func TestPrintResultOmitsUnavailableFields(t *testing.T) {
	tests := []struct {
		name string
		res  result
		want string
	}{
		{
			name: "available",
			res: result{
				port:    40000,
				status:  "206 Partial Content",
				bytes:   12345,
				elapsed: 1500 * time.Millisecond,
				tcpStats: tcpStats{
					available: true,
					txRetrans: 7,
					rxOOO:     3,
				},
			},
			want: "port=40000 status=\"206 Partial Content\" bytes=12345 elapsed=1.5s tx_retrans=7 rx_ooo=3\n",
		},
		{
			name: "error without tcp stats",
			res: result{
				port:     40001,
				bytes:    42,
				elapsed:  time.Millisecond,
				err:      errors.New("download failed"),
				tcpStats: tcpStats{err: errors.New("tcp_info unsupported on this platform")},
			},
			want: "port=40001 status=\"-\" bytes=42 elapsed=1ms error=\"download failed\"\n",
		},
		{
			name: "error with tcp stats",
			res: result{
				port:    40003,
				status:  "206 Partial Content",
				bytes:   163555,
				elapsed: 30001 * time.Millisecond,
				err:     context.DeadlineExceeded,
				tcpStats: tcpStats{
					available: true,
					txRetrans: 2,
					rxOOO:     1,
				},
			},
			want: "port=40003 status=\"206 Partial Content\" bytes=163555 elapsed=30.001s error=\"context deadline exceeded\" tx_retrans=2 rx_ooo=1\n",
		},
		{
			name: "success without tcp stats",
			res: result{
				port:    40002,
				status:  "200 OK",
				bytes:   24,
				elapsed: 2 * time.Millisecond,
			},
			want: "port=40002 status=\"200 OK\" bytes=24 elapsed=2ms\n",
		},
		{
			name: "quic stats",
			res: result{
				port:    40004,
				status:  "206 Partial Content",
				bytes:   123,
				elapsed: time.Second,
				quicStats: quicStats{
					available:       true,
					packetsLost:     1,
					bytesLost:       1200,
					packetsSent:     10,
					packetsReceived: 9,
					latestRTT:       25 * time.Millisecond,
					smoothedRTT:     30 * time.Millisecond,
				},
			},
			want: "port=40004 status=\"206 Partial Content\" bytes=123 elapsed=1s quic_packets_lost=1 quic_bytes_lost=1200 quic_packets_sent=10 quic_packets_received=9 quic_latest_rtt=25ms quic_smoothed_rtt=30ms\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printResult(&out, tt.res)
			if got := out.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
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
		url:         server.URL,
		bytes:       12,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
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

func TestHTTPSDownloadAcceptsSelfSignedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello over tls")
	}))
	defer server.Close()

	port := freeLocalPort(t)
	res := download(context.Background(), config{
		url:         server.URL,
		bytes:       5,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
	}, port)

	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if res.bytes != 5 {
		t.Fatalf("read %d bytes, want 5", res.bytes)
	}
}

func TestQUICDownloadLimitsBytesSendsRangeAndUsesUDPSourcePort(t *testing.T) {
	var gotRange string
	seenPort := make(chan int, 1)
	server := newHTTP3TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		seenPort <- remotePortFromRequest(t, r)
		_, _ = io.WriteString(w, strings.Repeat("q", 128))
	}))

	port := freeLocalUDPPort(t)
	res := download(context.Background(), config{
		url:         server.url,
		bytes:       12,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
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
	if !res.quicStats.available {
		t.Fatalf("quic stats unavailable: %v", res.quicStats.err)
	}

	select {
	case got := <-seenPort:
		if got != port {
			t.Fatalf("request used UDP source port %d, want %d", got, port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QUIC request")
	}
}

func TestDownloadTimeoutRetainsTCPStats(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("TCP_INFO is only supported on Linux")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "partial body")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	port := freeLocalPort(t)
	res := download(context.Background(), config{
		url:         server.URL,
		bytes:       1024 * 1024,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     50 * time.Millisecond,
		minInterval: time.Millisecond,
	}, port)

	if res.err == nil {
		t.Fatal("expected timeout error")
	}
	if res.statusCode != http.StatusPartialContent {
		t.Fatalf("status code = %d, want %d", res.statusCode, http.StatusPartialContent)
	}
	if res.bytes != int64(len("partial body")) {
		t.Fatalf("read %d bytes, want %d", res.bytes, len("partial body"))
	}
	if !res.tcpStats.available {
		t.Fatalf("tcp stats unavailable after timeout: %v", res.tcpStats.err)
	}

	var out bytes.Buffer
	printResult(&out, res)
	line := out.String()
	for _, want := range []string{"error=", "tx_retrans=", "rx_ooo="} {
		if !strings.Contains(line, want) {
			t.Fatalf("output %q does not contain %q", line, want)
		}
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
		url:         server.URL,
		bytes:       2,
		startPort:   ports[0],
		count:       len(ports),
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
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

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(ports) {
		t.Fatalf("output line count = %d, want %d\noutput:\n%s", len(lines), len(ports), out.String())
	}
	for _, line := range lines {
		if strings.Contains(line, " error=") {
			t.Fatalf("successful output line %q unexpectedly contains an error field", line)
		}
		if strings.Contains(line, " tcpinfo_error=") {
			t.Fatalf("output line %q unexpectedly contains a tcpinfo_error field", line)
		}
	}
}

func TestRunHonorsMinimumInterval(t *testing.T) {
	ports := freeLocalPortSequence(t, 3)
	requestTimes := make(chan time.Time, len(ports))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestTimes <- time.Now()
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	var out bytes.Buffer
	minInterval := 25 * time.Millisecond
	err := run(context.Background(), config{
		url:         server.URL,
		bytes:       2,
		startPort:   ports[0],
		count:       len(ports),
		step:        1,
		timeout:     5 * time.Second,
		minInterval: minInterval,
	}, &out)
	if err != nil {
		t.Fatalf("run failed: %v\noutput:\n%s", err, out.String())
	}

	times := make([]time.Time, len(ports))
	for i := range times {
		select {
		case times[i] = <-requestTimes:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for request %d", i)
		}
	}
	for i := 1; i < len(times); i++ {
		if elapsed := times[i].Sub(times[i-1]); elapsed < minInterval-5*time.Millisecond {
			t.Fatalf("request %d started %s after previous request, want at least %s", i, elapsed, minInterval)
		}
	}
}

func TestDownloadRetries429OnSamePortAfterBackoff(t *testing.T) {
	var attempts atomic.Int32
	remotePorts := make(chan int, 2)
	requestTimes := make(chan time.Time, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestTimes <- time.Now()
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
		remotePorts <- port

		if attempts.Add(1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	port := freeLocalPort(t)
	minInterval := 5 * time.Millisecond
	res := download(context.Background(), config{
		url:         server.URL,
		bytes:       2,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: minInterval,
	}, port)
	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}

	firstPort := <-remotePorts
	secondPort := <-remotePorts
	if firstPort != secondPort {
		t.Fatalf("retry used local port %d, want same port %d", secondPort, firstPort)
	}

	firstTime := <-requestTimes
	secondTime := <-requestTimes
	if elapsed := secondTime.Sub(firstTime); elapsed < 10*minInterval {
		t.Fatalf("retry started after %s, want at least %s", elapsed, 10*minInterval)
	}
}

func TestQUICDownloadRetries429OnSameUDPPortAfterBackoff(t *testing.T) {
	var attempts atomic.Int32
	remotePorts := make(chan int, 2)
	requestTimes := make(chan time.Time, 2)

	server := newHTTP3TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestTimes <- time.Now()
		remotePorts <- remotePortFromRequest(t, r)

		if attempts.Add(1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))

	port := freeLocalUDPPort(t)
	minInterval := 5 * time.Millisecond
	res := download(context.Background(), config{
		url:         server.url,
		bytes:       2,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: minInterval,
	}, port)
	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if !res.quicStats.available {
		t.Fatalf("quic stats unavailable: %v", res.quicStats.err)
	}

	firstPort := <-remotePorts
	secondPort := <-remotePorts
	if firstPort != secondPort {
		t.Fatalf("retry used UDP source port %d, want same port %d", secondPort, firstPort)
	}
	if firstPort != port {
		t.Fatalf("request used UDP source port %d, want %d", firstPort, port)
	}

	firstTime := <-requestTimes
	secondTime := <-requestTimes
	if elapsed := secondTime.Sub(firstTime); elapsed < 10*minInterval {
		t.Fatalf("retry started after %s, want at least %s", elapsed, 10*minInterval)
	}
}

type http3TestServer struct {
	url string
}

func newHTTP3TestServer(t *testing.T, handler http.Handler) *http3TestServer {
	t.Helper()

	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	certs := certServer.TLS.Certificates
	certServer.Close()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen for HTTP/3 test server: %v", err)
	}

	server := &http3.Server{
		Handler: handler,
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{
			Certificates: certs,
		}),
	}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(conn)
	}()

	t.Cleanup(func() {
		_ = server.Close()
		_ = conn.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("HTTP/3 test server failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out closing HTTP/3 test server")
		}
	})

	return &http3TestServer{
		url: "quic://" + conn.LocalAddr().String(),
	}
}

func remotePortFromRequest(t *testing.T, r *http.Request) int {
	t.Helper()

	addr, ok := r.Context().Value(http3.RemoteAddrContextKey).(net.Addr)
	if !ok {
		t.Fatalf("missing HTTP/3 remote address")
	}
	_, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		t.Fatalf("invalid remote addr %q: %v", addr.String(), err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("invalid remote port %q: %v", portText, err)
	}
	return port
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

func freeLocalUDPPort(t *testing.T) int {
	t.Helper()

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen for free UDP port: %v", err)
	}
	defer conn.Close()

	return conn.LocalAddr().(*net.UDPAddr).Port
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
