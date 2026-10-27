# Privacy

Status: current for `v0.1.0-pre` (2026-10-28). This page describes the data the software
stores, how long it keeps it, who can read it, and what it does not do. It is a technical
description, not legal advice.

## Who is the operator

`ajs-accountd` is self-hosted software. The person or organisation running an instance is the
data controller for that instance; the project does not receive a copy of its data. The live
instance at `ajs.dwgx.top` / `gate.dwgx.top` is run by the project Owner and is the only
instance whose posture this page describes.

## What is collected

Everything below lives in the operator's state directory or nginx logs. Nothing is sent to the
project.

| Data | Where | Contents | Why |
|---|---|---|---|
| Panel records | `state.json` | users (id, name, platform, quota, allowlist), accounts (ids, labels, regions, entitlement fields, vendor server catalog), leases (user, account, node, port, TTL), API-key metadata, scoped-key hashes, settings, schedules, webhook registry | operate the service |
| Sealed secrets | `secrets.json`, `device-secrets.json` | account passwords sealed with AES-256-GCM; device identities sealed under an HKDF-derived key | log in to vendor accounts; keep device identity stable |
| Request log | `state.json`, capped at **10,000 rows** | timestamp, user id/name, account label, node label, region, method, protocol, target host, status, bytes, latency, source | usage display and quota accounting |
| Usage counters | `state.json` | per-user monthly bytes and request counts; windowed aggregates up to 366 days | enforce quotas |
| Audit history | `state.json`, capped at **1,000 events** | actor, action, target, short detail, small credential-free metadata | accountability for operator actions |
| Event ring | memory, capped at **500 events** | lease/account/webhook events for replay and SSE | customer-visible events |
| Lab log ring | memory, capped at **2,000 entries** | redacted daemon log lines for the admin Lab view | diagnostics |
| Backups | operator-chosen path | one passphrase-encrypted `.ajsb` envelope of the three state files | restore |
| nginx access logs | host log path | method, path **including query string**, status, bytes, timings, user agent; the API log has no request headers | security and capacity |

Not collected: no analytics, no telemetry, no crash reporting, no advertising identifiers, no
card data. The daemon never stores a card number and never sends a payment request.

The caps above are constants in the code, not hand-maintained prose:

```bash
grep -n 'AuditRetention\|RequestRetention' daemon/internal/store/store.go
grep -n 'DefaultCapacity = ' daemon/internal/events/events.go daemon/internal/lablog/ring.go
grep -n 'MaxWindowDays' daemon/internal/usage/usage.go
```

## Retention

- **Bounded in-process histories** drop the oldest entry on insert: request log 10,000 rows,
  audit 1,000 events, event ring 500, lab log ring 2,000. There is no time-based expiry for
  these; they are size-bounded.
- **Usage windows** are capped at 366 days per query. `truncated: true` means the 10,000-row
  request cap clipped the window, not that older usage was deleted from the counters.
- **State files** persist until the operator deletes them or restores a backup. There is no
  automatic purge job. Deleting `state.json`/`secrets.json`/`device-secrets.json` while the
  daemon is stopped removes the instance's records; back them up first if that is not intended.
- **Backups** are retained wherever the operator puts them; the daemon does not expire them.
- **nginx logs** follow the host's logrotate policy, which this repository does not manage.
- **Gate stream logging is off** on the live edge, and `Proxy-Authorization` is never logged by
  nginx. The daemon's own request rows record the target host and byte counts, not payloads.

## Who can read it

| Reader | Sees | Controlled by |
|---|---|---|
| `admin` operator | everything, including account records, audit, requests, backups | operator identity + strict RBAC in the kit; the live host runs compatibility mode (below) |
| `operator` | ordinary reads, connections, leases, schedules; not accounts, keys, users or backups | role matrix in [`docs/RBAC.md`](RBAC.md) |
| `viewer` | ordinary dashboard reads, metrics, logs, requests, audit | role matrix |
| Scoped key holder | its own user's leases, usage, requests and events; never another user's rows | key subject + `accountIds`/`regions` narrowing |
| Customer portal user | its own orders/leases when the portal is enabled | portal cookie; **portal off in this release** |
| Host root / local file reader | state, master key, operator hashes | Unix permissions; outside the application boundary |

On the live host, strict RBAC is not enabled: `AJS_RBAC_STRICT` is unset, so compatibility mode
still accepts the legacy shared bearer, and the daemon still runs as root. The reach of that
API is narrower than it was at the 2026-10-27 cutover: `ajs-accountd` binds `127.0.0.1:2261`,
and the HMC internal vhost in front of it accepts only the VPS WireGuard peer (`10.99.0.1`) and
`127.0.0.1`, so a LAN client gets `403` on `/v1/*` and cannot read any of this data through the
API. What remains LAN-facing is the dashboard on `:18082`; its two SSE routes inject a
server-side admin bearer and are limited to source addresses `127.0.0.1` and `192.168.11.2`, so
a process on that workstation can follow those streams without a separate login. See
[`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md#known-limitations).

## Outbound contacts

- **Vendor hosts**: only when `AJS_ACCOUNT_LOGIN=1` (login, control API, FV data). Off by
  default; the default build contacts no vendor host.
- **Renewal plans**: the daemon caches the vendor's public purchase page; no account data is
  sent.
- **Resin**: only when `AJS_RESIN_ADMIN_TOKEN` is set, to `127.0.0.1:2260` by default.
- **Webhooks**: only to receivers the operator configures, signed with an operator secret.
- **Payments**: only to a configured epay gateway or from a configured Stripe webhook;
  experimental and off in this release.

There is no other phone-home path in the daemon.

## Sale and sharing

The project does not sell, rent or share instance data. There is no third-party analytics or
advertising integration in the code. An operator may of course export data through the admin
API (`/v1/accounts/export`, `/v1/audit/export.csv`, backups); that is the operator's decision
and responsibility.

## Secrets

Credentials are not written to logs: the API access log carries no headers, gate stream logging
is off, and audit metadata is credential-free. Sealed account passwords are unreadable without
`AJS_MASTER_KEY`; losing that key makes them unrecoverable. Rotate any credential you believe
was exposed; see [`docs/OPERATIONS.md`](OPERATIONS.md#key-and-credential-rotation).

## Contact

Privacy questions about the live instance go to its operator. Security reports follow
[`SECURITY.md`](../SECURITY.md). Do not paste credentials, state files or backup envelopes into
an issue.
