# AJS-412 — Operator RBAC design and acceptance

## Status and scope

Implemented in `daemon/internal/api/rbac.go`, strict-mode startup wiring and
`deploy/production/render-admin-operators.sh`. The original assignment file was
not present in this checkout; this document records the resulting contract.
No live host, vendor account or production credential was used.

The customer portal, provider-signed payment callbacks and machine `ajs_sk_`
keys are separate authentication worlds. RBAC does not turn a scoped key or a
customer cookie into an operator. `AJS_ALLOW_INSECURE=1` remains a loopback lab
feature only when strict mode is off.

## Roles

| Capability | viewer | operator | admin |
|---|:---:|:---:|:---:|
| Ordinary dashboard reads, metrics, logs, requests and audit | yes | yes | yes |
| `/v1/whoami` | yes | yes | yes |
| Connect/disconnect/login/logout, selection, drain/probe, leases, schedules and egress check | no | yes | yes |
| Reconcile and mutation lab | no | yes | yes |
| Create/edit/delete vendor accounts, import/export, bulk delete | no | no | yes |
| Device generation/lifecycle/import/export | no | no | yes |
| Customer users, proxy keys, scoped keys and webhook administration | no | no | yes |
| Read scoped-key inventory or webhook receivers/deliveries | no | no | yes |
| Account device read, plans/orders/tenant reads | no | no | yes |
| Plans, tenant approval, settlement/refund/chargeback/cancel/reissue | no | no | yes |
| Settings, backup/inspect/restore | no | no | yes |

GET defaults to `viewer`, except explicitly sensitive reads. Unsafe methods
default to `admin`; operator capabilities are explicit exceptions. The router
checks the matched method **and pattern** before invoking its handler, so an
unmapped future mutation fails closed. Denial is HTTP 403 `FORBIDDEN` JSON.
Authentication failures are 401; RBAC never disguises them as 404.

## Credentials and trust boundary

Each person has a unique username, Basic password and independent random API
token. `/etc/ajs/admin-operators` is the root-only source inventory:

```text
username:viewer|operator|admin:random-token:basic-password
```

The renderer produces:

* root:nginx-group `0640` `/etc/nginx/ajs-admin.htpasswd` (apr1 hashes);
* root:root `0600` `/etc/nginx/snippets/ajs-admin-tokens.map` (plaintext bearer
  selected by `$remote_user`, loaded by the root nginx master);
* root:ajs `0640` `/etc/ajs/admin-operators.hashes` (SHA-256 bearer hashes only).

The SPA still receives no bearer. nginx overwrites client `Authorization` and
`X-AJS-Operator` values. The daemon accepts the mapped bearer and derives both
actor and role from its hash lookup. **In strict mode `X-AJS-Operator` is not
an identity or role source.** The direct loopback API can accept an operator
token for CLI use; a local caller with arbitrary file-read/root capability is
outside this boundary and must be constrained by Unix permissions.

Token hashes are offline-verifiable if that file is stolen, so it remains a
secret despite containing no plaintext. Tokens must be high entropy. Credential
files are not included in panel snapshots or backup bundles.

## Migration (no silent downgrade)

1. **Baseline.** Existing deployments use the shared bearer and Basic audit
   label. Upgrade the daemon without setting `AJS_RBAC_STRICT`; compatibility
   behavior and existing SDKs remain valid.
2. **Provision.** Create unique roles/passwords/tokens. Render the three files.
   Configure `AJS_ADMIN_OPERATORS_FILE`; keep `AJS_RBAC_STRICT=0` temporarily.
   Restart the daemon and reload nginx. Test `/v1/whoami` as all three roles.
   This stage logs `rbac_compatibility_mode`: the legacy bearer still works.
3. **Strict cutover.** Set `AJS_RBAC_STRICT=1` and replace the shared-token
   nginx include with the per-operator include from this kit. Restart and
   reload. The shared bearer, missing bearer, anonymous insecure mode and
   spoofed role headers fail; scoped keys retain their existing policy.
