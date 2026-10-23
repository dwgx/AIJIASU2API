# Code review — audit wave 83

Line-level review of the daemon, emulator and dashboard paths that carry the
most risk: the node controller, the login/session/TLS/control stack, the node
and account API handlers, the update check, the SOCKS5 gate, the emulator
control client, the dashboard's async hook and accounts UI, and the deploy
script.

- **Tree**: `D:/Project/LovePlusSpeedToProxy`, branch `panel-design`, HEAD `2eee11a` (`feat(panel): add release update check`), working tree clean before the review.
- **Host**: Windows 10 x64, Go 1.26.7, Node toolchain untouched. No Docker, no live vendor call, no credential read.
- **Baseline**: `cd daemon && go vet ./...` → exit 0; `cd daemon && go test ./...` → every package `ok`. The findings below are therefore *not* caught by the current suite.
- **Method**: read every listed file end to end, traced each cross-file contract (login result → node controller → gate → session, and API hook → controller → panel), then reproduced the behavioural findings with Go `-overlay` tests that add files to a package **without touching the tree** (harness in `evidence/code-review/`, commands inline below).

**Verdict: no P0. 3 × P1, 13 × P2.** The P1s are all "a failure is classified as success, and the consumer then destroys working state" bugs: the emulator client ignores the HTTP status, the login service overwrites the retained server list with an empty one, and the account-delete/logout seam reconciles the panel *before* the store mutation it is supposed to hide.

| # | Sev | Location | Finding |
|---|-----|----------|---------|
| 1 | P1 | `emulator/internal/api/client.go:344-354` | `post` ignores the HTTP status; 5xx bodies are decoded as responses |
| 2 | P1 | `daemon/internal/account/login_service.go:205-219` | failed/empty `loginSync` replaces the retained server list → node controller deletes every node |
| 3 | P1 | `daemon/internal/api/accounts.go:76-84` | delete/logout reconcile the panel before the store cascade/drain; nothing reconciles after |
| 4 | P2 | `daemon/internal/runtime/nodes.go:340-347` | `DrainNode` mutates the store first and skips the reconcile on a gate error |
| 5 | P2 | `daemon/internal/gate/server.go:328-365` | `Close` never joins handler goroutines (`handlers` WaitGroup is test-only) |
| 6 | P2 | `daemon/internal/runtime/nodes.go:383-389` | `Stop` closes gates serially with no deadline; each `Close` may burn 30 s |
| 7 | P2 | `daemon/internal/runtime/nodes.go:527-556` | gate-port cursor is monotonic; freed ports are never reused and it never wraps |
| 8 | P2 | `daemon/internal/api/nodes_leases.go:77-90` | every drain/undrain reconciles Resin twice |
| 9 | P2 | `daemon/internal/runtime/nodes.go:270-282` | a cancelled request context marks the remaining nodes DOWN |
| 10 | P2 | `dashboard/src/lib/useAsync.ts:120-124,159-166,175-177` | a request that never settles latches the hook forever |
| 11 | P2 | `dashboard/src/pages/Accounts.tsx:357-367` + `lib/api.ts:154` | bulk delete is `Promise.all`; one 404 wedges the dialog after a partial delete |
| 12 | P2 | `dashboard/src/pages/Accounts.tsx:303-308` + `CommandPalette.tsx:130` | opening/closing the create drawer drops the `q` search param |
| 13 | P2 | `daemon/internal/api/update.go:116-122` | `draft`/`prerelease` guard is dead on `/releases/latest` |
| 14 | P2 | `deploy/update.sh:108-114` | rollback tags two refs without checking the images exist; a failure leaves a half-restored pair |
| 15 | P2 | `dashboard/src/components/CommandPalette.tsx:201-204` | Enter during an IME composition runs the highlighted entry |
| 16 | P2 | `daemon/internal/account/control.go:40,132` | stale docs: who owns a `Control`, and what `Servers` does |

---

## P1

### P1-1 — `post` treats every HTTP status as success

**Where**: `emulator/internal/api/client.go:344-354`

```go
resp, err := c.HTTP.Do(req)
...
data, readErr := decodeBody(resp)
resp.Body.Close()
...
return data, nil
```

