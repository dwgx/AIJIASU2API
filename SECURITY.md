# Security Policy

> **TL;DR** — do not open a public issue. Report privately through the repository's
> **Security → Report a vulnerability** channel (GitHub Security Advisories), or contact the
> owner profile named in [GOVERNANCE.md](GOVERNANCE.md). First response is a best effort within
> 72 hours. There is no bounty.

The 2026-10-24 audit at [research/83-audit-bench/SECURITY.md](research/83-audit-bench/SECURITY.md)
is a research record with a point-in-time verdict. It is not this policy and not a disclosure
channel.

## Supported versions

| Version | Supported |
|---|---|
| `v0.1.0-pre` | Yes — pre-release, best effort |
| Earlier local tags (`v0.1.0-archive` … `v0.7.0-live`) | No — milestone checkpoints, not releases |
| Forks and self-hosted instances | Maintained by their operators |

`v0.1.0-pre` is a pre-release. There is no long-term-support branch and no backport promise.
Self-hosted deployments are the operator's responsibility; if you run one, you own its patching,
network exposure, and credential handling.

## In scope

- Authentication or authorisation bypass on the panel API (`/v1/*`), including RBAC, CSRF, and
  `Origin` checks.
- Scoped-key isolation failures: a key reading or mutating data outside its `userId`,
  `accountIds`, `regions`, or scopes.
- Key and lease lifecycle failures: revocation or expiry that does not stop new handshakes, or
  that leaves an entitlement usable when it should not be.
- Credential exposure: sealed-store plaintext, backup blobs, logs, error envelopes, or support
  bundles that reveal a secret.
- Gate admission bypass: SOCKS5 or HTTP CONNECT authentication, lease binding, allowlists, quota,
  or flow metering.
- SSRF through webhooks; path traversal in backup or restore.
- TLS listener misconfiguration that exposes the admin surface or weakens authentication.

## Out of scope

- The vendor suspending, rate-limiting, or banning an account. That is account management, not a
  vulnerability in this code.
- Findings that require the operator's live key, the HMC host, or a vendor account to reproduce.
  Describe them; expect a slower answer and a possible "cannot reproduce".
- Surfaces this release declares disabled: the tenant portal (`AJS_TENANT_PORTAL`), orders, the
  epay/Stripe adapters, and the removed remote-ingress credential store. See
  [README.md](README.md#experimental-and-disabled-surfaces).
- Social engineering, physical access, denial of service by volume against the operator-run demo,
  and issues in third-party services we do not control.
- Missing hardening that has no demonstrated impact (scanner output alone, version banners,
  best-practice headers without an exploit path).

## Reporting

Use the repository's **Security → Report a vulnerability** channel. If the repository page is not
available, contact the owner profile named in [GOVERNANCE.md](GOVERNANCE.md). Do not open a public
issue, post a live key, or publish an exploit before a fix is available.

Please include:

- The affected commit (`git rev-parse HEAD`) or release tag.
- The listener and auth shape: admin bearer, `ajs_sk_` scoped key, gate userpass, portal cookie,
  or unauthenticated.
- A minimal reproduction, with every credential redacted. Never paste a live key, account
  password, session cookie, or hash of one.
- Impact: what an attacker gains, and what preconditions they need.

## Process

1. We acknowledge within 72 hours, best effort.
2. We triage, reproduce on a local instance, and agree a disclosure window with you.
3. We fix, add a regression test, and publish an advisory when a fix is available.
4. We credit reporters who want credit. There is no bug bounty.

Do not test the operator-run demo without written permission. A local instance is the expected
target for research; see [README.md](README.md#quick-start).
