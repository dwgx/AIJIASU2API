# scripts/m1 — M1 spike harness

A minimal SOCKS5 server used to prove the Resin integration assumptions without a vendor
account. Stdlib only; no credentials; loopback-only by design.

## Build and test

```sh
cd scripts/m1/dummy-socks
go vet ./...
go test ./...
go build -o dummy-socks ./...
./dummy-socks --self-test
```

`--self-test` starts an in-process echo target, a direct SOCKS5 server as a fake upstream, and
an upstream-mode node, then verifies direct, upstream, and domain-target round trips:

```
PASS mode=direct proxy=127.0.0.1:33989 target=127.0.0.1:36153 rtt_ms=0
PASS mode=upstream proxy=127.0.0.1:37001 target=127.0.0.1:36153 rtt_ms=0
PASS mode=upstream-domain proxy=127.0.0.1:37001 target=localhost:36153 rtt_ms=0
SELFTEST PASS
```

## Run

Two nodes with different egress paths:

```sh
# direct: dials targets from the host's default route
./dummy-socks --listen 127.0.0.1:12001 --mode direct --name m1-a

# upstream: relays through another no-auth SOCKS5 server
./dummy-socks --listen 127.0.0.1:12002 --mode upstream --upstream 127.0.0.1:2081 --name m1-b
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `--listen` | `127.0.0.1:12001` | SOCKS5 listen address |
| `--mode` | `direct` | `direct` or `upstream` |
| `--upstream` | — | upstream SOCKS5 address (required for `upstream`) |
| `--name` | `dummy-socks` | label used in log lines |
| `--self-test` | `false` | run the in-process test and exit |

The server implements SOCKS5 no-auth CONNECT only (IPv4, IPv6, domain targets), relays bytes
bidirectionally with half-close, and logs one line per connection and per completed relay.

## Egress check

```sh
curl -sS --socks5-hostname 127.0.0.1:12001 https://cloudflare.com/cdn-cgi/trace | grep '^ip='
curl -sS --socks5-hostname 127.0.0.1:12002 https://cloudflare.com/cdn-cgi/trace | grep '^ip='
```

On HMC both the host default route and sing-box inbound `127.0.0.1:2080` exit the same VPS, so
node `m1-b` uses the second inbound `127.0.0.1:2081` (homelane) to obtain a distinct egress IP.
