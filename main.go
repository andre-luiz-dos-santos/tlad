package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	defaultBytes       = int64(256 * 1024)
	defaultStartPort   = 40000
	defaultCount       = 100
	defaultStep        = 1
	defaultTimeout     = 30 * time.Second
	defaultMinInterval = time.Second
)

type config struct {
	url         string
	bytes       int64
	startPort   int
	count       int
	step        int
	timeout     time.Duration
	minInterval time.Duration
	ipv6        bool

	tlsConfig *tls.Config
}

type result struct {
	port       int
	statusCode int
	status     string
	bytes      int64
	elapsed    time.Duration
	tcpStats   tcpStats
	quicStats  quicStats
	err        error
}

type requestPacer struct {
	minInterval time.Duration
	lastStart   time.Time
}

type tcpStats struct {
	available bool
	txRetrans uint32
	rxOOO     uint32
	err       error
}

type quicStats struct {
	available       bool
	packetsLost     uint64
	bytesLost       uint64
	packetsSent     uint64
	packetsReceived uint64
	latestRTT       time.Duration
	smoothedRTT     time.Duration
	err             error
}

type tcpConnTracker struct {
	mu        sync.Mutex
	conn      *net.TCPConn
	lastStats tcpStats
}

type quicConnTracker struct {
	mu        sync.Mutex
	conn      *quic.Conn
	lastStats quicStats
}

type trackedTCPConn struct {
	*net.TCPConn
	tracker *tcpConnTracker
}

type captureStatsFunc func(*result)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseConfig(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}

	if err := run(context.Background(), cfg, stdout); err != nil {
		return 1
	}
	return 0
}

func parseConfig(args []string) (config, error) {
	cfg := config{}
	fs := newFlagSet(&cfg)
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	return cfg, cfg.validate()
}

func printUsage(out io.Writer) {
	cfg := config{}
	fs := newFlagSet(&cfg)
	fs.SetOutput(out)
	fs.Usage()
}

func newFlagSet(cfg *config) *flag.FlagSet {
	fs := flag.NewFlagSet("tlad", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.url, "url", "", "http, https, or quic URL to download")
	fs.Int64Var(&cfg.bytes, "bytes", defaultBytes, "maximum number of bytes to read per request")
	fs.IntVar(&cfg.startPort, "start-port", defaultStartPort, "first local TCP or UDP source port")
	fs.IntVar(&cfg.count, "count", defaultCount, "number of sequential requests to make")
	fs.IntVar(&cfg.step, "step", defaultStep, "local port increment between requests")
	fs.DurationVar(&cfg.timeout, "timeout", defaultTimeout, "per-request timeout")
	fs.DurationVar(&cfg.minInterval, "min-interval", defaultMinInterval, "minimum time between request attempts")
	fs.BoolVar(&cfg.ipv6, "ipv6", false, "force download connections over IPv6")
	return fs
}

func (cfg config) validate() error {
	if cfg.url == "" {
		return errors.New("missing required -url")
	}
	parsed, err := url.Parse(cfg.url)
	if err != nil {
		return fmt.Errorf("invalid -url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "quic" {
		return fmt.Errorf("unsupported URL scheme %q: only http, https, and quic are supported", parsed.Scheme)
	}
	if parsed.Host == "" {
		return errors.New("invalid -url: missing host")
	}
	if cfg.bytes <= 0 {
		return errors.New("-bytes must be greater than zero")
	}
	if cfg.startPort < 1 || cfg.startPort > 65535 {
		return errors.New("-start-port must be between 1 and 65535")
	}
	if cfg.count <= 0 {
		return errors.New("-count must be greater than zero")
	}
	if cfg.step <= 0 {
		return errors.New("-step must be greater than zero")
	}
	if cfg.timeout <= 0 {
		return errors.New("-timeout must be greater than zero")
	}
	if cfg.minInterval <= 0 {
		return errors.New("-min-interval must be greater than zero")
	}

	lastPort := int64(cfg.startPort) + int64(cfg.count-1)*int64(cfg.step)
	if lastPort > 65535 {
		return fmt.Errorf("port sequence exceeds 65535: last port would be %d", lastPort)
	}

	return nil
}

func run(ctx context.Context, cfg config, out io.Writer) error {
	var failed bool
	pacer := &requestPacer{minInterval: cfg.minInterval}
	for i := 0; i < cfg.count; i++ {
		port := cfg.startPort + i*cfg.step
		res := downloadWithPacer(ctx, cfg, port, pacer)
		printResult(out, res)
		if res.err != nil {
			failed = true
		}
	}
	if failed {
		return errors.New("one or more downloads failed")
	}
	return nil
}

func download(ctx context.Context, cfg config, localPort int) result {
	pacer := &requestPacer{minInterval: cfg.minInterval}
	return downloadWithPacer(ctx, cfg, localPort, pacer)
}

func downloadWithPacer(ctx context.Context, cfg config, localPort int, pacer *requestPacer) result {
	requestURL, err := requestURLForTransport(cfg.url)
	if err != nil {
		return result{port: localPort, err: err}
	}
	if isQUICURL(cfg.url) {
		return downloadQUICWithPacer(ctx, cfg, requestURL, localPort, pacer)
	}
	return downloadHTTPWithPacer(ctx, cfg, requestURL, localPort, pacer)
}

func downloadHTTPWithPacer(ctx context.Context, cfg config, requestURL string, localPort int, pacer *requestPacer) result {
	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{
			Port: localPort,
		},
	}
	tracker := &tcpConnTracker{}
	transport := &http.Transport{
		DialContext:     tracker.dialContext(dialer, cfg.ipv6),
		TLSClientConfig: insecureTLSConfig(cfg.tlsConfig),
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport}
	return downloadWithClient(ctx, cfg, requestURL, localPort, client, pacer, func(res *result) {
		res.tcpStats = tracker.tcpInfo()
	})
}