**Evidence** (expected FAIL — the test asserts the correct behaviour):

```
cd daemon && go test -count=1 \
  -overlay=../research/83-audit-bench/evidence/code-review/overlay-account.json \
  ./internal/account -run TestOverlayPostIgnoresHTTPStatus -v
--- FAIL: TestOverlayPostIgnoresHTTPStatus (0.00s)
    overlay_status_test.go:53: control.Login on HTTP 500: err=<nil>
      result={AuthResult:OK ... Servers:map[]}
    overlay_status_test.go:55: HTTP 500 with a JSON body was treated as a successful login
```

**What**: the loop returns `data, nil` for *any* status. A 5xx body that happens to contain a `Login` object is parsed as a successful login (`AuthResult:"OK"` in the test), and a 5xx/4xx body without `Servers` is parsed as a successful `loginSync` with zero servers. `decodeBody` also ignores the status, so the error text is lost. The comment on `post` ("an HTTP error status or invalid JSON is returned to the caller") describes the *body* being returned, not an error — the caller has no way to tell a 500 from a 200.

**Fix**: after `c.HTTP.Do`, reject non-2xx before decoding:

```go
if resp.StatusCode < 200 || resp.StatusCode >= 300 {
    resp.Body.Close()
    return nil, fmt.Errorf("api: %s: HTTP %d", url, resp.StatusCode)
}
```

That makes the daemon classify the failure as `ErrUpstream` (no account state change) instead of "rejected" or "no servers".

**Risk**: a vendor/WAF error page is interpreted as a login result. With P1-2 it silently deletes nodes; on its own it can mark a healthy account `auth_failed` when the error body carries a `Login` envelope with a non-OK `AuthResult`.

---

### P1-2 — a failed `loginSync` wipes the retained server list, and the node controller deletes every node

**Where**: `daemon/internal/account/login_service.go:205-219`

```go
if synced, syncErr := control.LoginSync(ctx); syncErr != nil {
    s.logger.Warn("account_login_sync_failed", ...)
} else {
    ...
    result.Servers = synced.Servers
}
```

**Evidence A** (vendor error envelope, HTTP 200):

```
cd daemon && go test -count=1 \
  -overlay=../research/83-audit-bench/evidence/code-review/overlay-account.json \
  ./internal/account -run TestOverlayVendorSyncErrorWipesServers -v
    overlay_sync_error_test.go:34: after good sync: servers=1
    overlay_sync_error_test.go:38: second Login: err=<nil> state="active"
    overlay_sync_error_test.go:40: after vendor ERR sync: servers=0
```

**Evidence B** (end-to-end, real `LoginService` + real `NodeController` + fake vendor):

```
cd daemon && go test -count=1 \
  -overlay=../research/83-audit-bench/evidence/code-review/overlay-runtime-e2e.json \
  ./internal/runtime -run TestOverlayLoginSyncErrorWipesNodes -v
    overlay_e2e_test.go:76: after good login+sync: nodes=1
    overlay_e2e_test.go:84: after vendor ERR loginSync: syncErr=runtime: account acc-01:
      no node started for 0 usable servers nodes=0
```

**What**: `Control.LoginSync` (`emulator/internal/api/client.go:429-455`) never looks at `Login.AuthResult`; an error envelope is a "successful" sync with an empty `Servers` map. `Login` then stores that empty map as the retained result. The login hook calls `SyncAccount`, whose `desired` set is empty, so `removeUndesiredLocked` (`nodes.go:484-518`) closes every gate, closes every FV session and deletes every node of the account. `runLoginHook` (`api/accounts.go:121-128`) swallows the sync error, so the operator sees HTTP 200 and a green login. A 502 (Evidence A of P1-1) reaches the same state through a different door.

**Fix**: treat "no server list" as unknown, not as "the account has no servers". Minimal change in `Login`:

```go
if synced, syncErr := control.LoginSync(ctx); syncErr != nil {
    s.logger.Warn("account_login_sync_failed", ...)
} else if len(synced.Servers) > 0 {
    ...
    result.Servers = synced.Servers
}
```