4. **Retire.** Remove `AJS_ADMIN_TOKEN` and the old root-only shared-token
   snippet. Rotate any token that was exposed during migration. Check logs and
   `/v1/whoami` (`strictRbac:true`) for each operator.
5. **Rollback before credential retirement.** Restore compatibility config and
   the previous nginx snippet, restart/reload. After retirement, rollback means
   restoring a *new* shared credential deliberately; never reuse a leaked one.

The kit ships strict mode. Merely installing new daemon configuration without
the new nginx map will not authenticate: fail closed, not a shared-token
fallback.

## Rotation, revocation and recovery

There is no hot reload. Restarting the daemon is the revocation barrier;
nginx must also be reloaded after changing its map. For zero-interruption token
rotation, add a second row with the same username and role but a new token.
Conflicting roles or a token shared by two names are rejected. Load both hashes
in the daemon, switch nginx to the new token, verify, then remove the old hash
and restart. Password changes alone do not revoke a copied bearer.

For offboarding, remove the person's rows, render, reload nginx and restart the
daemon. Keep at least one working `admin` credential; the implementation does
not pretend to recover a host whose last administrator and root access are
lost. Recovery is root provisioning of a new admin credential, not an HTTP
role-management endpoint.

## Threat model

| Threat | Control / limit |
|---|---|
| Viewer/operator calls an admin mutation directly | Daemon route policy, not UI hiding; 403 before handler |
| Client forges `X-AJS-Operator` or duplicate identity | Strict actor/role from bearer lookup; nginx overwrites the header |
| Old shared bearer survives cutover | Strict auth does not compare it; named migration test |
| `AJS_ALLOW_INSECURE=1` bypasses roles | Ignored for admin auth in strict mode; lab-only outside it |
| New route accidentally gets broad rights | GET viewer / non-GET admin defaults; exhaustive registration audit test |
| Basic password and API token collapse into one secret | Separate fields and independent rotation |
| Token/hash leakage | nginx map root-only; daemon gets hashes only; no token in logs, whoami, audit or bundle; hashes remain sensitive |
| Revoked user keeps access | Removal plus daemon restart and nginx reload; no hot-reload claim |
| Scoped key or portal cookie crosses boundary | Existing independent auth chains retained; strict mode does not disable scoped-key scopes |
| Browser CSRF | Existing `X-AJS-CSRF` and exact admin Origin checks remain after authentication |
| Backup leaks credentials | Operator files excluded from snapshot/backup; restore cannot alter roles |
| Malformed credential file | Startup rejects duplicate token, conflicting role, invalid name/hash, empty file and unsafe Unix mode before listening |
| Brute force/DoS | TLS, private listener/allowlist and nginx limits remain; SHA-256 is not a password KDF and high-entropy tokens are mandatory |
| Compromised root/nginx or `ajs` account | Can defeat this boundary; use host hardening and file ownership. RBAC is not a defense against host root |
| In-flight request during revocation | Restart terminates existing HTTP work; SSE reconnect must reauthenticate. No distributed/session revocation is claimed |

## Named acceptance tests

* `TestAJS412OperatorRoleMatrix`
* `TestAJS412LegacyTokenRevokedAndIdentityCannotSpoof`
* `TestAJS412OperatorIdentityComesFromToken`
* `TestAJS412ScopedKeyAndPortalIsolation`
* `TestAJS412EveryRouteHasIntendedDefaultPolicy`
* `TestAJS412Whoami`
* `TestAJS412OperatorFileValidation`
* `TestAJS412TwoGenerationRotation`
* `TestAJS412DeniedResponseIsJSONAndAuditsNoMutation`
* `TestAJS412OperatorFileNeverContainsPlaintext`
* `TestAJS412StrictAllowsLegacyTokenRemoval`
* `TestAJS412ProductionRenderer` (production deployment gate; explicit `--dev` skips root-only ownership tests)

The daemon tests are offline. A real production cutover, TLS/nginx reload,
last-admin recovery and revocation on the live host remain operational drills,
not claims established by unit tests.