func downloadQUICWithPacer(ctx context.Context, cfg config, requestURL string, localPort int, pacer *requestPacer) result {
	tracker := &quicConnTracker{}
	network := udpNetwork(cfg.ipv6)
	udpConn, err := net.ListenUDP(network, &net.UDPAddr{Port: localPort})
	if err != nil {
		return result{port: localPort, err: fmt.Errorf("bind local UDP port %d: %w", localPort, err)}
	}

	quicTransport := &quic.Transport{Conn: udpConn}
	transport := &http3.Transport{
		TLSClientConfig: insecureTLSConfig(cfg.tlsConfig),
		Dial: func(ctx context.Context, addr string, tlsCfg *tls.Config, quicCfg *quic.Config) (*quic.Conn, error) {
			udpAddr, err := net.ResolveUDPAddr(network, addr)
			if err != nil {
				return nil, err
			}
			conn, err := quicTransport.Dial(ctx, udpAddr, tlsCfg, quicCfg)
			if err != nil {
				return nil, err
			}
			tracker.set(conn)
			return conn, nil
		},
	}
	defer func() {
		_ = transport.Close()
		_ = quicTransport.Close()
		_ = udpConn.Close()
	}()

	client := &http.Client{Transport: transport}
	return downloadWithClient(ctx, cfg, requestURL, localPort, client, pacer, func(res *result) {
		res.quicStats = tracker.quicStats()
	})
}

func downloadWithClient(ctx context.Context, cfg config, requestURL string, localPort int, client *http.Client, pacer *requestPacer, captureStats captureStatsFunc) result {
	for {
		res := downloadAttempt(ctx, cfg, requestURL, localPort, client, pacer, captureStats)
		if res.statusCode != http.StatusTooManyRequests || cfg.minInterval <= 0 {
			return res
		}
		if err := sleepContext(ctx, 10*cfg.minInterval); err != nil {
			res.err = fmt.Errorf("429 retry wait canceled: %w", err)
			captureStats(&res)
			return res
		}
	}
}

func downloadAttempt(ctx context.Context, cfg config, requestURL string, localPort int, client *http.Client, pacer *requestPacer, captureStats captureStatsFunc) result {
	start := time.Now()
	res := result{port: localPort}

	var err error
	start, err = pacer.wait(ctx)
	if err != nil {
		res.elapsed = time.Since(start)
		res.err = err
		captureStats(&res)
		return res
	}

	reqCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, requestURL, nil)
	if err != nil {
		res.elapsed = time.Since(start)
		res.err = err
		captureStats(&res)
		return res
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", cfg.bytes-1))

	resp, err := client.Do(req)
	if err != nil {
		res.elapsed = time.Since(start)
		res.err = err
		captureStats(&res)
		return res
	}

	res.statusCode = resp.StatusCode
	res.status = resp.Status
	if resp.StatusCode == http.StatusTooManyRequests && cfg.minInterval > 0 {
		res.bytes, err = io.Copy(io.Discard, resp.Body)
	} else {
		res.bytes, err = io.Copy(io.Discard, io.LimitReader(resp.Body, cfg.bytes))
	}
	res.elapsed = time.Since(start)
	if err != nil {
		res.err = err
		_ = resp.Body.Close()
		captureStats(&res)
		return res
	}
	if resp.StatusCode >= http.StatusBadRequest {
		res.err = fmt.Errorf("server returned %s", resp.Status)
	}
	_ = resp.Body.Close()
	captureStats(&res)
	return res
}