and make `Client.LoginSync` return an error when `env.Login != nil && env.Login.AuthResult != "" && !env.Login.OK()`.

**Risk**: one transient vendor hiccup takes the account's whole node set offline (gates closed, store rows deleted, Resin subscription emptied by the next reconcile) while reporting success. Recovery needs another successful login.

---

### P1-3 — account delete/logout republish the nodes they just stopped, and never reconcile afterwards

**Where**: `daemon/internal/api/accounts.go:76-84` (delete) and `:158-166` (logout); the reconcile lives in `runtime/nodes.go:409-430`.

**Evidence**:

```
cd daemon && go test -count=1 \
  -overlay=../research/83-audit-bench/evidence/code-review/overlay-runtime-delete.json \
  ./internal/runtime -run TestOverlayStopAccountReconcilesBeforeDelete -v
    overlay_delete_test.go:63: reconciles=2 live-at-last-reconcile=1
      nodes-after-delete=0 reconciles-after-delete=0
```

**What**: the handler runs `AccountDeleteHook` → `StopAccount`, which closes the gates/sessions **and reconciles**. `ResinPanel.render` (`runtime/panel.go:375-391`) publishes `store.Nodes()` filtered to `NodeLive` — at that moment the account's nodes are still LIVE (the cascade has not run), so the reconcile *re-publishes* them, now pointing at closed loopback ports. `store.DeleteAccount` then removes the rows with no further reconcile; `LogoutAccount` (`store/store.go:658-682`) marks them DRAINING with no further reconcile. Nothing reconciles on a timer; the dashboard never calls `POST /v1/reconcile` (`grep -rn reconcile dashboard/src` → no hits).

**Fix**: reconcile *after* the store mutation. Have `handleDeleteAccount`/`handleLogoutAccount` call `s.cfg.Panel.Reconcile(r.Context())` once the store call succeeds (best-effort, log-only on failure), or add a post-mutation hook beside the existing pre-mutation ones. `StopAccount` should then drop its own reconcile, or mark the account's nodes DRAINING before reconciling so the publish actually removes them.

**Risk**: after an operator deletes or logs out an account, Resin keeps leasing its tags to clients; every flow is refused by the closed gate until an unrelated node sync/drain happens to reconcile. The panel's own topology view also keeps listing the deleted nodes.

---

## P2

### P2-4 — `DrainNode` mutates the store first and skips the reconcile when the gate refuses

**Where**: `daemon/internal/runtime/nodes.go:340-347` (mirror at `:352-369`)

```go
if _, err := c.store.DrainNode(id); err != nil { return err }
running, ok := c.running[id]
if ok {
    if err := running.gate.Drain(ctx); err != nil {
        return fmt.Errorf("runtime: drain node %s: %w", id, err)
    }
    running.draining = true
}
return c.reconcileLocked(ctx)
```

**Evidence**:

```
cd daemon && go test -count=1 \
  -overlay=../research/83-audit-bench/evidence/code-review/overlay-runtime-drain.json \
  ./internal/runtime -run TestOverlayDrainErrorLeavesPanelStale -v
    overlay_drain_test.go:54: DrainNode err=runtime: drain node acc-01:s1: drain failed
      store state="DRAINING" reconciles before=1 after=1
```

**What**: the store is DRAINING before the gate is touched; when `gate.Drain` fails (a cancelled request context is enough, `gate.Drain` returns `ctx.Err()`), the function returns without reconciling, so Resin still advertises a node that no longer admits flows. The API maps the error to 500 (`writeNodeDrainHookError`), so the operator sees a failure although the store changed.

**Fix**: drain the gate first, then the store; or always reconcile before returning the gate error (`defer` the reconcile, aggregate errors). Same for `UndrainNode`.

**Risk**: store/panel divergence that lasts until the next successful reconcile; clients keep being routed to a refusing gate.

### P2-5 — gate `Close` does not join its handlers

**Where**: `daemon/internal/gate/server.go:328-365`; the `handlers sync.WaitGroup` at `:155` is incremented at `:398` but never waited in production (`grep -rn 'handlers.Wait' daemon/internal/gate` → only `server_test.go:581,636`).

