# TLAD

TLAD is a Go command-line tool for making sequential HTTP or HTTPS range download requests with a configured local TCP source port sequence.

It is useful when you need to test how repeated downloads behave across specific local ports while keeping each request small and paced.

## Features

- Download from HTTP and HTTPS URLs.
- Limit bytes read per request with `-bytes`.
- Bind each request to a predictable local source port sequence with `-start-port`, `-count`, and `-step`.
- Pace request attempts with `-min-interval`.
- Retry HTTP `429 Too Many Requests` responses after `10 * min-interval` using the same local port.
- Report local TCP_INFO counters for retransmits and out-of-order received packets when supported.

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
| `-url` | required | HTTP or HTTPS URL to download. |
| `-bytes` | `262144` | Maximum number of bytes to read per request. |
| `-start-port` | `40000` | First local TCP source port. |
| `-count` | `100` | Number of sequential requests to make. |
| `-step` | `1` | Local port increment between requests. |
| `-timeout` | `30s` | Per-request timeout. |
| `-min-interval` | `1s` | Minimum time between request attempts. |

## Example Output

```text
port=40000 status="206 Partial Content" bytes=262144 elapsed=1.421s tx_retrans=0 rx_ooo=0
port=40001 status="206 Partial Content" bytes=262144 elapsed=863ms tx_retrans=0 rx_ooo=0
port=40002 status="206 Partial Content" bytes=163555 elapsed=30.001s error="context deadline exceeded" tx_retrans=2 rx_ooo=1
```

The TCP fields are read from the local TCP stack when supported. On Linux, `tx_retrans` is `TCP_INFO.tcpi_total_retrans`, and `rx_ooo` is `TCP_INFO.tcpi_rcv_ooopack` for out-of-order received packets. `rx_ooo` is a receive-side signal that can indicate server-to-client loss or reordering, not an exact remote packet-loss counter. If TCP_INFO cannot be read, the TCP fields are omitted.

## Exit Codes

| Code | Meaning |
| ---: | --- |
| `0` | Success or help output. |
| `1` | One or more downloads failed. |
| `2` | Invalid CLI configuration. |