func requestURLForTransport(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "quic" {
		return rawURL, nil
	}

	transportURL := *parsed
	transportURL.Scheme = "https"
	return transportURL.String(), nil
}

func isQUICURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == "quic"
}

func insecureTLSConfig(base *tls.Config) *tls.Config {
	if base == nil {
		return &tls.Config{InsecureSkipVerify: true}
	}

	cfg := base.Clone()
	cfg.InsecureSkipVerify = true
	return cfg
}

func (t *tcpConnTracker) dialContext(dialer *net.Dialer, forceIPv6 bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if forceIPv6 {
			network = "tcp6"
		}
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			t.set(tcpConn)
			return &trackedTCPConn{
				TCPConn: tcpConn,
				tracker: t,
			}, nil
		}
		return conn, nil
	}
}

func udpNetwork(forceIPv6 bool) string {
	if forceIPv6 {
		return "udp6"
	}
	return "udp"
}

func (c *trackedTCPConn) Close() error {
	if c.tracker != nil {
		c.tracker.setStats(tcpInfoForConn(c.TCPConn))
	}
	return c.TCPConn.Close()
}

func (t *tcpConnTracker) set(conn *net.TCPConn) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.conn = conn
	t.lastStats = tcpStats{}
}

func (t *tcpConnTracker) latest() *net.TCPConn {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.conn
}

func (t *tcpConnTracker) tcpInfo() tcpStats {
	if t == nil {
		return tcpInfoForConn(nil)
	}

	stats := tcpInfoForConn(t.latest())
	if stats.available {
		t.setStats(stats)
		return stats
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastStats.available {
		return t.lastStats
	}
	return stats
}

func (t *tcpConnTracker) setStats(stats tcpStats) {
	if t == nil || !stats.available {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.lastStats = stats
}

func (t *quicConnTracker) set(conn *quic.Conn) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.conn = conn
	t.lastStats = quicStats{}
}

func (t *quicConnTracker) latest() *quic.Conn {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.conn
}

func (t *quicConnTracker) quicStats() quicStats {
	if t == nil {
		return quicStats{err: errors.New("quic connection unavailable")}
	}

	conn := t.latest()
	if conn != nil {
		stats := quicStatsForConn(conn)
		t.setStats(stats)
		return stats
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastStats.available {
		return t.lastStats
	}
	return quicStats{err: errors.New("quic connection unavailable")}
}

func (t *quicConnTracker) setStats(stats quicStats) {
	if t == nil || !stats.available {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.lastStats = stats
}

func quicStatsForConn(conn *quic.Conn) quicStats {
	if conn == nil {
		return quicStats{err: errors.New("quic connection unavailable")}
	}

	stats := conn.ConnectionStats()
	return quicStats{
		available:       true,
		packetsLost:     stats.PacketsLost,
		bytesLost:       stats.BytesLost,
		packetsSent:     stats.PacketsSent,
		packetsReceived: stats.PacketsReceived,
		latestRTT:       stats.LatestRTT,
		smoothedRTT:     stats.SmoothedRTT,
	}
}

func (p *requestPacer) wait(ctx context.Context) (time.Time, error) {
	if p == nil {
		return time.Now(), nil
	}
	if p.minInterval > 0 && !p.lastStart.IsZero() {
		wait := p.minInterval - time.Since(p.lastStart)
		if wait > 0 {
			if err := sleepContext(ctx, wait); err != nil {
				return time.Now(), err
			}
		}
	}
	p.lastStart = time.Now()
	return p.lastStart, nil
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func printResult(out io.Writer, res result) {
	status := res.status
	if status == "" {
		status = "-"
	}

	fmt.Fprintf(out, "port=%d status=%q bytes=%d elapsed=%s",
		res.port, status, res.bytes, res.elapsed.Round(time.Millisecond))
	if res.err != nil {
		fmt.Fprintf(out, " error=%q", res.err.Error())
	}
	if res.tcpStats.available {
		fmt.Fprintf(out, " tx_retrans=%d rx_ooo=%d", res.tcpStats.txRetrans, res.tcpStats.rxOOO)
	}
	if res.quicStats.available {
		fmt.Fprintf(out, " quic_packets_lost=%d quic_bytes_lost=%d quic_packets_sent=%d quic_packets_received=%d quic_latest_rtt=%s quic_smoothed_rtt=%s",
			res.quicStats.packetsLost,
			res.quicStats.bytesLost,
			res.quicStats.packetsSent,
			res.quicStats.packetsReceived,
			res.quicStats.latestRTT.Round(time.Millisecond),
			res.quicStats.smoothedRTT.Round(time.Millisecond))
	}
	fmt.Fprintln(out)
}