**Evidence**:

```
cd daemon && go test -count=1 \
  -overlay=../research/83-audit-bench/evidence/code-review/overlay-gate.json \
  ./internal/gate -run TestOverlayCloseDoesNotJoinHandlers -v
    overlay_closejoin_test.go:51: Close returned with handlers still inside OpenFlow: 1
```

**What**: `Close` closes the listener, waits up to `DrainGrace` for `active` to hit zero, force-closes clients and cancels flow contexts, then returns. A handler already inside `Session.OpenFlow` is not joined: `flowSession.OpenFlow` only checks `closed` *before* calling `OpenStreamContext` (`account/session.go:215-223`), and the real carrier uses a fixed 20 s handshake deadline after `dialTCP` (`emulator/internal/fv/binary.go:324-335`) — ctx cancellation only aborts the dial. `NodeController.closeGateLocked` (`nodes.go:583-596`) therefore calls `session.Close()` while that `OpenFlow` is still running, and a flow handshake can complete *after* the session was closed — contradicting the comment on `closeGateLocked` ("stops a gate before its session, so no flow can outlive the connection it relays through").

**Fix**: wait for the handlers before returning, after the force-close step:

```go
s.mu.Unlock()
s.handlers.Wait()
```

**Risk**: use-after-close on the session and a vendor flow established for an account whose gates were already stopped (delete/logout/shutdown). Narrow window, but it is exactly the ordering the controller claims to guarantee.

### P2-6 — `Stop` closes gates serially with no deadline

**Where**: `daemon/internal/runtime/nodes.go:383-389`

**Evidence**: `defaultDrainGrace = 30 * time.Second` (`gate/server.go:53`); `Close` waits that long when `active > 0` (`gate/server.go:344-350`); `Stop` loops `closeGateLocked` under `c.mu`; `main.go:143-149` passes a 10 s context that only reaches `reconcileLocked`, not `Close`.

**What**: one active flow per gate makes shutdown cost up to 30 s per gate, serially. The HTTP server's own `Shutdown` already used up to 5 s before this. The process therefore exceeds Docker's default 10 s stop grace (`deploy/compose.yml` sets no `stop_grace_period`) and is SIGKILLed before the final `saveState` defer runs.

**Fix**: bound the close loop by the caller's context (or run the closes concurrently with a shared deadline), and set `stop_grace_period: 60s` on `accountd` in `deploy/compose.yml`. [UNVERIFIED] for the SIGKILL timing — no Docker run on this host; the 30 s wait is code-proven.

**Risk**: the last snapshot (node states, draining flags, probe timestamps) is lost on every stop that has traffic in flight; with many nodes the stop can take tens of minutes.

### P2-7 — the gate-port cursor only moves forward and never wraps

**Where**: `daemon/internal/runtime/nodes.go:527-556`

```go
for port := c.nextPort; port <= maxGatePort && attempts < maxGatePortAttempts; port++ { ... }
...
c.nextPort = bound + 1
```

**What**: `nextPort` starts at `portBase` (13000) and is only ever incremented; a closed gate's port is never reused, and once `nextPort` passes 65535 the loop body never runs again, so every new gate fails with "no free loopback gate port" even with zero gates running. `reservedPortsLocked` already knows which ports are free, so the cursor adds nothing but the leak. Capacity is ~52.5k gate starts per daemon lifetime.

**Fix**: wrap the search (`if c.nextPort > maxGatePort { c.nextPort = c.portBase }`) and drop the cursor entirely in favour of scanning `reservedPortsLocked` from `portBase`.

**Risk**: a long-lived daemon with a rotating server list eventually cannot start any node; the failure is permanent until restart.

### P2-8 — every drain/undrain reconciles Resin twice

**Where**: `daemon/internal/api/nodes_leases.go:77-90` (and `:106-119`); wiring at `cmd/ajs-accountd/main.go:153-158`.

