# Roadmap

This roadmap is honest about direction, not a schedule. There are no dates and no delivery
promises. Items move when the evidence supports them, and an item can be removed without notice.

The current release is `v0.1.0-pre`; see [CHANGELOG.md](CHANGELOG.md) and the full
[release notes](docs/releases/v0.1.0-pre.md). The use boundary in
[README.md](README.md#use-boundary) and [DISCLAIMER.md](DISCLAIMER.md) applies to everything below.

## Now

- Keep the public demo invite-only: operator-issued `ajs_sk_` keys, no signup, no SLA, no resale.
- Keep the public edge limited to HTTPS CONNECT and the scoped API allowlist.
- Correct public claims that do not match the code or the live edge, and keep generated docs in
  sync with `docs/openapi.json`.
- Keep the release gate green: Go 1.26.10 or newer, a vulnerability scan, pinned GitHub Actions,
  and the third-party notices in every artifact. A gate that is described but not run is not a
  gate.

## Next

Ordered by risk, not by appeal. These are the items that block a stable tag.

1. **Prove the authorization lifecycle end to end.** Revocation and expiry must close tunnels that
   are already established, not only refuse new handshakes. The fix is tracked as AJS-476; it
   needs a live proof on both the SOCKS5 and HTTPS CONNECT listeners, including the case where
   revocation races a CONNECT.
2. **Add edge rate limits.** The production kit defines per-IP request and connection zones; the
   live edge does not apply them. Apply the reviewed configuration, verify it from the active
   nginx dump, and keep the daemon's per-key limits authoritative below the edge.
3. **Close the live RBAC gap.** The live daemon runs in compatibility mode and accepts the legacy
   shared bearer. Run the public deployment in strict RBAC, or document the exposure as a
   deliberate limit of the demo. The public vhost's stated precondition must match reality.
4. **Align the live edge with the contract.** The contract fixes for schedule-run, 504/503
   envelopes and the generated mirror are in the tree; `/metrics` is still advertised as a scoped
   public route but returns 404 at the live origin. Route it or narrow the public contract, and
   keep tests that compare behaviour, not only schema shape.
5. **Verify the pre-release pipeline on a scratch tag.** The workflows now gate `:latest`
   promotion and the GitHub `prerelease` flag on a tag containing `-`, but they have never run
   (no remote). Exercise a scratch tag, confirm `:latest` is unchanged, and keep
   [PROTOCOL.md](PROTOCOL.md) and [docs/RELEASE.md](docs/RELEASE.md) in agreement.
6. **Make the live deployment reproducible.** Import the running unit, environment, and public
   vhost into the reviewed kit, or document the divergence precisely. Add a restore drill that
   runs, not one that is described.
7. **Re-run the production gate on the release host.** The last recorded root `PASS` predates the
   current assertions. Record the new result, with the identity that ran it, before tagging.

## Later

Not scheduled, and not promised:

- Live proof of multi-account coexistence on one daemon, and a second serving slot per account.
- Live Resin publish through the continuous reconciler.
- Live confirmation of the newer request shapes and signing (`X-Sign` / versioned clients).
- Windows service, launchd unit, and systemd unit for packaged installs.
- Published, versioned SDK packages and a compatibility policy for the public API.
- End-to-end TLS or mTLS on the VPS-to-HMC API hop, and per-user auth for the dashboard stream
  routes.

## Not planned

- A public SOCKS5 ingress. The public data plane is HTTPS CONNECT only.
- Resale, shared access, or a commercial multi-tenant proxy service without written vendor
  authorisation covering the accounts, service, and jurisdictions.
- Card charging, money transfer, or a payment service. The epay/Stripe/orders code is
  experimental, disabled, and unsupported.
- Bypassing vendor account limits, hiding abusive traffic, or evading the vendor's terms.
- An uptime or support SLA for the operator-run demo.
- A mobile or desktop client application.

## How items are chosen

An item moves up when it removes a false claim, closes an authorization gap, or makes a release
reproducible. An item moves down when it only adds surface. Security reports follow
[SECURITY.md](SECURITY.md); feature proposals follow [CONTRIBUTING.md](CONTRIBUTING.md).
