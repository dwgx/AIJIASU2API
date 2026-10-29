# Protocol summary

Status: current for `v0.1.0-pre.2` (release prep, 2026-10-29; tag not yet pushed). Audience:
operators and integrators who need to know what moves over the wire and what the transport
actually protects. This is a summary, not a specification; the research record is
[`research/40-protocol/PROTOCOL.md`](../research/40-protocol/PROTOCOL.md).

> **Protocol obfuscation is not encryption.** The vendor transport wraps its payload in
> AES-128-CTR with keys and IVs that are partly sent in the clear, reuses one key stream across
> both directions of a flow, and carries no authentication tag. Treat the carrier as obfuscated
> bytes on an untrusted network, not as a confidential channel. Do not send anything through it
> that you would not send over plain HTTP, and do not describe it as a VPN or as end-to-end
> encryption.

This page contains no credentials, no vendor binaries or capture files, and no procedure for
intercepting or replaying vendor traffic. The research record holds the field-level detail and
its evidence labels.

## Three flows

```text
client ── HTTPS ──> control API (/client.php)          login, server list, connect/switch
   │
   └── TCP (no TLS) ──> FV endpoint                    session auth, then one TCP
                          │                             connection per proxied flow
                          └── AES-128-CTR payload ──> SOCKS5 CONNECT + relayed bytes
```

1. **Control API.** JSON requests to a single script path on the vendor's HTTPS hosts. Each
   request carries `X-Sig` and `X-Timestamp`; the signature covers the exact query string and a
   millisecond timestamp, and is built from a static secret embedded in the shipping clients.
   The hash behind it is a non-cryptographic 32-bit accumulator pair, so the signature is an
   anti-tamper token, not a MAC. OpenAJS, the reference client this study is based on, disables
   TLS certificate verification, so transport confidentiality there depends on the path, not on a
   verified peer.
2. **FV session.** A plain TCP connection (no TLS, no HTTP) to the endpoint named by the selected
   vendor server. The client sends a `key=value` authentication body encrypted with a fresh
   AES-128 session key; the session key itself is wrapped with RSA-OAEP. The header IV is sent in
   clear text as a 16-byte prefix. The server answers with a length-prefixed, CTR-encrypted text
   map and half-closes; the client requires `r=ok` and stores the returned session id (`sid`).
3. **Data channel.** A **new** TCP connection to the same endpoint for each proxied flow — no
   multiplexing was observed. The client sends a short frame naming the session id, then an
   AES-128-CTR-encrypted SOCKS5 `CONNECT` request (domain targets only in the reference client).
   The client reads a 10-byte SOCKS5 reply, then the socket becomes a byte relay in both
   directions under the same key and IV.

The offline emulator in [`emulator/`](../emulator/README.md) models step 2 and step 3 for tests.
It never contacts a vendor host and is never deployed.

## What is obfuscated, what is encrypted

| Element | Protection | Consequence |
|---|---|---|
| Control API request body | HTTPS, if the client verifies the peer | the reference client does not; a MITM is possible there |
| Control API signature | static-secret 8-byte hash over body + timestamp | proves the sender knew the embedded secret; not a MAC, and no replay rule is specified — the timestamp is the only freshness field |
| FV auth body | AES-128-CTR under a per-connection session key | payload bytes are encrypted, but the header IV travels in clear text |
| Session key | RSA-OAEP with a long-lived wrapping key | no forward secrecy: one compromised wrapping key exposes every captured session key |
| FV frame header | AES-128-CTR with the clear-text header IV | the IV prefix and frame length are visible |
| Proxied bytes | AES-128-CTR, same key/IV pair in both directions | key-stream reuse lets an on-path observer XOR the two directions and crib-drag known headers |
| Metadata | none | endpoint addresses, flow count, timing, sizes, and the fact that this is an FV carrier are all visible |
| Integrity | none | there is no tag or MAC; corruption and tampering look alike |

Two further weaknesses are recorded in the research file and matter to anyone reasoning about
exposure: the first bytes of each direction reuse the same key-stream positions, and the
emulator's auth body and auth map both start at CTR offset 0. The data-channel reuse was
observed against the vendor; the auth-map reuse is an emulator assumption
(`research/40-protocol/PROTOCOL.md`, "Confidentiality of the carrier").

## Sessions and leases

Two different objects share the word "session"; keep them apart:

| Object | Owner | Meaning |
|---|---|---|
| Vendor session | the vendor | one authenticated FV data session per account, identified by `sid`; a second concurrent session for the same account is refused at the vendor, and multi-slot coexistence is unproven |
| Lease | this project | a control-plane record binding one user, one account, one node and one gate port, with a TTL; the gate re-checks it at `CONNECT` and refuses a revoked or expired lease |

A lease is not a vendor session and does not create one. It is the entitlement the local gate
checks before it opens an FV flow, and the port in `connection.port` moves when a switch rebinds
the lease. The gate keeps only the lease id in its flow records; the scoped key is never stored.
See [`docs/API-GUIDE.md`](API-GUIDE.md#proxy-data-plane-loopback-socks5-public-https-connect) for
the operator-facing view.

## Scope

The material summarised here exists to study interoperability with accounts you are authorised
to use. It is not a bypass, a traffic-hiding service, or a way around vendor limits. The legal
and use constraints are in [`DISCLAIMER.md`](../DISCLAIMER.md) and
[`README.md`](../README.md#use-boundary).

## Pointers

- [`research/40-protocol/PROTOCOL.md`](../research/40-protocol/PROTOCOL.md) — full research
  record, evidence labels, signature vectors, error cases
- [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) — where the gate and the control plane run
- [`docs/API-GUIDE.md`](API-GUIDE.md) — leases, scoped keys, the public CONNECT edge
- [`emulator/README.md`](../emulator/README.md) — the offline FV model
