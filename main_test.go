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
	"net/url"
	"runtime"
	"strings"
	"sync"
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
	if cfg.minInterval != 2*time.Second {
		t.Fatalf("minInterval = %s, want 2s", cfg.minInterval)
	}
	if cfg.elapsedThreshold != 5*time.Second {
		t.Fatalf("elapsedThreshold = %s, want 5s", cfg.elapsedThreshold)
	}
	if cfg.ipv6 {
		t.Fatal("ipv6 = true, want false")
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
	for _, want := range []string{"Usage of tlad:", "-url", "http, https, or quic", "-bytes", "-count", "-min-interval", "-elapsed-threshold", "-ipv6", "(default 262144)", "(default 100)", "(default 2s)", "(default 5s)"} {
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

func TestParseConfigIPv6(t *testing.T) {
	cfg, err := parseConfig([]string{"-url", "https://example.com/file", "-ipv6"})
	if err != nil {
		t.Fatalf("parseConfig failed: %v", err)
	}
	if !cfg.ipv6 {
		t.Fatal("ipv6 = false, want true")
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
		{
			name: "zero elapsed threshold",
			args: []string{"-url", "https://example.com/file", "-elapsed-threshold", "0"},
			want: "-elapsed-threshold must be greater than zero",
		},
		{
			name: "negative elapsed threshold",
			args: []string{"-url", "https://example.com/file", "-elapsed-threshold", "-1s"},
			want: "-elapsed-threshold must be greater than zero",
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

func TestSelectRemoteIP(t *testing.T) {
	tests := []struct {
		name      string
		ips       []net.IP
		forceIPv6 bool
		want      string
		wantErr   string
	}{
		{
			name: "default prefers IPv4",
			ips: []net.IP{
				net.ParseIP("2001:db8::1"),
				net.ParseIP("192.0.2.10"),
			},
			want: "192.0.2.10",
		},
		{
			name: "default falls back to IPv6",
			ips: []net.IP{
				net.ParseIP("2001:db8::1"),
			},
			want: "2001:db8::1",
		},
		{
			name: "IPv6 filter selects IPv6",
			ips: []net.IP{
				net.ParseIP("192.0.2.10"),
				net.ParseIP("2001:db8::1"),
			},
			forceIPv6: true,
			want:      "2001:db8::1",
		},
		{
			name: "IPv6 filter errors without IPv6",
			ips: []net.IP{
				net.ParseIP("192.0.2.10"),
			},
			forceIPv6: true,
			wantErr:   "no IPv6 address available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectRemoteIP(tt.ips, tt.forceIPv6)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectRemoteIP failed: %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("selected IP %s, want %s", got, tt.want)
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
					available:        true,
					txRetrans:        7,
					txLostCurrent:    1,
					txRetransCurrent: 2,
					txRetransBytes:   4096,
					dsackDups:        4,
					rxOOO:            3,
					rxReordSeen:      5,
					rxDataSegs:       6,
				},
			},
			want: "port=40000 status=\"206 Partial Content\" bytes=12345 elapsed=1.5s tx_retrans=7 tx_lost_current=1 tx_retrans_current=2 tx_retrans_bytes=4096 dsack_dups=4 rx_ooo=3 rx_reord_seen=5 rx_data_segs=6\n",
		},
		{
			name: "mixed zero tcp stats",
			res: result{
				port:    40005,
				status:  "206 Partial Content",
				bytes:   23456,
				elapsed: 750 * time.Millisecond,
				tcpStats: tcpStats{
					available:        true,
					txRetrans:        7,
					txRetransCurrent: 2,
					dsackDups:        4,
					rxReordSeen:      5,
				},
			},
			want: "port=40005 status=\"206 Partial Content\" bytes=23456 elapsed=750ms tx_retrans=7 tx_retrans_current=2 dsack_dups=4 rx_reord_seen=5\n",
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
			name: "timeout with tcp stats",
			res: result{
				port:     40003,
				status:   "206 Partial Content",
				bytes:    163555,
				elapsed:  30001 * time.Millisecond,
				timedOut: true,
				err:      context.DeadlineExceeded,
				tcpStats: tcpStats{
					available:        true,
					txRetrans:        2,
					txLostCurrent:    3,
					txRetransCurrent: 4,
					txRetransBytes:   8192,
					dsackDups:        5,
					rxOOO:            1,
					rxReordSeen:      6,
					rxDataSegs:       7,
				},
			},
			want: "port=40003 status=\"206 Partial Content\" bytes=163555 elapsed=30.001s+ tx_retrans=2 tx_lost_current=3 tx_retrans_current=4 tx_retrans_bytes=8192 dsack_dups=5 rx_ooo=1 rx_reord_seen=6 rx_data_segs=7\n",
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
		{
			name: "mixed zero quic stats",
			res: result{
				port:    40006,
				status:  "206 Partial Content",
				bytes:   456,
				elapsed: time.Second,
				quicStats: quicStats{
					available:   true,
					packetsLost: 1,
					packetsSent: 10,
					latestRTT:   400 * time.Microsecond,
					smoothedRTT: 30 * time.Millisecond,
				},
			},
			want: "port=40006 status=\"206 Partial Content\" bytes=456 elapsed=1s quic_packets_lost=1 quic_packets_sent=10 quic_smoothed_rtt=30ms\n",
		},
		{
			name: "available all zero stats",
			res: result{
				port:      40007,
				status:    "200 OK",
				bytes:     24,
				elapsed:   2 * time.Millisecond,
				tcpStats:  tcpStats{available: true},
				quicStats: quicStats{available: true},
			},
			want: "port=40007 status=\"200 OK\" bytes=24 elapsed=2ms\n",
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

func TestPrintResultElapsedColoring(t *testing.T) {
	tests := []struct {
		name string
		res  result
		opts resultPrintOptions
		want string
	}{
		{
			name: "disabled",
			res: result{
				port:    40000,
				status:  "200 OK",
				bytes:   24,
				elapsed: 2 * time.Second,
			},
			opts: resultPrintOptions{
				elapsedThreshold: time.Second,
			},
			want: "port=40000 status=\"200 OK\" bytes=24 elapsed=2s\n",
		},
		{
			name: "green below threshold",
			res: result{
				port:    40001,
				status:  "200 OK",
				bytes:   24,
				elapsed: 2 * time.Second,
			},
			opts: resultPrintOptions{
				colorElapsed:     true,
				elapsedThreshold: 5 * time.Second,
			},
			want: "port=40001 status=\"200 OK\" bytes=24 elapsed=\x1b[32m2s\x1b[0m\n",
		},
		{
			name: "green equal threshold",
			res: result{
				port:    40002,
				status:  "200 OK",
				bytes:   24,
				elapsed: 5 * time.Second,
			},
			opts: resultPrintOptions{
				colorElapsed:     true,
				elapsedThreshold: 5 * time.Second,
			},
			want: "port=40002 status=\"200 OK\" bytes=24 elapsed=\x1b[32m5s\x1b[0m\n",
		},
		{
			name: "red above threshold timeout with following fields",
			res: result{
				port:     40003,
				status:   "206 Partial Content",
				bytes:    12345,
				elapsed:  6 * time.Second,
				timedOut: true,
				err:      context.DeadlineExceeded,
				tcpStats: tcpStats{
					available: true,
					txRetrans: 2,
				},
			},
			opts: resultPrintOptions{
				colorElapsed:     true,
				elapsedThreshold: 5 * time.Second,
			},
			want: "port=40003 status=\"206 Partial Content\" bytes=12345 elapsed=\x1b[31m6s\x1b[0m+ tx_retrans=2\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printResultWithOptions(&out, tt.res, tt.opts)
			if got := out.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProgressPrinterWritesSameLineStatus(t *testing.T) {
	var out bytes.Buffer
	progress := &progressPrinter{out: &out, enabled: true}

	progress.set("waiting")
	progress.set("transferring")
	progress.clear()

	got := out.String()
	want := "\rwaiting\x1b[K\rtransferring\x1b[K\r\x1b[K"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("progress output contains newline: %q", got)
	}
}

func TestProgressPrinterDisabledWritesNothing(t *testing.T) {
	var out bytes.Buffer
	progress := &progressPrinter{out: &out}

	progress.set("waiting")
	progress.clear()

	if got := out.String(); got != "" {
		t.Fatalf("output = %q, want empty", got)
	}
}

func TestRunProgressShowsConnectingBeforeBodyTransfer(t *testing.T) {
	headersFlushed := make(chan struct{})
	releaseBody := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseBody)
		})
	}
	defer release()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(headersFlushed)
		<-releaseBody
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	port := freeLocalPort(t)
	out := newRecordingWriter()
	progress := &progressPrinter{out: out, enabled: true}
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), config{
			url:         server.URL,
			bytes:       2,
			startPort:   port,
			count:       1,
			step:        1,
			timeout:     5 * time.Second,
			minInterval: time.Millisecond,
			progress:    progress,
		}, out)
	}()

	select {
	case <-headersFlushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response headers")
	}

	got := waitForOutput(t, out, "\rconnecting\x1b[K", 2*time.Second)
	if strings.Contains(got, "\rtransferring\x1b[K") {
		t.Fatalf("output shows transferring before body bytes were released: %q", got)
	}

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run failed: %v\noutput:\n%s", err, out.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for run to finish")
	}

	got = out.String()
	connectingIndex := strings.Index(got, "\rconnecting\x1b[K")
	transferringIndex := strings.Index(got, "\rtransferring\x1b[K")
	finalIndex := strings.LastIndex(got, fmt.Sprintf("\r\x1b[Kport=%d status=\"206 Partial Content\"", port))
	if connectingIndex < 0 || transferringIndex < 0 || finalIndex < 0 {
		t.Fatalf("output missing expected progress or final result: %q", got)
	}
	if connectingIndex > transferringIndex || transferringIndex > finalIndex {
		t.Fatalf("progress order is wrong: %q", got)
	}
}

