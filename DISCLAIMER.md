# Disclaimer

**AIJIASU2API is an educational and research project.** It is a protocol corpus and a reference
implementation for interoperability study. It is not a product, not a service offering, and not
legal advice.

## No affiliation

This project is not affiliated with, authorised by, sponsored by, or endorsed by 爱加速 / AJiaSu,
its operator, or any other vendor. Vendor names and marks are used only to identify the protocol
under study and remain the property of their owners. Nothing in this repository grants any right
to a vendor's software, service, data, accounts, or marks.

## No vendor material

No vendor binaries, installers, drivers, packet captures, memory dumps, account credentials, or
derived secrets are committed to this repository. `.gitignore` blocks the common artefact types at
the repository root; retained research captures are synthetic or redacted. If you believe a file
violates this rule, report it privately as described in [SECURITY.md](SECURITY.md).

## Authorised accounts only

Use this software only with accounts you own or are explicitly authorised to use. You are
responsible for the vendor's terms of service and for the law where you operate. Do not use this
project to bypass account limits, resell access without written vendor authorisation, or hide the
origin of abusive traffic. The boundary is restated in [Use boundary](README.md#use-boundary).

## The live demo

The public instance at `https://ajs.dwgx.top:2055` and the HTTPS CONNECT edge at
`gate.dwgx.top` are **operator-run and invite-only**. Access requires an `ajs_sk_` scoped key
issued by the operator; there is no signup, no self-service key issuance, no SLA, no uptime
promise, and no resale. The demo may be withdrawn, restricted, or revoked at any time, and keys
may be revoked without notice. It demonstrates the code in this repository; it is not a
commercial proxy service.

The public edge offers **HTTPS CONNECT only**. There is no public SOCKS5 ingress; SOCKS5 gates
bind loopback.

## Payments and tenant surfaces

The 易支付 (epay), Stripe, orders, and tenant-portal paths in this tree are **experimental,
AI-generated research paths**. In `v0.1.0-pre` they are disabled and are not part of the public
edge. They are not a payment service: this project does not charge a card, transfer money, or
offer support for them. Payment and order code does exist in the repository; "disabled" describes
the release surface, not the absence of code.

## No warranty

This repository is licensed under the MIT licence (see [LICENSE](LICENSE)). It is provided "as
is", without warranty of any kind, express or implied, including fitness for a particular purpose
or non-infringement. The authors are not liable for any claim, damage, or other liability arising
from use of the software or of any instance operated by someone else. The MIT licence grants
rights to this repository's code only — not to any vendor's software, service, data, or marks.

## Security reports

Do not open a public issue for a vulnerability. Report privately as described in
[SECURITY.md](SECURITY.md). First response is a best effort within 72 hours; there is no bounty.

## Jurisdiction

Reverse engineering is restricted by law in some jurisdictions, and contract terms may restrict
it further. Check both before you use, build, or deploy anything here. This document is not legal
advice.
