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
	"strconv"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	defaultBytes            = int64(256 * 1024)
	defaultStartPort        = 61000
	defaultCount            = 100
	defaultStep             = 1
	defaultTimeout          = 30 * time.Second
	defaultMinInterval      = time.Second
	defaultElapsedThreshold = 5 * time.Second

	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiReset = "\x1b[0m"
)

type config struct {
	url              string
	bytes            int64
	startPort        int
	count            int
	step             int
	timeout          time.Duration
	minInterval      time.Duration
	elapsedThreshold time.Duration
	colorElapsed     bool
	ipv6             bool

	endpoint *resolvedEndpoint
	lookupIP lookupIPFunc

	tlsConfig *tls.Config
}

type result struct {
	port       int
	statusCode int
	status     string
	bytes      int64
	elapsed    time.Duration
	timedOut   bool
	tcpStats   tcpStats
	quicStats  quicStats
	err        error
}

type requestPacer struct {
	minInterval time.Duration
	lastStart   time.Time
}

type lookupIPFunc func(context.Context, string, string) ([]net.IP, error)

type resolvedEndpoint struct {
	host       string
	port       int
	portText   string
	ip         net.IP
	address    string
	tcpNetwork string
	udpNetwork string
}

type tcpStats struct {
	available        bool
	txRetrans        uint32
	txLostCurrent    uint32
	txRetransCurrent uint32
	txRetransBytes   uint64
	dsackDups        uint32
	rxOOO            uint32
	rxReordSeen      uint32
	rxDataSegs       uint32
	err              error
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

type resultPrintOptions struct {
	colorElapsed     bool
	elapsedThreshold time.Duration
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
	cfg.colorElapsed = isTerminal(stdout)

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
	fs.DurationVar(&cfg.elapsedThreshold, "elapsed-threshold", defaultElapsedThreshold, "elapsed time color threshold")
	fs.BoolVar(&cfg.ipv6, "ipv6", false, "select an IPv6 address instead of the default IPv4 preference")
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
	if cfg.elapsedThreshold <= 0 {
		return errors.New("-elapsed-threshold must be greater than zero")
	}

	lastPort := int64(cfg.startPort) + int64(cfg.count-1)*int64(cfg.step)
	if lastPort > 65535 {
		return fmt.Errorf("port sequence exceeds 65535: last port would be %d", lastPort)
	}

	return nil
}

func run(ctx context.Context, cfg config, out io.Writer) error {
	resolvedCfg, err := cfg.withResolvedEndpoint(ctx)
	if err != nil {
		return err
	}
	printResolvedEndpoint(out, resolvedCfg.endpoint)

	var failed bool
	pacer := &requestPacer{minInterval: resolvedCfg.minInterval}
	for i := 0; i < resolvedCfg.count; i++ {
		port := resolvedCfg.startPort + i*resolvedCfg.step
		res := downloadWithPacer(ctx, resolvedCfg, port, pacer)
		printResultWithOptions(out, res, resultPrintOptions{
			colorElapsed:     resolvedCfg.colorElapsed,
			elapsedThreshold: resolvedCfg.elapsedThreshold,
		})
		if res.err != nil && !res.timedOut {
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
	resolvedCfg, err := cfg.withResolvedEndpoint(ctx)
	if err != nil {
		return result{port: localPort, err: err}
	}

	requestURL, err := requestURLForTransport(cfg.url)
	if err != nil {
		return result{port: localPort, err: err}
	}
	if isQUICURL(cfg.url) {
		return downloadQUICWithPacer(ctx, resolvedCfg, requestURL, localPort, pacer)
	}
	return downloadHTTPWithPacer(ctx, resolvedCfg, requestURL, localPort, pacer)
}

func downloadHTTPWithPacer(ctx context.Context, cfg config, requestURL string, localPort int, pacer *requestPacer) result {
	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{
			Port: localPort,
		},
	}
	tracker := &tcpConnTracker{}
	transport := &http.Transport{
		DialContext:     tracker.dialContext(dialer, cfg.endpoint),
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
	udpConn, err := net.ListenUDP(cfg.endpoint.udpNetwork, &net.UDPAddr{Port: localPort})
	if err != nil {
		return result{port: localPort, err: fmt.Errorf("bind local UDP port %d: %w", localPort, err)}
	}

	quicTransport := &quic.Transport{Conn: udpConn}
	udpAddr := &net.UDPAddr{IP: cfg.endpoint.ip, Port: cfg.endpoint.port}
	transport := &http3.Transport{
		TLSClientConfig: insecureTLSConfig(cfg.tlsConfig),
		Dial: func(ctx context.Context, addr string, tlsCfg *tls.Config, quicCfg *quic.Config) (*quic.Conn, error) {
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
		if res.timedOut || res.statusCode != http.StatusTooManyRequests || cfg.minInterval <= 0 {
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
		markRequestTimeout(&res, ctx, reqCtx, err)
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
		markRequestTimeout(&res, ctx, reqCtx, err)
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

func markRequestTimeout(res *result, parentCtx, reqCtx context.Context, err error) {
	if parentCtx.Err() == nil && errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
		res.timedOut = true
		return
	}
	res.err = err
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

func (cfg config) withResolvedEndpoint(ctx context.Context) (config, error) {
	if cfg.endpoint != nil {
		return cfg, nil
	}

	endpoint, err := resolveEndpoint(ctx, cfg)
	if err != nil {
		return config{}, err
	}
	cfg.endpoint = endpoint
	return cfg, nil
}

func resolveEndpoint(ctx context.Context, cfg config) (*resolvedEndpoint, error) {
	parsed, err := url.Parse(cfg.url)
	if err != nil {
		return nil, err
	}

	host := parsed.Hostname()
	if host == "" {
		return nil, errors.New("invalid -url: missing host")
	}

	portText := parsed.Port()
	if portText == "" {
		portText, err = defaultPort(parsed.Scheme)
		if err != nil {
			return nil, err
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", portText, err)
	}

	ips, err := resolveHostIPs(ctx, cfg, host)
	if err != nil {
		return nil, err
	}
	ip, err := selectRemoteIP(ips, cfg.ipv6)
	if err != nil {
		return nil, fmt.Errorf("select IP for host %q: %w", host, err)
	}

	tcpNetwork, udpNetwork := endpointNetworks(ip)
	return &resolvedEndpoint{
		host:       host,
		port:       port,
		portText:   portText,
		ip:         ip,
		address:    net.JoinHostPort(ip.String(), portText),
		tcpNetwork: tcpNetwork,
		udpNetwork: udpNetwork,
	}, nil
}

func defaultPort(scheme string) (string, error) {
	switch scheme {
	case "http":
		return "80", nil
	case "https", "quic":
		return "443", nil
	default:
		return "", fmt.Errorf("unsupported URL scheme %q", scheme)
	}
}

func resolveHostIPs(ctx context.Context, cfg config, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}

	lookupIP := cfg.lookupIP
	if lookupIP == nil {
		lookupIP = net.DefaultResolver.LookupIP
	}

	ips, err := lookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve host %q: no IP addresses", host)
	}
	return ips, nil
}

func selectRemoteIP(ips []net.IP, forceIPv6 bool) (net.IP, error) {
	if forceIPv6 {
		if ip := firstIPv6(ips); ip != nil {
			return ip, nil
		}
		return nil, errors.New("no IPv6 address available")
	}

	if ip := firstIPv4(ips); ip != nil {
		return ip, nil
	}
	if ip := firstIPv6(ips); ip != nil {
		return ip, nil
	}
	return nil, errors.New("no IPv4 or IPv6 address available")
}

func firstIPv4(ips []net.IP) net.IP {
	for _, ip := range ips {
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4
		}
	}
	return nil
}

func firstIPv6(ips []net.IP) net.IP {
	for _, ip := range ips {
		if ip.To4() == nil {
			if ipv6 := ip.To16(); ipv6 != nil {
				return ipv6
			}
		}
	}
	return nil
}

func endpointNetworks(ip net.IP) (string, string) {
	if ip.To4() != nil {
		return "tcp4", "udp4"
	}
	return "tcp6", "udp6"
}

func (t *tcpConnTracker) dialContext(dialer *net.Dialer, endpoint *resolvedEndpoint) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if endpoint != nil {
			network = endpoint.tcpNetwork
			address = endpoint.address
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
	printResultWithOptions(out, res, resultPrintOptions{})
}

func printResultWithOptions(out io.Writer, res result, opts resultPrintOptions) {
	status := res.status
	if status == "" {
		status = "-"
	}

	fmt.Fprintf(out, "port=%d status=%q bytes=%d elapsed=%s",
		res.port, status, res.bytes, formatElapsed(res.elapsed, opts, res.timedOut))
	if res.err != nil && !res.timedOut {
		fmt.Fprintf(out, " error=%q", res.err.Error())
	}
	if res.tcpStats.available {
		printNonZeroUint32(out, "tx_retrans", res.tcpStats.txRetrans)
		printNonZeroUint32(out, "tx_lost_current", res.tcpStats.txLostCurrent)
		printNonZeroUint32(out, "tx_retrans_current", res.tcpStats.txRetransCurrent)
		printNonZeroUint64(out, "tx_retrans_bytes", res.tcpStats.txRetransBytes)
		printNonZeroUint32(out, "dsack_dups", res.tcpStats.dsackDups)
		printNonZeroUint32(out, "rx_ooo", res.tcpStats.rxOOO)
		printNonZeroUint32(out, "rx_reord_seen", res.tcpStats.rxReordSeen)
		printNonZeroUint32(out, "rx_data_segs", res.tcpStats.rxDataSegs)
	}
	if res.quicStats.available {
		printNonZeroUint64(out, "quic_packets_lost", res.quicStats.packetsLost)
		printNonZeroUint64(out, "quic_bytes_lost", res.quicStats.bytesLost)
		printNonZeroUint64(out, "quic_packets_sent", res.quicStats.packetsSent)
		printNonZeroUint64(out, "quic_packets_received", res.quicStats.packetsReceived)
		printNonZeroDuration(out, "quic_latest_rtt", res.quicStats.latestRTT)
		printNonZeroDuration(out, "quic_smoothed_rtt", res.quicStats.smoothedRTT)
	}
	fmt.Fprintln(out)
}

func formatElapsed(elapsed time.Duration, opts resultPrintOptions, timedOut bool) string {
	rounded := elapsed.Round(time.Millisecond)
	suffix := ""
	if timedOut {
		suffix = "+"
	}
	if !opts.colorElapsed {
		return rounded.String() + suffix
	}
	if rounded <= opts.elapsedThreshold {
		return ansiGreen + rounded.String() + ansiReset + suffix
	}
	return ansiRed + rounded.String() + ansiReset + suffix
}

func printResolvedEndpoint(out io.Writer, endpoint *resolvedEndpoint) {
	fmt.Fprintf(out, "target_host=%q target_ip=%s\n", endpoint.host, endpoint.ip)
}

func isTerminal(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func printNonZeroUint32(out io.Writer, name string, value uint32) {
	if value != 0 {
		fmt.Fprintf(out, " %s=%d", name, value)
	}
}

func printNonZeroUint64(out io.Writer, name string, value uint64) {
	if value != 0 {
		fmt.Fprintf(out, " %s=%d", name, value)
	}
}

func printNonZeroDuration(out io.Writer, name string, value time.Duration) {
	rounded := value.Round(time.Millisecond)
	if rounded != 0 {
		fmt.Fprintf(out, " %s=%s", name, rounded)
	}
}