type recordingWriter struct {
	mu     sync.Mutex
	out    bytes.Buffer
	writes chan string
}

func newRecordingWriter() *recordingWriter {
	return &recordingWriter{writes: make(chan string, 100)}
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.out.Write(p)
	w.mu.Unlock()
	w.writes <- string(p)
	return n, err
}

func (w *recordingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.String()
}

func waitForOutput(t *testing.T, out *recordingWriter, want string, timeout time.Duration) string {
	t.Helper()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		got := out.String()
		if strings.Contains(got, want) {
			return got
		}

		select {
		case <-out.writes:
		case <-timer.C:
			t.Fatalf("timed out waiting for %q in output: %q", want, got)
		}
	}
}

func TestParentDeadlineRemainsError(t *testing.T) {
	parentCtx, cancelParent := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancelParent()
	reqCtx, cancelReq := context.WithTimeout(parentCtx, time.Second)
	defer cancelReq()

	res := result{}
	markRequestTimeout(&res, parentCtx, reqCtx, context.DeadlineExceeded)
	if res.timedOut {
		t.Fatal("parent deadline was marked as request timeout")
	}
	if !errors.Is(res.err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", res.err)
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

func TestHTTPDownloadCanForceIPv6(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}

	remoteAddrs := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteAddrs <- r.RemoteAddr
		_, _ = io.WriteString(w, "hello over ipv6")
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	port := freeLocalIPv6Port(t)
	res := download(context.Background(), config{
		url:         server.URL,
		bytes:       5,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
		ipv6:        true,
	}, port)

	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if res.bytes != 5 {
		t.Fatalf("read %d bytes, want 5", res.bytes)
	}

	select {
	case remoteAddr := <-remoteAddrs:
		assertIPv6RemotePort(t, remoteAddr, port)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HTTP request")
	}
}

func TestHTTPDownloadFallsBackToIPv6WhenOnlyIPv6Resolves(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}

	remoteAddrs := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteAddrs <- r.RemoteAddr
		_, _ = io.WriteString(w, "hello over ipv6")
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	serverPort := listener.Addr().(*net.TCPAddr).Port
	var lookupCalls atomic.Int32
	port := freeLocalIPv6Port(t)
	res := download(context.Background(), config{
		url:         fmt.Sprintf("http://ipv6-only.test:%d/file", serverPort),
		bytes:       5,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
		lookupIP: func(ctx context.Context, network, host string) ([]net.IP, error) {
			lookupCalls.Add(1)
			if network != "ip" {
				t.Errorf("lookup network = %q, want ip", network)
			}
			if host != "ipv6-only.test" {
				t.Errorf("lookup host = %q, want ipv6-only.test", host)
			}
			return []net.IP{net.ParseIP("::1")}, nil
		},
	}, port)

	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if res.bytes != 5 {
		t.Fatalf("read %d bytes, want 5", res.bytes)
	}
	if lookupCalls.Load() != 1 {
		t.Fatalf("lookup calls = %d, want 1", lookupCalls.Load())
	}

	select {
	case remoteAddr := <-remoteAddrs:
		assertIPv6RemotePort(t, remoteAddr, port)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HTTP request")
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

func TestQUICDownloadUsesResolvedEndpointForFakeDomain(t *testing.T) {
	gotHost := make(chan string, 1)
	seenPort := make(chan int, 1)
	server := newHTTP3TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost <- r.Host
		seenPort <- remotePortFromRequest(t, r)
		_, _ = io.WriteString(w, strings.Repeat("q", 128))
	}))

	parsed, err := url.Parse(server.url)
	if err != nil {
		t.Fatalf("parse HTTP/3 server URL: %v", err)
	}
	_, serverPort, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split HTTP/3 server address %q: %v", parsed.Host, err)
	}

	var lookupCalls atomic.Int32
	port := freeLocalUDPPort(t)
	res := download(context.Background(), config{
		url:         fmt.Sprintf("quic://quic-download.test:%s/file", serverPort),
		bytes:       12,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
		lookupIP: func(ctx context.Context, network, host string) ([]net.IP, error) {
			lookupCalls.Add(1)
			if network != "ip" {
				t.Errorf("lookup network = %q, want ip", network)
			}
			if host != "quic-download.test" {
				t.Errorf("lookup host = %q, want quic-download.test", host)
			}
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
	}, port)

	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if res.bytes != 12 {
		t.Fatalf("read %d bytes, want 12", res.bytes)
	}
	if lookupCalls.Load() != 1 {
		t.Fatalf("lookup calls = %d, want 1", lookupCalls.Load())
	}
	if !res.quicStats.available {
		t.Fatalf("quic stats unavailable: %v", res.quicStats.err)
	}

	select {
	case host := <-gotHost:
		wantHost := fmt.Sprintf("quic-download.test:%s", serverPort)
		if host != wantHost {
			t.Fatalf("Host = %q, want %q", host, wantHost)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for QUIC host")
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

func TestQUICDownloadCanForceIPv6(t *testing.T) {
	remoteAddrs := make(chan string, 1)
	server := newHTTP3IPv6TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteAddrs <- remoteAddrFromRequest(t, r)
		_, _ = io.WriteString(w, strings.Repeat("q", 128))
	}))

	port := freeLocalIPv6UDPPort(t)
	res := download(context.Background(), config{
		url:         server.url,
		bytes:       12,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
		ipv6:        true,
	}, port)

	if res.err != nil {
		t.Fatalf("download failed: %v", res.err)
	}
	if res.bytes != 12 {
		t.Fatalf("read %d bytes, want 12", res.bytes)
	}
	if !res.quicStats.available {
		t.Fatalf("quic stats unavailable: %v", res.quicStats.err)
	}

	select {
	case remoteAddr := <-remoteAddrs:
		assertIPv6RemotePort(t, remoteAddr, port)
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

	if !res.timedOut {
		t.Fatal("expected timeout marker")
	}
	if res.err != nil {
		t.Fatalf("timeout was recorded as error: %v", res.err)
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
	if !strings.Contains(line, " elapsed=") || !strings.Contains(line, "+") {
		t.Fatalf("output %q does not contain timeout elapsed marker", line)
	}
	if strings.Contains(line, "error=") {
		t.Fatalf("output %q contains error field", line)
	}
	for _, field := range []string{"tx_retrans", "tx_lost_current", "tx_retrans_current", "tx_retrans_bytes", "dsack_dups", "rx_ooo", "rx_reord_seen", "rx_data_segs"} {
		if strings.Contains(line, field+"=0") {
			t.Fatalf("output %q contains zero-valued %s field", line, field)
		}
	}
}

func TestRunTimeoutDoesNotFail(t *testing.T) {
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
	var out bytes.Buffer
	err := run(context.Background(), config{
		url:         server.URL,
		bytes:       1024 * 1024,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     50 * time.Millisecond,
		minInterval: time.Millisecond,
	}, &out)
	if err != nil {
		t.Fatalf("run failed on timeout: %v\noutput:\n%s", err, out.String())
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output line count = %d, want 2\noutput:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[1], " elapsed=") || !strings.Contains(lines[1], "+") {
		t.Fatalf("output %q does not contain timeout elapsed marker", lines[1])
	}
	if strings.Contains(lines[1], " error=") {
		t.Fatalf("output %q contains error field", lines[1])
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

	if strings.Contains(out.String(), "\r") || strings.Contains(out.String(), "\x1b[K") {
		t.Fatalf("non-terminal run output contains progress control bytes: %q", out.String())
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
	if len(lines) != len(ports)+1 {
		t.Fatalf("output line count = %d, want %d\noutput:\n%s", len(lines), len(ports)+1, out.String())
	}
	if !strings.HasPrefix(lines[0], "target_host=") || !strings.Contains(lines[0], " target_ip=") {
		t.Fatalf("resolution output line = %q, want target_host and target_ip", lines[0])
	}
	for _, line := range lines[1:] {
		if strings.Contains(line, " error=") {
			t.Fatalf("successful output line %q unexpectedly contains an error field", line)
		}
		if strings.Contains(line, " tcpinfo_error=") {
			t.Fatalf("output line %q unexpectedly contains a tcpinfo_error field", line)
		}
	}
}

func TestRunResolvesHTTPHostOnceAndReusesEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for HTTP test server: %v", err)
	}

	ports := freeLocalPortSequence(t, 3)
	hosts := make(chan string, len(ports))
	localAddrs := make(chan string, len(ports))
	remotePorts := make(chan int, len(ports))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts <- r.Host

		localAddr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if !ok {
			t.Errorf("missing local server address")
			return
		}
		localAddrs <- localAddr.String()

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
		_, _ = io.WriteString(w, "ok")
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	serverPort := listener.Addr().(*net.TCPAddr).Port
	var lookupCalls atomic.Int32
	var out bytes.Buffer
	err = run(context.Background(), config{
		url:         fmt.Sprintf("http://download.test:%d/file", serverPort),
		bytes:       2,
		startPort:   ports[0],
		count:       len(ports),
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
		lookupIP: func(ctx context.Context, network, host string) ([]net.IP, error) {
			lookupCalls.Add(1)
			if network != "ip" {
				t.Errorf("lookup network = %q, want ip", network)
			}
			if host != "download.test" {
				t.Errorf("lookup host = %q, want download.test", host)
			}
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
	}, &out)
	if err != nil {
		t.Fatalf("run failed: %v\noutput:\n%s", err, out.String())
	}
	if lookupCalls.Load() != 1 {
		t.Fatalf("lookup calls = %d, want 1", lookupCalls.Load())
	}
	resolutionLine := strings.Split(strings.TrimSpace(out.String()), "\n")[0]
	wantResolutionLine := "target_host=\"download.test\" target_ip=127.0.0.1"
	if resolutionLine != wantResolutionLine {
		t.Fatalf("resolution output line = %q, want %q", resolutionLine, wantResolutionLine)
	}

	wantHost := fmt.Sprintf("download.test:%d", serverPort)
	for i, wantPort := range ports {
		select {
		case got := <-hosts:
			if got != wantHost {
				t.Fatalf("request %d Host = %q, want %q", i, got, wantHost)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for Host from request %d", i)
		}

		select {
		case got := <-localAddrs:
			host, portText, err := net.SplitHostPort(got)
			if err != nil {
				t.Fatalf("invalid local server addr %q: %v", got, err)
			}
			if host != "127.0.0.1" {
				t.Fatalf("request %d connected to server IP %q, want 127.0.0.1", i, host)
			}
			if portText != fmt.Sprint(serverPort) {
				t.Fatalf("request %d connected to server port %s, want %d", i, portText, serverPort)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for local address from request %d", i)
		}

		select {
		case got := <-remotePorts:
			if got != wantPort {
				t.Fatalf("request %d used local port %d, want %d", i, got, wantPort)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for remote port from request %d", i)
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

func TestRunProgressShows429RetryBeforeFinalResult(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	port := freeLocalPort(t)
	var out bytes.Buffer
	progress := &progressPrinter{out: &out, enabled: true}
	err := run(context.Background(), config{
		url:         server.URL,
		bytes:       2,
		startPort:   port,
		count:       1,
		step:        1,
		timeout:     5 * time.Second,
		minInterval: time.Millisecond,
		progress:    progress,
	}, &out)
	if err != nil {
		t.Fatalf("run failed: %v\noutput:\n%s", err, out.String())
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}

	got := out.String()
	retryIndex := strings.Index(got, "\rretrying\x1b[K")
	if retryIndex < 0 {
		t.Fatalf("output %q does not contain retrying progress", got)
	}
	finalIndex := strings.LastIndex(got, fmt.Sprintf("\r\x1b[Kport=%d status=\"200 OK\"", port))
	if finalIndex < 0 {
		t.Fatalf("output %q does not contain cleared final result", got)
	}
	if retryIndex > finalIndex {
		t.Fatalf("retrying progress appears after final result: %q", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("output newline count = %d, want 2\noutput: %q", strings.Count(got, "\n"), got)
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

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen for HTTP/3 test server: %v", err)
	}

	return newHTTP3TestServerWithConn(t, handler, conn)
}

func newHTTP3IPv6TestServer(t *testing.T, handler http.Handler) *http3TestServer {
	t.Helper()

	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.ParseIP("::1")})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}

	return newHTTP3TestServerWithConn(t, handler, conn)
}

func newHTTP3TestServerWithConn(t *testing.T, handler http.Handler, conn *net.UDPConn) *http3TestServer {
	t.Helper()

	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	certs := certServer.TLS.Certificates
	certServer.Close()

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

	addr := remoteAddrFromRequest(t, r)
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("invalid remote addr %q: %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("invalid remote port %q: %v", portText, err)
	}
	return port
}

func remoteAddrFromRequest(t *testing.T, r *http.Request) string {
	t.Helper()

	addr, ok := r.Context().Value(http3.RemoteAddrContextKey).(net.Addr)
	if !ok {
		t.Fatalf("missing HTTP/3 remote address")
	}
	return addr.String()
}

func assertIPv6RemotePort(t *testing.T, remoteAddr string, wantPort int) {
	t.Helper()

	host, portText, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		t.Fatalf("invalid remote addr %q: %v", remoteAddr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("remote host %q is not an IP address", host)
	}
	if ip.To4() != nil {
		t.Fatalf("remote host %q is IPv4, want IPv6", host)
	}
	var gotPort int
	if _, err := fmt.Sscanf(portText, "%d", &gotPort); err != nil {
		t.Fatalf("invalid remote port %q: %v", portText, err)
	}
	if gotPort != wantPort {
		t.Fatalf("request used source port %d, want %d", gotPort, wantPort)
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

func freeLocalIPv6Port(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
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

func freeLocalIPv6UDPPort(t *testing.T) int {
	t.Helper()

	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.ParseIP("::1")})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
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
