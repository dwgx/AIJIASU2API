# LovePlusSpeedToProxy

Reverse-engineering research repository for the **AJiaSu / 爱加速** client, with the goal of
reimplementing its protocol so the service can be driven as a **proxy API with a selectable
egress IP**.

## What this repository is

A research corpus. It records what is known about the AJiaSu client — its installer payload,
its Windows and Linux binaries, its `FV*` protocol stack, its login/node-selection flow — and
the plan for a native reimplementation of that protocol.

It is **not** a client, not a proxy, and not a distribution of vendor software. No vendor
binaries, drivers, installers, packet captures, or memory dumps are committed here; `.gitignore`
blocks them at the repository root.

## Current status

**Research only — pre-implementation.**

As of 2026-10-23 the repository contains the campaign plan and the static-analysis baseline:
OSINT, Windows static inventory, Linux static analysis, and a language-neutral FV protocol
specification. No code that logs in, connects, or proxies exists yet. Nothing in this repository
has been validated against a live service.

## Scope and tiers

- **Tier 1** — orchestrate the official Linux client as a black box and expose its egress as a
  proxy. Fast, low protocol risk, prior art exists.
- **Tier 2** — native reimplementation of the `FV` protocol (login, node list, tunnel), running
  without any vendor binary. This is the north star.

The Windows kernel component shipped with the client is the stock OpenVPN TAP-Windows driver
(`tap0901`); it is the system-wide TUN path only, and a SOCKS5/HTTP proxy API does not need it.

## Legal and terms-of-service caution

This work is reverse engineering for interoperability research. Before any use of the findings:

- The vendor's terms of service, and the terms of any account used for testing, govern what is
  permitted. Automated or programmatic use of the service may violate them.
- Do not use this research to bypass account limits, resell access, or hide the identity of
  abusive traffic. The egress-IP selection described here is a technical capability, not a
  licence to misuse the service.
- Reverse engineering is restricted by law in some jurisdictions. Check your own.
- Any published code must carry a licence compatible with the obligations above.

## No-credentials rule

**Never commit credentials.** No accounts, passwords, e-mail addresses, tokens, TOTP secrets,
session cookies, or hashes of any of them belong in this repository — not in reports, not in
test fixtures, not in commit messages. `.env`, `*.key` and `*.pem` are ignored by default.

Live testing, when it happens, is performed with an Owner-supplied throwaway account and the
results are recorded as observations only; the credential itself is never written to disk in
this tree.

## Layout

```
README.md                  this file
PLAN.md                    campaign plan, slices, acceptance criteria, risks
research/00-plan/          question tree, master index, ledger
research/10-* .. 90-*      per-slice research output (later waves)
```

## Reports

All research reports are written in English and cite a path, URL, or command for every claim.
Claims that were not directly observed are marked `[INFERENCE]` or `[UNKNOWN]`.