**What**: `NodeDrainHook` is `controller.DrainNode`, which drains the store, drains the gate **and reconciles** (`nodes.go:347`). The handler then calls `s.cfg.Panel.DrainNode`, which drains the store again and reconciles again (`panel.go:277-287`). Each reconcile is `EnsureProbeConfig` + `EnsureSubscription` + `EnsurePlatform` + `Publish` + `SystemInfo` (`panel.go:312-370`). So a drain costs two full publish cycles instead of one.

**Fix**: let the controller own the gate and the panel own the publish — drop `reconcileLocked` from `DrainNode`/`UndrainNode` when a `Panel` handles the request, or make the handler skip `Panel.DrainNode` when the hook already ran.

**Risk**: doubled drain latency and doubled Resin API traffic; with `WriteTimeout: 120s` and a slow Resin, a drain can approach the write timeout. [UNVERIFIED] call count against a live Resin (no live call in this wave).

### P2-9 — a cancelled request context marks the remaining nodes DOWN

**Where**: `daemon/internal/runtime/nodes.go:270-282`

**What**: `SyncAccount` opens sessions with the caller's context. The production caller is the login handler (`api/accounts.go:114` → `runLoginHook(r.Context(), id)`), so if the browser disconnects or times out mid-sync, every remaining `sessions.Open` fails with `context canceled` and takes the `markDownLocked` branch, which writes DOWN nodes and then publishes them. The already-opened gates stay up, so the account ends half-synced with spurious DOWN rows.

**Fix**: check `ctx.Err()` at the top of the loop and abort without writing DOWN; better, open sessions with a controller-scoped context and use the request context only to cancel the wait.

**Risk**: spurious DOWN nodes in the store and the panel after a cancelled login; the next login repairs them, but the dashboard shows the outage in the meantime.

### P2-10 — `useAsync` latches forever when a request never settles

**Where**: `dashboard/src/lib/useAsync.ts:120-124` (early return), `:159-166` (only reset), `:175-177` (cleanup)

```js
if (inFlightRef.current) { if (queueIfBusy) queuedRef.current = true; return }
inFlightRef.current = true
...
.finally(() => { inFlightRef.current = false; ... })
```

**What**: `inFlightRef` is per hook instance and is cleared only from the promise's `finally`. The effect cleanup on a deps/reload change bumps `generationRef` but leaves the latch set, and `http()` (`lib/api.ts:63`) passes no `AbortSignal` and no timeout. A fetch that never settles (proxy black hole, a page suspended in a bfcache, a daemon that stops answering) therefore makes every later `reload()` and every poll a no-op: the new run only sets `queuedRef`, and the only code that drains the queue is the dead request's `finally`. The global activity counter also stays above zero, so the topbar progress line never stops.

**Fix**: give each generation its own `AbortController`, pass `signal` to the loader, and reset `inFlightRef`/`queuedRef` in the effect cleanup (the generation guard already prevents stale writes). A hard timeout on `http()` is the belt-and-braces version.

**Risk**: one hung request freezes a page's data for the lifetime of the mount, with no error and no stale marker.

### P2-11 — bulk delete reports total failure after a partial success and wedges the dialog

**Where**: `dashboard/src/pages/Accounts.tsx:357-367`; `dashboard/src/lib/api.ts:154`

```ts
await Promise.all(ids.map((id) => http<void>(`/v1/accounts/${id}`, { method: 'DELETE' })))
```

**What**: `Promise.all` rejects on the first failure. If one ID was already deleted (another tab, a poll that raced the click) the remaining DELETEs may still have succeeded, but `confirmDelete` shows "Delete failed", does not reload, does not prune `selected` and does not clear `deleteIds`; retrying hits the same 404 forever, so the confirm dialog can only be dismissed with Cancel. `selected` is never pruned when the 15 s poll drops an account, so the same stale-ID 404 is reachable from the bulk bar.

**Fix**: `Promise.allSettled`, then always `accounts.reload()`, prune `selected`/`deleteIds` by the settled IDs, and report "N deleted, M failed" instead of a boolean.

**Risk**: operator cannot tell what was deleted, and the dialog looks broken.

### P2-12 — opening or closing the create drawer drops the `q` search param

**Where**: `dashboard/src/pages/Accounts.tsx:303-308`; the palette action at `dashboard/src/components/CommandPalette.tsx:130`.

