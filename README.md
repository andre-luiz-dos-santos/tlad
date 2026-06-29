# TLAD

TLAD is a Go command-line tool for making sequential HTTP or HTTPS range download requests with a configured local TCP source port sequence.

It is useful when you need to test how repeated downloads behave across specific local ports while keeping each request small and paced.

## Features

- Download from HTTP and HTTPS URLs.
- Limit bytes read per request with `-bytes`.
- Bind each request to a predictable local source port sequence with `-start-port`, `-count`, and `-step`.
- Pace request attempts with `-min-interval`.
- Retry HTTP `429 Too Many Requests` responses after `10 * min-interval` using the same local port.

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
port=40000 status="206 Partial Content" bytes=262144 elapsed=1.421s error="-"
port=40001 status="206 Partial Content" bytes=262144 elapsed=863ms error="-"
port=40002 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40003 status="206 Partial Content" bytes=262144 elapsed=991ms error="-"
port=40004 status="206 Partial Content" bytes=262144 elapsed=982ms error="-"
port=40005 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40006 status="206 Partial Content" bytes=163555 elapsed=30.001s error="context deadline exceeded"
port=40007 status="206 Partial Content" bytes=262144 elapsed=989ms error="-"
port=40008 status="206 Partial Content" bytes=262144 elapsed=998ms error="-"
port=40009 status="206 Partial Content" bytes=147171 elapsed=30.001s error="context deadline exceeded"
port=40010 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40011 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40012 status="206 Partial Content" bytes=262144 elapsed=988ms error="-"
port=40013 status="206 Partial Content" bytes=262144 elapsed=990ms error="-"
port=40014 status="206 Partial Content" bytes=262144 elapsed=986ms error="-"
port=40015 status="206 Partial Content" bytes=262144 elapsed=989ms error="-"
port=40016 status="206 Partial Content" bytes=262144 elapsed=866ms error="-"
port=40017 status="206 Partial Content" bytes=262144 elapsed=991ms error="-"
port=40018 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40019 status="206 Partial Content" bytes=262144 elapsed=873ms error="-"
port=40020 status="206 Partial Content" bytes=262144 elapsed=984ms error="-"
port=40021 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40022 status="206 Partial Content" bytes=262144 elapsed=1.108s error="-"
port=40023 status="206 Partial Content" bytes=262144 elapsed=868ms error="-"
port=40024 status="206 Partial Content" bytes=262144 elapsed=982ms error="-"
port=40025 status="206 Partial Content" bytes=262144 elapsed=984ms error="-"
port=40026 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40027 status="206 Partial Content" bytes=262144 elapsed=992ms error="-"
port=40028 status="206 Partial Content" bytes=262144 elapsed=984ms error="-"
port=40029 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40030 status="206 Partial Content" bytes=262144 elapsed=1.107s error="-"
port=40031 status="206 Partial Content" bytes=262144 elapsed=861ms error="-"
port=40032 status="206 Partial Content" bytes=262144 elapsed=994ms error="-"
port=40033 status="206 Partial Content" bytes=262144 elapsed=859ms error="-"
port=40034 status="206 Partial Content" bytes=262144 elapsed=862ms error="-"
port=40035 status="206 Partial Content" bytes=262144 elapsed=865ms error="-"
port=40036 status="206 Partial Content" bytes=262144 elapsed=871ms error="-"
port=40037 status="206 Partial Content" bytes=262144 elapsed=990ms error="-"
port=40038 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40039 status="206 Partial Content" bytes=262144 elapsed=984ms error="-"
port=40040 status="206 Partial Content" bytes=262144 elapsed=982ms error="-"
port=40041 status="206 Partial Content" bytes=262144 elapsed=983ms error="-"
port=40042 status="206 Partial Content" bytes=262144 elapsed=982ms error="-"
port=40043 status="206 Partial Content" bytes=262144 elapsed=992ms error="-"
port=40044 status="206 Partial Content" bytes=262144 elapsed=867ms error="-"
port=40045 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40046 status="206 Partial Content" bytes=262144 elapsed=994ms error="-"
port=40047 status="206 Partial Content" bytes=262144 elapsed=983ms error="-"
port=40048 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40049 status="206 Partial Content" bytes=196323 elapsed=30.001s error="context deadline exceeded"
port=40050 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40051 status="206 Partial Content" bytes=262144 elapsed=989ms error="-"
port=40052 status="206 Partial Content" bytes=262144 elapsed=987ms error="-"
port=40053 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40054 status="206 Partial Content" bytes=262144 elapsed=984ms error="-"
port=40055 status="206 Partial Content" bytes=262144 elapsed=867ms error="-"
port=40056 status="206 Partial Content" bytes=262144 elapsed=913ms error="-"
port=40057 status="206 Partial Content" bytes=262144 elapsed=868ms error="-"
port=40058 status="206 Partial Content" bytes=262144 elapsed=911ms error="-"
port=40059 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40060 status="206 Partial Content" bytes=163555 elapsed=30.001s error="context deadline exceeded"
port=40061 status="206 Partial Content" bytes=262144 elapsed=873ms error="-"
port=40062 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40063 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40064 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40065 status="206 Partial Content" bytes=262144 elapsed=1.108s error="-"
port=40066 status="206 Partial Content" bytes=262144 elapsed=865ms error="-"
port=40067 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40068 status="206 Partial Content" bytes=262144 elapsed=994ms error="-"
port=40069 status="206 Partial Content" bytes=262144 elapsed=867ms error="-"
port=40070 status="206 Partial Content" bytes=262144 elapsed=986ms error="-"
port=40071 status="206 Partial Content" bytes=262144 elapsed=867ms error="-"
port=40072 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40073 status="206 Partial Content" bytes=262144 elapsed=857ms error="-"
port=40074 status="206 Partial Content" bytes=262144 elapsed=872ms error="-"
port=40075 status="206 Partial Content" bytes=262144 elapsed=983ms error="-"
port=40076 status="206 Partial Content" bytes=262144 elapsed=873ms error="-"
port=40077 status="206 Partial Content" bytes=212707 elapsed=30.001s error="context deadline exceeded"
port=40078 status="206 Partial Content" bytes=262144 elapsed=991ms error="-"
port=40079 status="206 Partial Content" bytes=262144 elapsed=873ms error="-"
port=40080 status="206 Partial Content" bytes=262144 elapsed=989ms error="-"
port=40081 status="206 Partial Content" bytes=262144 elapsed=867ms error="-"
port=40082 status="206 Partial Content" bytes=262144 elapsed=993ms error="-"
port=40083 status="206 Partial Content" bytes=196323 elapsed=30s error="context deadline exceeded"
port=40084 status="206 Partial Content" bytes=262144 elapsed=982ms error="-"
port=40085 status="206 Partial Content" bytes=262144 elapsed=868ms error="-"
port=40086 status="206 Partial Content" bytes=262144 elapsed=986ms error="-"
port=40087 status="206 Partial Content" bytes=262144 elapsed=983ms error="-"
port=40088 status="206 Partial Content" bytes=262144 elapsed=987ms error="-"
port=40089 status="206 Partial Content" bytes=262144 elapsed=866ms error="-"
port=40090 status="206 Partial Content" bytes=262144 elapsed=916ms error="-"
port=40091 status="206 Partial Content" bytes=262144 elapsed=865ms error="-"
port=40092 status="206 Partial Content" bytes=262144 elapsed=981ms error="-"
port=40093 status="206 Partial Content" bytes=262144 elapsed=985ms error="-"
port=40094 status="206 Partial Content" bytes=262144 elapsed=865ms error="-"
port=40095 status="206 Partial Content" bytes=262144 elapsed=864ms error="-"
port=40096 status="206 Partial Content" bytes=262144 elapsed=863ms error="-"
port=40097 status="206 Partial Content" bytes=262144 elapsed=908ms error="-"
port=40098 status="206 Partial Content" bytes=262144 elapsed=872ms error="-"
port=40099 status="206 Partial Content" bytes=262144 elapsed=982ms error="-"
```

## Exit Codes

| Code | Meaning |
| ---: | --- |
| `0` | Success or help output. |
| `1` | One or more downloads failed. |
| `2` | Invalid CLI configuration. |
