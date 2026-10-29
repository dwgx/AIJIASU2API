# Docs index

Which door is yours? If you only want to run the service, you need nothing from the
"Understand the evidence" table. If you are calling the API, start with the guide and treat the
contract as the authority.

Status key: **current** — describes `v0.1.0-pre.3` and is maintained; **historical** — a snapshot
of an earlier design, kept for the record; **generated** — produced from another file, never
edited by hand; **internal** — research or working material, not part of the public release
surface.

## Run and operate it

| Document | Status | Purpose |
|---|---|---|
| [`README.md`](../README.md) | current | front door: what this is, quick start, use boundary |
| [`docs/OPERATIONS.md`](OPERATIONS.md) | current | install, health, backup, rotation, upgrade, rollback |
| [`docs/DEPLOYMENT.md`](DEPLOYMENT.md) | current | three deployment modes, release verification, update and rollback; 简体中文 at [`DEPLOYMENT.zh-CN.md`](DEPLOYMENT.zh-CN.md) |
| [`docs/RELEASE.md`](RELEASE.md) | current | version scheme, release gates, artefacts, checksums, rollback |
| [`docs/releases/`](releases/) | current | one notes file per release; `v0.1.0-pre.3` is current |
| [`docs/VERSIONING.md`](VERSIONING.md) | current | SemVer policy, tag vs assets, `:latest`, deprecation |
| [`docs/TROUBLESHOOTING.md`](TROUBLESHOOTING.md) | current | 407/429/409/404/401/403/502/504 triage with commands |
| [`deploy/production/README.md`](../deploy/production/README.md) | current | reviewed production kit and the live deployment record |
| [`deploy/README.md`](../deploy/README.md) | current | compose deployment |
| [`deploy/packaging.md`](../deploy/packaging.md) | current | artefact build, manifest, checksums |
| [`docs/releases/v0.1.0-pre.3.md`](releases/v0.1.0-pre.3.md) | current | release notes, verified checks, known limitations |
| [`docs/releases/v0.1.0-pre.2.md`](releases/v0.1.0-pre.2.md) | historical | previous release notes and limitations |
| [`docs/releases/v0.1.0-pre.md`](releases/v0.1.0-pre.md) | historical | first release notes and limitations |
| [`CHANGELOG.md`](../CHANGELOG.md) | current | release index |

## Call it

| Document | Status | Purpose |
|---|---|---|
| [`docs/API-GUIDE.md`](API-GUIDE.md) | current | scoped-key quickstart, scopes, leases, the public CONNECT edge, errors |
| [`docs/ADMIN-API.md`](ADMIN-API.md) | current | map of the operator-only surface and its credential rules |
| [`docs/PROTOCOL.md`](PROTOCOL.md) | current | short public summary of the vendor protocol and what it does not protect |
| [`docs/openapi.json`](openapi.json) | current | OpenAPI 3.1 contract — the source of truth for shapes |
| [`dashboard/public/docs/reference.md`](../dashboard/public/docs/reference.md) | generated | rendered reference, generated from the contract by `dashboard/scripts/gen-docs.mjs` |
| [`sdk/README.md`](../sdk/README.md) | current | Go and TypeScript clients |
| [`docs/CLI.md`](CLI.md) | current | source-built `ajs` command: status, nodes, leases, lease, doctor |
| [`dashboard/README.md`](../dashboard/README.md) | current | operator console manual |

## Understand the evidence

| Document | Status | Purpose |
|---|---|---|
| [`docs/ARCHITECTURE.md`](ARCHITECTURE.md) | current | components, ports, storage, live topology, trust boundaries |
| [`docs/SECURITY-MODEL.md`](SECURITY-MODEL.md) | current | what is enforced where, and what is not protected |
| [`docs/PRIVACY.md`](PRIVACY.md) | current | stored data, retention, operator responsibilities |
| [`docs/EPAY-STATUS.md`](EPAY-STATUS.md) | current | why the payment and tenant surfaces are off |
| [`docs/RBAC.md`](RBAC.md) | current | operator identities and the strict-mode cutover |
| [`SECURITY.md`](../SECURITY.md) | current | private vulnerability reporting and scope |
| [`DISCLAIMER.md`](../DISCLAIMER.md) | current | affiliation, authorised use, warranty |
| [`CONTRIBUTING.md`](../CONTRIBUTING.md) | current | contribution rules and evidence requirements |
| [`GOVERNANCE.md`](../GOVERNANCE.md) | current | maintainer model |
| [`ROADMAP.md`](../ROADMAP.md) | current | near-term work and explicitly closed directions |
| [`ARCHIVE.md`](../ARCHIVE.md) | historical | archive map of retained, experimental and removed surfaces |
| [`docs/PANEL.md`](PANEL.md) | historical | early control-plane design baseline, not the implementation |
| [`PLAN.md`](../PLAN.md) | historical | wave-1 research plan |
| [`REVIEW-EPAY-WAVE.md`](../REVIEW-EPAY-WAVE.md) | historical | point-in-time payment audit |
| [`REVIEW-PROD-KIT.md`](../REVIEW-PROD-KIT.md) | historical | point-in-time production-kit audit |
| [`research/40-protocol/PROTOCOL.md`](../research/40-protocol/PROTOCOL.md) | internal | full protocol research record, evidence labels, vectors |
| [`research/`](../research/) | internal | per-wave research reports; each is a point-in-time record |
| [`deploy/live/`](../deploy/live/README.md) | internal | byte-for-byte copies of the configs serving the live hosts |

## Generated and mirrored files

| File | Produced by | Note |
|---|---|---|
| [`dashboard/public/docs/`](../dashboard/public/docs/) | `cd dashboard && npm run docs:gen` | the static pages served at `/docs`; `npm run docs:gen -- --check` fails on drift |
| [`daemon/internal/api/openapi.json`](../daemon/internal/api/openapi.json) | copy of [`docs/openapi.json`](openapi.json) | must stay byte-identical: `cmp docs/openapi.json daemon/internal/api/openapi.json` |
| `dashboard/public/llms.txt` | same generator | machine-readable index of the served docs |

Do not edit a generated file; edit its source and regenerate.

## House rule

Every claim in a doc must be checkable: a path, a command, or an explicit `[UNVERIFIED]`. Counts
are reproduced by the command beside them, never maintained by hand. If a document disagrees
with the contract or with the code, the contract and the code win — and the document is the bug.

## Pointers

- Root entry point: [`README.md`](../README.md)
- Live rendered docs: <https://ajs.dwgx.top:2055/docs>
- Static project page: <https://dwgx.github.io/AIJIASU2API/>
- Releases: <https://github.com/dwgx/AIJIASU2API/releases>