```ts
const closeCreate = () => {
  setParams({}, { replace: true })
  ...
}
// CommandPalette:
run: () => navigate('/accounts?new=1'),
```

**What**: the drawer is driven only by `?new=1` (`Accounts.tsx:283`), and its only opener is the palette action, which navigates to a URL without `q`. `useParamQuery` follows every param change, so the palette's account search (`/accounts?q=Label`) is cleared the moment the drawer opens; `closeCreate` then calls `setParams({})`, removing every remaining param. The comment above `useParamQuery` (`:32-36`) explicitly promises the opposite ("without clobbering a query the user typed ... e.g. opening `?new=1` or closing the drawer").

**Fix**: preserve the current search in the palette action (read `useLocation().search`, set/keep `q`) and delete only the drawer key on close:

```ts
const next = new URLSearchParams(params)
next.delete('new')
setParams(next, { replace: true })
```

**Risk**: the palette's account search result is lost as soon as the operator opens the create drawer.

### P2-13 — the draft/prerelease guard is dead on `/releases/latest`

**Where**: `daemon/internal/api/update.go:116-122`

```go
func newerVersion(tag, current string, draft, prerelease bool) bool {
    if draft || prerelease { if !isPrerelease(current) { return false } }
```

**What**: the only caller passes `release.Draft`/`release.Prerelease` from `GET /repos/{repo}/releases/latest`, which GitHub documents as the most recent **non-draft, non-prerelease** release. The two flags are therefore always `false` in production, and the branch (plus the `isPrerelease` helper) is unreachable. `update_test.go:133-149` exercises the branch with a stub response carrying `"prerelease":true` / `"draft":true`, which `/releases/latest` cannot return — that is why it looks covered.

**Fix**: either drop the parameters and `isPrerelease`, or switch the endpoint to `/releases?per_page=1` if pre-releases are meant to be surfaced. Keep the semver comparison.

**Risk**: dead code that implies a protection (stable deployments never nagged into an unstable build) which the endpoint already provides; a future switch to `/releases` would silently rely on untested-in-production logic.

### P2-14 — rollback tags two refs without checking the images still exist

**Where**: `deploy/update.sh:108-114`

```sh
docker tag "$OLD_ACCOUNTD_ID" "$ACCOUNTD_REF"
docker tag "$OLD_DASHBOARD_ID" "$DASHBOARD_REF"
compose up -d --no-build
```

**What**: `update-state.json` stores raw image IDs. If either image was pruned between the update and the rollback, the first `docker tag` may succeed and the second fail; `set -e` aborts the script right there, leaving `ACCOUNTD_REF` pointing at the old image while `DASHBOARD_REF` still points at the failed release. The script also never reads the recorded `tag` field, so the refs it rewrites are derived from the *current* `AJS_IMAGE_TAG`, not from the tag the state file says was deployed.

**Fix**: verify both IDs before touching any ref (`docker image inspect "$ID" >/dev/null 2>&1 || die ...`), then tag both; use the recorded tag for the refs (or drop the unused `tag`/`updated_at` fields).

**Risk**: a half-restored pair after a failed rollback, and a local tag that no longer describes the image it names. [UNVERIFIED] end-to-end — no Docker on this host.

### P2-15 — Enter during an IME composition runs the palette entry

**Where**: `dashboard/src/components/CommandPalette.tsx:201-204`

```tsx
} else if (event.key === 'Enter') {
  event.preventDefault()
  const entry = filtered[selected]
  if (entry) runEntry(entry)
}
```

**What**: no `event.nativeEvent.isComposing` check. Committing a CJK composition with Enter fires this handler in the same keydown, so typing a Chinese account label in the palette navigates away on the first commit instead of finishing the text.

**Fix**:

```tsx
} else if (event.key === 'Enter' && !event.nativeEvent.isComposing) {
```

**Risk**: the palette's account/node search is unusable for IME input. [UNVERIFIED] in a browser — reasoned from the DOM event contract, not exercised.

### P2-16 — stale `Control` documentation

**Where**: `daemon/internal/account/control.go:40` and `:132`

