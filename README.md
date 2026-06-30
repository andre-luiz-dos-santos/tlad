# TLAD

TLAD is a Go command-line tool for making sequential HTTP, HTTPS, or QUIC range download requests with a configured local source port sequence.

It is useful when you need to test how repeated downloads behave across specific local ports while keeping each request small and paced.

## Features

- Download from HTTP, HTTPS, and HTTP/3-over-QUIC URLs.
- Limit bytes read per request with `-bytes`.
- Bind each request to a predictable local TCP or UDP source port sequence with `-start-port`, `-count`, and `-step`.
- Resolve the target host once at startup, reusing the selected server IP and port across all requests.
- Select an IPv6 target address with `-ipv6`; by default TLAD prefers IPv4 and falls back to IPv6 only when no IPv4 address is available.
- Pace request attempts with `-min-interval`.
- Retry HTTP `429 Too Many Requests` responses after `10 * min-interval` using the same local port.
- Report local TCP_INFO counters for TCP retransmit state and receive-side reordering signals when supported.
- Report QUIC packet loss and RTT counters for `quic://` downloads.

## Requirements

- Go 1.25 or newer

## Build

```sh
make build
```

Or build directly with Go:

```sh
go build -o tlad .
```

## Usage

```sh
./tlad -url https://example.com/file
```

### Flags

| Flag | Default | Description |
| --- | ---: | --- |
| `-url` | required | HTTP, HTTPS, or QUIC URL to download. |
| `-bytes` | `262144` | Maximum number of bytes to read per request. |
| `-start-port` | `61000` | First local TCP or UDP source port. |
| `-count` | `100` | Number of sequential requests to make. |
| `-step` | `1` | Local port increment between requests. |
| `-timeout` | `30s` | Per-request timeout. |
| `-min-interval` | `2s` | Minimum time between request attempts. |
| `-elapsed-threshold` | `5s` | Elapsed time color threshold for terminal output. |
| `-ipv6` | `false` | Select an IPv6 address; by default TLAD prefers IPv4 and falls back to IPv6 only if no IPv4 address is available. |

## Example Output

```text
port=61000 status="206 Partial Content" bytes=262144 elapsed=1.421s rx_data_segs=182
port=61001 status="206 Partial Content" bytes=262144 elapsed=863ms rx_data_segs=181
port=61002 status="206 Partial Content" bytes=163555 elapsed=30.001s+ tx_retrans=2 tx_retrans_bytes=96 rx_ooo=1 rx_reord_seen=1 rx_data_segs=114
```

When output is written to a terminal, the elapsed value is shown in green when it is at or below `-elapsed-threshold` and red when it is above the threshold. Redirected or piped output remains plain text.
When a request reaches `-timeout`, the elapsed value gets a trailing `+` to show the download was incomplete without treating it as an error.

The TCP fields are read from the local TCP stack when supported. On Linux, `tx_retrans` is `TCP_INFO.tcpi_total_retrans`, `tx_lost_current` is `TCP_INFO.tcpi_lost`, `tx_retrans_current` is `TCP_INFO.tcpi_retrans`, `tx_retrans_bytes` is `TCP_INFO.tcpi_bytes_retrans`, `dsack_dups` is `TCP_INFO.tcpi_dsack_dups`, `rx_ooo` is `TCP_INFO.tcpi_rcv_ooopack`, `rx_reord_seen` is `TCP_INFO.tcpi_reord_seen`, and `rx_data_segs` is `TCP_INFO.tcpi_data_segs_in`.

For HTTP and HTTPS downloads, the local process is mostly receiving data, so transmit-side loss and retransmit counters can stay at zero even when server-to-client packets were lost and retransmitted by the server. Zero-valued TCP and QUIC stat fields are omitted from output. Linux TCP_INFO does not expose an exact receiver-side packet-loss total for the socket. `rx_ooo`, `rx_reord_seen`, and `rx_data_segs` are receive-side signals that can help identify loss or reordering, but they are not exact remote packet-loss counters. More exact downstream TCP loss evidence requires packet-capture inference from repeated TCP sequence ranges, or sender-side stats from a server you control. If TCP_INFO cannot be read, the TCP fields are omitted.

Use `quic://host/path` to make the request over HTTP/3. The client accepts any TLS certificate for HTTPS and QUIC downloads. QUIC output includes counters such as `quic_packets_lost`, `quic_bytes_lost`, `quic_packets_sent`, `quic_packets_received`, `quic_latest_rtt`, and `quic_smoothed_rtt`.

## Exit Codes

| Code | Meaning |
| ---: | --- |
| `0` | Success or help output. |
| `1` | One or more downloads failed. |
| `2` | Invalid CLI configuration. |
