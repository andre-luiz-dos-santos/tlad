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
	"time"
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

	tlsConfig *tls.Config
}

type result struct {
	port       int
	statusCode int
	status     string
	bytes      int64
	elapsed    time.Duration
	err        error
}

type requestPacer struct {
	minInterval time.Duration
	lastStart   time.Time
}

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
	fs.StringVar(&cfg.url, "url", "", "http or https URL to download")
	fs.Int64Var(&cfg.bytes, "bytes", defaultBytes, "maximum number of bytes to read per request")
	fs.IntVar(&cfg.startPort, "start-port", defaultStartPort, "first local TCP source port")
	fs.IntVar(&cfg.count, "count", defaultCount, "number of sequential requests to make")
	fs.IntVar(&cfg.step, "step", defaultStep, "local port increment between requests")
	fs.DurationVar(&cfg.timeout, "timeout", defaultTimeout, "per-request timeout")
	fs.DurationVar(&cfg.minInterval, "min-interval", defaultMinInterval, "minimum time between request attempts")
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
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q: only http and https are supported", parsed.Scheme)
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
	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{
			Port: localPort,
		},
	}
	transport := &http.Transport{
		DialContext:     dialer.DialContext,
		TLSClientConfig: cfg.tlsConfig,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport}
	for {
		res := downloadAttempt(ctx, cfg, localPort, client, pacer)
		if res.statusCode != http.StatusTooManyRequests || cfg.minInterval <= 0 {
			return res
		}
		if err := sleepContext(ctx, 10*cfg.minInterval); err != nil {
			res.err = fmt.Errorf("429 retry wait canceled: %w", err)
			return res
		}
	}
}

func downloadAttempt(ctx context.Context, cfg config, localPort int, client *http.Client, pacer *requestPacer) result {
	start := time.Now()
	res := result{port: localPort}

	var err error
	start, err = pacer.wait(ctx)
	if err != nil {
		res.elapsed = time.Since(start)
		res.err = err
		return res
	}

	reqCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cfg.url, nil)
	if err != nil {
		res.elapsed = time.Since(start)
		res.err = err
		return res
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", cfg.bytes-1))

	resp, err := client.Do(req)
	if err != nil {
		res.elapsed = time.Since(start)
		res.err = err
		return res
	}
	defer resp.Body.Close()

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
		return res
	}
	if resp.StatusCode >= http.StatusBadRequest {
		res.err = fmt.Errorf("server returned %s", resp.Status)
	}
	return res
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
	errText := "-"
	if res.err != nil {
		errText = res.err.Error()
	}
	status := res.status
	if status == "" {
		status = "-"
	}
	fmt.Fprintf(out, "port=%d status=%q bytes=%d elapsed=%s error=%q\n",
		res.port, status, res.bytes, res.elapsed.Round(time.Millisecond), errText)
}