- `:40` — "not safe for concurrent use; **the session factory owns one per account**". The session factory (`account.Factory`) never touches a `Control`; `LoginService` keeps one per account in `controls` (`login_service.go:94`, `remember` at `:292-305`). The ownership sentence is wrong, and it is the sentence a future caller would use to decide whether sharing is safe.
- `:132` — "Servers returns the server map from LoginSync", but the method issues a fresh `client.Servers` request (`:133-135`); `Control.LoginSync` is what refreshes them.

**Fix**: "the login service owns one per account (see `LoginService.controls`)"; "Servers performs a loginSync and returns only the server map."

**Risk**: documentation-only, but it misstates both the owner and the side effect of a method on the vendor path.

---

## Reviewed, no findings

- `daemon/internal/account/tls.go` — the pin replaces chain/hostname verification deliberately and is documented; `InsecureSkipVerify` is scoped to the pinned client, `hmac.Equal` is used for the compare, malformed fingerprints fail startup (`main.go:397-407`). No issue.
- `daemon/internal/account/session.go` — profile matching and request validation are sound; the `closed` TOCTOU is reported under P2-5 because the gate is the only caller that can race it.
- `daemon/internal/account/control.go` — logic is correct apart from the docs above; `Login`/`LoginSync` failover semantics match the emulator client.
- `daemon/internal/api/update.go` — the timeout, body limit, status handling and semver comparison are correct; only the dead branch above.
- `daemon/internal/api/nodes_leases.go` — filters, pagination and 404-before-hook ordering are correct.
- `daemon/internal/gate/server.go` — SOCKS5 parsing, reply codes, admission accounting, drain/undrain and the `zeroCh`/`drainCh` handoff are correct; `Close`'s join and the shutdown budget are the only issues (P2-5/P2-6).
- `dashboard/src/components/CommandPalette.tsx` — focus capture/restore, the scrim (`pointer-events: none`, so the backdrop click works), and the fetch cancellation flag are correct; only the IME check above.
- `deploy/update.sh` — the health checks (2261 host network, 18081 published loopback) match `compose.yml`; the state file is written before the pull so a failed pull is still rollback-able.

## Harness

The reproduction tests are `-overlay` files: Go adds them to the package for one build without writing into the repository. Manifests and sources live in `research/83-audit-bench/evidence/code-review/`; the manifests contain absolute paths for this workstation, so regenerate them elsewhere with the same mapping.

| Manifest | Package | Test | Expected |
|----------|---------|------|----------|
| `overlay-account.json` | `./internal/account` | `TestOverlayPostIgnoresHTTPStatus` | FAIL (proves P1-1) |
| `overlay-account.json` | `./internal/account` | `TestOverlayLoginSyncFailureDropsServers`, `TestOverlayVendorSyncErrorWipesServers` | PASS (proves P1-2 at the service layer) |
| `overlay-runtime-e2e.json` | `./internal/runtime` | `TestOverlayLoginSyncErrorWipesNodes` | PASS (proves P1-2 end to end) |
| `overlay-runtime-delete.json` | `./internal/runtime` | `TestOverlayStopAccountReconcilesBeforeDelete` | PASS (proves P1-3) |
| `overlay-runtime-drain.json` | `./internal/runtime` | `TestOverlayDrainErrorLeavesPanelStale` | PASS (proves P2-4) |
| `overlay-gate.json` | `./internal/gate` | `TestOverlayCloseDoesNotJoinHandlers` | PASS (proves P2-5) |

All fixtures are synthetic (`tok-1|user`, `cp-1`, `203.0.113.x`); no credential, host or account from the live run is used.

## Limits

- No live vendor call, no Resin instance, no Docker run. P1-3's *Resin-side* effect and P2-6's SIGKILL timing are reasoned from `panel.go`/`compose.yml`, not observed; they are marked where relevant.
- The dashboard findings are static: no browser, no Vitest run in this wave (the dashboard suite is another lane's benchmark).
- The existing daemon suite also passes under `go test -race -count=1 ./...` (all packages ok), so none of the ordering problems above is exercised by a current test; P2-5's handler window was established by construction.
