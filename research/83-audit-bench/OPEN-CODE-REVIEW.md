# Open Code Review / AACR-Bench — method extraction and a runnable benchmark plan

Date: 2026-10-24 · Lane `83-audit-bench` · Repo `D:/Project/LovePlusSpeedToProxy`, branch `panel-design` @ `2eee11a` · Read-only: no source edit, no git write, no vendor call.

**Ask (one line):** extract how `alibaba/open-code-review` actually reviews code and how AACR-Bench scores it, then turn that into a reproducible Go+TS benchmark for this repo that separates what can be measured today from what needs labeled PRs and an LLM.

**DONE WHEN:** a reader can (a) name each deterministic stage and its source file, (b) reproduce every number in §5 with the command shown, (c) start the B0/B1 tiers without further design work, and (d) knows exactly which metrics are blocked and why.

---

## Verdict

- The method is **deterministic engineering around an LLM**, not an agent reading diffs: file selection, rule matching and line anchoring are code; only judgement is model output. The README's own framing — "hard constraints on the review process" — is accurate.
- **Precision-first is enforced twice**: the Go rule text says "Favor precision over recall … report only defects that are likely real", and the reflection pass is told that keeping a wrong comment costs seconds while deleting a right one destroys a finding, so it removes only what the diff *proves* wrong.
- AACR-Bench's **current** metric is file-level semantic coverage with greedy dedup, **not** line matching. Line-level P/R/F1 is deprecated precisely because `line(k)` was a hard gate that threw away correct comments.
- On this repo OCR's defaults select **103/197 changed files (52.3%) and 25,542/54,570 changed lines (46.8%)**. All **41 test files (13,965 lines, 25.6%)** and all **31 markdown files (14,426 lines, 26.4%)** are excluded by default. That is the single most important fact for benchmark design here.
- Precision/recall/F1/tokens/wall-time are **not measurable today** (no labeled PRs, no git remote, no judge model). What is measurable now: selection coverage, rule coverage, test/coverage health, historical review yield, and a deterministic false-positive triage ledger.

---

## 1. What Open Code Review does

Source read at `main` = `375f64c`; installed CLI is `open-code-review v1.13.3 (375f64c)` — same commit.

### 1.1 Pipeline

| # | Stage | Mechanism | Source |
|---|---|---|---|
| 1 | Bootstrap | resolve `(URL, token, model)`, load 6 prompts, tool registry, system rules | `cmd/opencodereview/`, `internal/config/template/task_template.json` |
| 2 | Diff provider | workspace / commit / range (`merge-base`), `DiffContextLines=3`, untracked files as full additions | `internal/diff/git.go` |
| 3 | **File selection** | six gates, pure function, no git/LLM access | `internal/agent/selection.go` |
| 4 | **Bundling** | one LLM call over *metadata only*, max 10 files/group, per-group sub-agent | `internal/agent/grouping.go` |
| 5 | Plan (optional) | ≥50 changed lines in one file, or ≥100 across ≥2 files | `internal/config/template/template.go:62-75` |
| 6 | Main loop | tool-use loop, ≤100 requests, 1/2/3 rounds by `--effort`, compression at 60%/80% of 200k | `internal/llmloop/loop.go`, `compression.go` |
| 7 | **Line resolution** | normalized consecutive-line match: hunk new side → hunk old side → whole file | `internal/diff/resolver.go:15,62,151,238` |
| 8 | **Cross-file relocation** | verbatim `existing_code` search over all diffs, unique hit only | `internal/diff/resolver.go:100` |
| 9 | **LLM re-location** | only when 7+8 fail; model must return the minimal range verbatim | `internal/diff/relocation.go:28,49` |
| 10 | **Reflection** | `REVIEW_FILTER_TASK` removes only provably-wrong comments; `--no-filter` disables | `internal/agent/agent.go:1798`, prompts `review_filter_task_system.md` |
| 11 | Second resolve pass + render | re-runs `ResolveLineNumbers` over the full set; unresolved → `start_line=0` = unanchored | `cmd/opencodereview/review_cmd.go` |

### 1.2 The six gates (deterministic file selection)

Order is fixed in `whyExcluded` (`selection.go:70-102`):

| # | Gate | Reason emitted | Note |
|---|---|---|---|
| 1 | binary | `binary` | first |
| 2 | built-in secret paths | `secret_exclude` | before user rules; **no include can override** |
| 3 | user `exclude` | `user_exclude` | highest user precedence |
| 4 | user `include` | — | kept **immediately**, bypasses gates 5–6 |
| 5 | extension allowlist | `unsupported_ext` | **skipped when the path has no extension** (`selection.go:93`: `if ext != "" && …`) |
| 6 | built-in default paths | `default_path` | tests, fixtures, `node_modules`, `vendor`, `dist`, `*.pb.go`, … |

After the gates, `selectFiles` (`selection.go:44`) adds `deleted` and `too_large` (diff > 80% of `max_tokens`). `--preview` consumes this exact function, so its `will_review` set is the real run's set (`preview.go:51`, no LLM call, no session file).

Two consequences that matter for us:

- Extensionless files enter review: `LICENSE`, `Dockerfile`, `.gitignore`, `.gitattributes` were all `will_review: true` in our preview (6 files here).
- `.env` is allowed and secret-exempt, but **`.env.example` is not**: its extension is `.example`, so it is dropped as `unsupported_ext`. The secret-template exemption never gets a chance to apply.

### 1.3 Bundling

One `GROUPING_TASK` call carrying path, status and `+/-` counts — **never diff content** — returns `{label, files:[indices]}` (`grouping.go`). Guards:

| Guard | Effect | Source |
|---|---|---|
| `maxFilesPerGroup = 10` | oversized groups split into 10-file chunks | `grouping.go:22,326` |
| per-group token budget | a bundle over the prompt limit degrades to per-file groups | `grouping.go` `enforceGroupTokenBudget` |
| coverage | any file the model did not assign gets its own group | `grouping.go` `parseGroupingResponse` |
| failure | unparseable/empty response → one-file-per-group | `groupDiffs` |

Small change sets skip the LLM entirely (`GroupingPlan`); a single file never calls it.

### 1.4 Rule matching

Four layers, first match wins, declaration order inside a layer: `--rule` → `<repo>/.opencodereview/rule.json` → `~/.opencodereview/rule.json` → embedded `system_rules.json`. Matching is `doublestar` globs, case-insensitive, `**` crosses directories. A matching user entry **replaces** the system rule unless `merge_system_rule: true`, in which case the system half is still resolved **per file** (so one catch-all entry adds your rule to `go.md`, `ts_js_tsx_jsx.md`, … without enumerating extensions). `.m` is content-sniffed between MATLAB and Objective-C.

The resolved body becomes `{{system_rule}}` in both the plan and main prompts.

### 1.5 Line-level positioning

`existing_code` is the anchor. `ResolveLineNumbers` normalizes lines (`resolver.go:301`) and requires a **consecutive** run: hunk new side, then hunk old side, then a scan of the full new file skipping blank lines. Inside the loop the order is: the comment's own file (`ResolveComment`), then a cross-file search that accepts only a **unique** hit (`resolver.go:100`, called at `internal/llmloop/loop.go:775`), then the model. The cross-file step runs before the LLM because the LLM step overwrites `existing_code` — the one piece of evidence pointing at the real file. If everything fails, the model is asked to copy the minimal range verbatim (`re_location_task_user.md`) and the comment is retried. Anything still unresolved stays at `start_line=0`, which downstream treats as unanchored.

### 1.6 Reflection

`executeGroupReviewFilter` (`agent.go:1798`) sends the group's diff plus its comments to a **fact-checker**, not a reviewer. The prompt is explicit about asymmetric cost:

> "Keeping an incorrect comment costs a reviewer a few seconds of attention. Removing a correct comment silently destroys a real finding … when your evidence falls short of proof, approve."

It runs per group, can be limited to comments added in the current round (`from` map), and records `comments.before` / `comments.filtered` in telemetry. `--no-filter` is the ablation switch.

### 1.7 Budgets

`--max-tools` (default 100, min 50), `--effort` (1/2/3 rounds), `--max-tokens` (prompt ceiling), `--max-tokens-budget` (run total; a group over budget gets one final round and is reported `failed(budget)`), `--timeout` per task. Rounds after the first re-inject confirmed findings as `{{confirmed_comments}}` and drop the plan, because "a plan tends to act as a coverage ceiling".

### 1.8 The run manifest is the benchmark record

`RunManifest` (`internal/session/manifest.go:231`) holds five **disjoint** file sets — `selected`, `completed`, `reused`, `failed`, `waived` — with `selected` the denominator and equal to their union. It also freezes the `resolved_base` / `resolved_head` SHAs and records `ocr_version`, provider, model, concurrency, plus hashes of the rule and runtime config. That is a reproducibility identity we do not have to invent: **coverage completeness = completed+reused / selected**, and two runs are comparable only if those values match.

---

## 2. AACR-Bench: what it measures and how

### 2.1 Corpus — verified, not quoted

The published counts are close to the docs but not identical; measured from the committed data:

| Quantity | Docs | Measured | Source |
|---|---|---|---|
| unique PRs | 200 | **200** (union of positive+negative) | both JSON files |
| positive comments | 1,505 | **1,506** | `dataset/positive_samples.json` |
| negative comments | 640 | **639** | `dataset/negative_samples.json` |
| total comments | 2,145 | **2,145** | 1,506 + 639 |
| positive PR records | — | 196 | `dataset/positive_samples.json` |
| languages | 10 | **10** (C++ 64, TS 55, Java 48, Go 40, Python 36, C 35, JS 20, Rust 19, PHP 18, C# 16) | grouped by `project_main_language` |
| integrity | — | sha256 `02c8561f…2097` matches `positive_samples.meta.json` | `hashlib.sha256` |

Corrections to the marketing numbers: README says "1,505 annotated ground-truth issues"; the committed file has **1,506** positive comments and **639** (not 640) negative ones. Also note the **2,145 is the reflection test set** (positive + negative), not the review corpus — precision/recall in the benchmark use the 1,506 positives as `expected_notes` and the model's own output as the other denominator.

Comment schema: `path`, `from_line`, `to_line`, `side`, `note`, `category` (`Code Defect` / `Maintainability and Readability` / `Performance` / `Security Vulnerability`), `context` (`Diff Level` / `File Level` / `Repo Level`), `is_ai_comment`, `source_model`. 1,114/1,506 positives are AI-authored.

### 2.2 Annotation

GitHub human comments → LLM enhancement → expert multi-round cross-annotation → consistency validation; 80+ engineers, 3 rounds. The paper's claim is a **285% increase in defect coverage** over raw PR comments — i.e. the labels are deliberately richer than what a human reviewer said at the time, which is why recall against them is hard.

### 2.3 Current metrics (default)

Group by normalized path; **side and line are context only**. One judge call per generated comment against *all* reference comments of that file; one-to-many coverage allowed; judge prompt says "ignore differences in wording, tone, formatting, or exact line numbers" (`evaluation/judge.py:307`).

```
precision = matched_generated / generated      # greedy: only comments covering ≥1 *previously uncovered* GT
recall    = covered_reference / expected       # deduped
balanced  = 2PR / (P + R)                      # 0 if P+R = 0
```

`hit_new_gt` is the precision gate: re-covering an already-covered reference is a duplicate and does **not** count. `missing_instances` are excluded from all denominators.

The README's **F1** is this `balanced_score` — the same harmonic mean, renamed in the docs. Usage is reported as `total/avg_duration_seconds` and `total_input_tokens` / `total_output_tokens` / `total_tokens` with `avg_*` variants; they are per-run aggregates, not per-comment costs.

### 2.4 Legacy metrics (deprecated)

`path → side → line(k=1) → semantic`, one-to-one with global consumption, yielding semantic/line P/R/F1. Deprecated because: one generated comment often covers several issues and was credited once; pairwise judging penalizes multi-issue comments; and `line(k)` **discarded correct comments before the judge saw them**. Migration doc: `docs/metric-migration.md`.

This is the most transferable lesson for us: do not build a benchmark whose location gate can silently delete true positives.

### 2.5 What is not reproducible from the repo

The headline table ("higher Precision and F1, ~1/9 tokens, lower Recall") has **no result artifacts** in `alibaba/aacr-bench` — only the dataset, the harness and images. It is a vendor claim until someone runs the harness with a judge. `[UNVERIFIED]`

---

## 3. Mapping onto this repo

| OCR mechanism | Local analogue today | Measured effect here |
|---|---|---|
| six-gate selection | none | 103/197 files, 25,542/54,570 lines selected; 41 test files and 31 md files invisible |
| `include` bypass | none | would be the only way to review `*_test.go` |
| 4-layer rule chain | **no `.opencodereview/rule.json`** | system rules only; 12/103 selected files fall to `default.md` |
| rule text "don't duplicate vet/staticcheck/gofmt" | `go vet` passes, **gofmt/staticcheck not in CI** | the model is told to skip what CI does not run `[INFERENCE]` |
| grouping (≤10, metadata only) | n/a | our instances are commit-sized (median 3 selected files) — grouping rarely fires |
| line anchoring via `existing_code` | review reports use `path:line` prose | any OCR output must carry `existing_code` or it lands unanchored |
| reflection (precision pass) | our triage is human/agent judgement | `--no-filter` gives a free A/B if a model is ever run |
| run manifest coverage | `results.tsv` + `scripts/metric.py` | the repo already has the ledger + frozen-allowlist pattern |

### 3.1 Selection coverage, measured authoritatively

`ocr review --preview --format json --from main --to HEAD` (no LLM, 4.6 s):

```
total_files 197   total_insertions 54510   total_deletions 60
reviewable_count 103   excluded_count 94
unsupported_ext 46   default_path 46   binary 2
```

| Scope | Files selected | Lines selected | Files total | Lines total |
|---|---|---|---|---|
| whole range | 103 (52.3%) | 25,542 (46.8%) | 197 | 54,570 |
| product code (`daemon`, `emulator`, `dashboard`, `deploy`) | 82 (61.7%) | 20,700 (51.9%) | 133 | 39,893 |

Blind spots by category (all `will_review: false`):

| Category | Files | Lines | Share of range churn |
|---|---|---|---|
| tests (`*_test.go`, `*.test.ts(x)`) | 41 | 13,965 | 25.6% |
| markdown | 31 | 14,426 | 26.4% |
| binary (2 PNG screenshots) | 2 | 0 | — |

An independent re-implementation of the six gates agreed with the CLI on **189/197** files; the 8 disagreements were exactly the extensionless files (6) and two PNGs I had mis-bucketed as `unsupported_ext` instead of `binary`. The CLI's number is the one to trust.

### 3.2 Rule coverage, measured

`ocr delegate rule` over the 103 selected paths (batched, no LLM):

| Resolved rule | Files |
|---|---|
| `**/*.go` → `go.md` | 40 |
| `**/*.{ts,js,tsx,jsx,mjs,cjs}` | 32 |
| `**/*.{py,pyi,ipynb}` | 11 |
| `**/*.{yaml,yml}` + workflow + `package.json` + `*.json` | 8 |
| **`default`** | **12** |

91/103 = **88.3% language-aware**. The 12 fallbacks include `dashboard/src/styles/app.css` — **3,518 lines, 13.8% of all selected churn** — plus 4 shell scripts, 2 Dockerfiles, `index.html`, `LICENSE`, `.gitattributes` and 2 `.gitignore`. There is no CSS rule and no shell rule in the system set.

### 3.3 Where our existing review loop already matches the method

`research/79-code-review/REPORT.md` is a real instance of the target loop, at `b75c1c3`:

| Fact | Value | Source |
|---|---|---|
| scope | daemon, 7,907 non-test Go lines, 11 package dirs (10 with tests), 153 test functions | `git ls-tree` at `b75c1c3` |
| findings | 12 (1 high, 5 medium, 6 low) | report table |
| yield | **1.52 findings/KLOC** | 12 / 7.907 |
| evidence discipline | 7 commands recorded, arithmetic repro for the overflow | report "Commands run" |
| disposition | **12/12 claimed fixed**, 11/12 with a named regression test | `daemon/README.md` "Review fixes (AJS-53)" |
| turnaround | same day (review and fixes both 2026-10-23) | git dates |

Spot-check at HEAD, independent of that table: `daemon/internal/supervisor/supervisor.go:283` is `cmd.Env = childEnv(spec.Env)` (F1) and `daemon/internal/api/server.go:533-538` clamps `offset`/`limit` before adding (F2). Two of twelve re-verified by reading; the other ten rest on the remediation table. `[UNVERIFIED]` for those ten.

Caveat that must travel with the number: the reviewer and the fixer are the same project, so 12/12 is an **acceptance rate under self-review**, not an independent precision measurement. n=1.

### 3.4 Differences that matter

- **No labeled PR corpus.** 51 commits total, 27 of them code-bearing for `.go/.ts/.tsx/.css`, all within 2026-10-23..24. There is no PR review history to mine.
- **No remote.** `git remote -v` is empty; AACR's `prepare_repo` does `git clone <clone_url>`, so it needs a `file://` URL or a local bare mirror.
- **Tests are invisible.** In a repo where daemon test churn (11,485) nearly equals source churn (12,399), the default filter removes a quarter of the change. A Go+TS benchmark must decide this explicitly, not inherit it.
- **`research/` is 18.4% of selected churn** (4,690/25,542 lines: 12 research files + 6 scripts). Left in, it inflates coverage and dilutes review yield.

---

## 4. Benchmark plan

### 4.0 Instance model

An **instance** = one commit (or one range) plus a frozen scope. Measured corpus, `main..HEAD`:

| Quantity | Value |
|---|---|
| commits | 49 (31 touch code, 18 docs-only) |
| code-bearing instances for `.go/.ts/.tsx/.css` | **27** |
| changed files per instance | median 6, p90 23, max 49 |
| **selected files per instance** | median **3**, p90 13, max 40 |
| **selected lines per instance** | median **182**, p90 948, max 8,703 |
| bulk-import outliers (>3,000 selected lines) | **2** (`cd57483` 8,703, `8bb0d4b` 5,913; next largest is 948) |

Rules: exclude the 2 bulk imports (they are imports, not changes), exclude `research/` and `scripts/` from the primary scope, keep `deploy/`. That leaves **25 product instances**; add 3 range instances (`main..HEAD`, daemon-only, dashboard-only) for integration-level review.

Instance identity = `base_commit` + `head_commit` + scope glob + `ocr_version`. Freeze all four in the ledger.

### 4.1 B0 — deterministic, no LLM, runs today

| ID | Check | Command (cwd) | Measured now |
|---|---|---|---|
| B0.1 | change-set profile | `git diff --numstat <base>..<head>` | 197 files / 54,570 lines |
| B0.2 | selection coverage | `ocr review --preview --format json --from <base> --to <head>` (`ocr/`) | 103 files / 25,542 lines |
| B0.3 | rule coverage | `ocr delegate rule <selected paths>` | 91/103 language-aware |
| B0.4 | Go gate | `go vet ./... && go test -count=1 -coverprofile=<tmp> ./...` (`daemon/`, `emulator/`) | vet clean; 72.3% / 63.0% |
| B0.5 | TS gate | `npm test` (`dashboard/`) | 71/71 in 11 files, 6.19 s |
| B0.6 | typecheck | `tsc -p tsconfig.app.json --noEmit --incremental false` (`dashboard/`) | not run — would rewrite `tsbuildinfo` `[UNVERIFIED]` |
| B0.7 | format | `gofmt -l` **on an LF checkout** (HMC), not on Windows | 1 real defect + 1 CRLF-only hit on Windows — see F6 |
| B0.8 | conformance gate | `python scripts/metric.py` (root) | existing 8/8 allowlist gate |
| B0.9 | coverage completeness | read `coverage.selected` vs `completed+reused` from the run manifest | requires a real run |

B0 is the floor: it costs seconds, needs no key, and every later number is only comparable if B0 passes on the same commit. Measured on this host: B0.2 4.6 s, B0.4 3.3 s (daemon) + 1.3 s (emulator), B0.5 7.1 s — about 16 s plus `vet`, which is cheap enough to run per instance.

### 4.2 B1 — review without a vendor key (delegation mode)

`ocr delegate preview --from <base> --to <head>` emits the deterministic spec (mode, merge-base, totals, per-file `+/-` with exclusion reasons). A host agent — this harness — reviews that spec and writes findings. Then run the triage ledger:

| Field | Allowed values |
|---|---|
| `verdict` | `CONFIRMED` (reproduced by a command), `FALSE` (contradicted by code/output), `UNPROVEN` (no deterministic check exists) |
| `evidence` | the exact command or `path:line` that decides it |
| `cost` | commands + wall minutes spent adjudicating |
| `disposition` | fixed / accepted-risk / duplicate / out-of-scope |

Metrics from B1: **review yield** (findings/KLOC of selected lines), **confirmed rate** = `CONFIRMED/(CONFIRMED+FALSE)`, **unproven rate** (the part of the review that cannot be adjudicated deterministically), **triage cost per finding**. This is the only tier that produces review signal today, and it is label-free: the repo's own code is the oracle.

### 4.3 B2 — AACR-compatible (blocked)

The harness already accepts a local repo (`reviewers/ocr.py` passes `--repo <path>`, `--format json`, `--audience agent`, `--max-tools`, `--concurrency`, `--timeout`). The instance format is small:

```json
{"instance_id":"loveplus@2eee11a","repo":"local/loveplus-speed-to-proxy",
 "base_commit":"<sha>","head_commit":"<sha>","clone_url":"file:///D:/Project/LovePlusSpeedToProxy",
 "reference_comments":[{"path":"daemon/internal/api/server.go","start_line":514,"end_line":530,
                        "side":"right","text":"…"}]}
```

Then: `python -m converters.<ours> --validate` → `python -m pipeline run --stage all --reviewer ocr --dataset data/loveplus.jsonl --run-id b2-001`.

**Trap:** the file-level metric has no "unlabeled" mode. A generated comment in a file with no reference comments gets `matched_gt_indices: []`, contributes to the precision denominator, and never invokes the judge. Feeding unlabeled instances therefore reports **precision 0**, not "unknown". B2 without labels is worse than no B2.

Two experiments are worth designing once a model is allowed: `--no-filter` A/B (reflection's effect on precision and volume) and `--effort low|medium|high` (rounds vs recall). Both are single-flag ablations on the same instances.

### 4.4 Ledger and anti-gaming

Follow the existing precedent, not a new one:

- `results.tsv` is an append-only `commit / metric / status / description` log — reuse its shape.
- `scripts/metric.py` freezes an **allowlist** of 8 test names so the autoresearch loop "cannot game the metric by writing more tests". The review benchmark needs the same: a frozen instance list and a frozen denominator. Adding files or tests must never raise the score; report `selected` and `completed+reused` separately so a run cannot look complete by reviewing less.

---

## 5. Proxy metrics runnable now

All values measured in this session on the local Windows host (`go1.26.7 windows/amd64`, `node v24.19.0`, `ocr v1.13.3`).

| Metric | Formula | Value | Command | Caveat |
|---|---|---|---|---|
| change-set size | `git numstat` | 197 files / 54,570 lines | `git diff --numstat main..HEAD` | includes research |
| **file coverage** | selected / changed | **103/197 = 52.3%** (product 82/133 = 61.7%) | `ocr review --preview --format json` | default filters |
| **line coverage** | selected churn / total churn | **25,542/54,570 = 46.8%** (product 20,700/39,893 = 51.9%) | same | same |
| test blind spot | test files excluded | 41 files / 13,965 lines = **25.6%** | preview + numstat | all excluded |
| **rule coverage** | language rule / selected | **91/103 = 88.3%** | `ocr delegate rule` | 12 fall to `default.md` |
| **test pass** | — | Go **229 + 32** functions, TS **71/71** | `go test ./...`, `npm test` | static counts match runtime |
| **statement coverage** | `go tool cover` | daemon **72.3%**, emulator **63.0%** | `go test -coverprofile` | dashboard n/a |
| weakest package | per-package coverage | `internal/store` **59.8%** at 2,409 lines; `cmd/ajs-accountd` 3.9%; `emulator/cmd/ajsproxy` 0.0% | `go tool cover -func` | 25 zero-covered store funcs |
| test density | test funcs / KLOC src | daemon 23.1, emulator 8.2, dashboard 11.6 | static count | dashboard = TS/TSX only |
| static gate | vet | 2/2 modules clean | `go vet ./...` | gofmt excluded (F6) |
| **review yield** | findings / KLOC reviewed | **1.52** (12 / 7.907) | `research/79-code-review/REPORT.md` | n=1, daemon only |
| **acceptance** | fixed / findings | **12/12** | `daemon/README.md` AJS-53 table | self-acceptance, biased |
| fix coverage | findings with regression test | **11/12** | same | F10 wiring only |
| triage cost | commands per review | 7 | report "Commands run" | one sample |
| risk-weighted surface | churn × (1−coverage) | top 5 of 6 are `internal/store/*` | numstat + cover | measurement only |

Risk weighting in one line: `daemon/internal/store` is 2,409 lines, 59.8% covered, 25 functions at 0% (`nextEgressIP`, `DrainNode`, `UndrainNode`, `ProbeNode`, …), and owns 5 of the 6 highest `churn × (1−coverage)` files in the selected set. Any benchmark that reports a single aggregate number will hide exactly this.

---

## 6. What cannot be measured without labeled PRs or an LLM

| Metric | Why blocked | What it would take |
|---|---|---|
| precision / recall / F1 | needs reference comments; unlabeled instances score precision 0 | 20–30 expert-labeled comments on 5–10 frozen instances |
| line precision | current metric ignores lines; legacy mode is deprecated | run legacy mode deliberately, or score `start_line` distance offline |
| tokens per review | no model run | one OCR run with `--format json` + manifest usage |
| wall-clock per review | same | same |
| reflection intercept rate | needs `comments.before` / `comments.filtered` from a real run | one run + `--no-filter` A/B |
| coverage completeness | needs `manifest.coverage` | same |
| per-language (Go vs TS) cells | 25 instances spread across Go and TS; per-cell CIs are too wide for claims | more instances or accept descriptive-only reporting |
| context-level (diff/file/repo) breakdown | requires labels carrying `context` | labels |
| cross-model comparison | needs ≥2 models and a judge | keys + judge |

Even with labels, n≈25 instances cannot support an F1 claim at AACR's scale (200 PRs). Report medians and per-instance tables, not a single harmonic mean.

---

## 7. Findings

| # | Sev | Location | Issue | Evidence | Proposed fix | Risk |
|---|---|---|---|---|---|---|
| F1 | **P1** | `.github/workflows/release.yml:1` | The only workflow builds and pushes images; **no test job exists**. 261 Go tests and 71 TS tests are never executed on push. | `ls .github/workflows` → 1 file; grepping it for `go test`, `vitest` or `npm test` → no match | Add `ci.yml`: `go vet` + `go test -race -count=1 ./...` (both modules) and `npm test` in `dashboard/` on push/PR | CI minutes only; `-race` on Windows runners is slow — run on ubuntu-latest |
| F2 | **P1** | upstream `internal/agent/selection.go:97` applied to this repo | Default selection excludes **every** test file: 41 files / 13,965 lines = 25.6% of the change set. A "review coverage" number computed from OCR defaults is therefore not coverage of the change. | `ocr review --preview` → 41 test files `will_review:false`, reason `default_path` | Decide explicitly: add `include: ["**/*_test.go","**/*.test.tsx","**/*.test.ts"]` to a project rule, **or** declare tests out of scope and report the 25.6% blind spot beside every coverage number | Test diffs are noisy; reviewing them will lower precision unless the benchmark scores them separately |
| F3 | **P1** | `.opencodereview/rule.json` (absent) | No project rules. The system Go rule tells the model **not** to duplicate `go vet`, Staticcheck, `go test -race`, `gofmt` — and none of those run in CI (F1). The repo's real invariants (loopback-only listeners, no credentials in child env, one FV session per account) are not encoded anywhere. | `ls -a .opencodereview` → absent; `ocr delegate rule daemon/internal/store/store.go` → `system / **/*.go` only | Add 4–6 project rules: store lock discipline, env allowlist, loopback-only, FV session cardinality, "tests must accompany behaviour change" | Rule text drifts from code; review the rules like code |
| F4 | P2 | `dashboard/src/styles/app.css:1` | 3,518 lines (13.8% of selected churn) resolve to `default.md`; there is no CSS rule. 4 shell scripts and 2 Dockerfiles also fall back. | `ocr delegate rule` → 12 files under `system / default` | Add a project rule for `**/*.css` (or explicitly waive CSS from the benchmark denominator) | Generic checklist on CSS produces style noise |
| F5 | P2 | `dashboard/package.json:1` | No coverage provider in devDependencies, so TS statement coverage is unmeasurable while Go coverage is not. | `ls node_modules/@vitest` → no `coverage-v8`; `npm test` reports counts only | Add `@vitest/coverage-v8` + `test:coverage` script | One dev dependency; lockfile churn |
| F6 | P2 | `daemon/internal/runtime/nodes.go:103` | No format gate runs, so the tree carries both a real gofmt defect and CRLF noise. `nodes.go` has a genuinely misaligned field (`Logger  *slog.Logger`); `nodes_test.go` is flagged only because the worktree is CRLF (`i/lf w/crlf`, `core.autocrlf=true`, and `.gitattributes` pins only `.sh/.py/.bpf`). | after `tr -d '\r'`, `gofmt` still rewrites `nodes.go:103` but leaves `nodes_test.go` unchanged | `gofmt -w` the one file; pin `*.go text eol=lf`; add `gofmt -l` to CI on Linux | A mass re-checkout if the eol rule changes; split into its own commit |
| F7 | P2 | repo root (no remote) | AACR's `prepare_repo` clones `clone_url`; this repo has no remote, and its history is 2 days / 51 commits. The upstream harness cannot consume it as-is. | `git remote -v` → empty; `git rev-list --count HEAD` → 51 | Create a local bare mirror and use `file:///…` as `clone_url`; write a converter that emits the §4.3 JSONL | None (read-only mirror) |
| F8 | P2 | `daemon/internal/store/store.go:1` | Coverage is inversely weighted to risk: the largest package is the least covered (59.8%, 2,409 lines, 25 zero-covered functions), while `cmd/ajs-accountd` sits at 3.9% and `emulator/cmd/ajsproxy` at 0.0%. | `go test -coverprofile` + `go tool cover -func` | Report `churn × (1−coverage)` per file; set a package floor (e.g. 70%) before adding new store surface | A floor can block legitimate refactors; apply to changed lines only |
| F9 | P2 | `research/`, `scripts/` | 18.4% of selected churn (4,690/25,542 lines) is research notes and lab scripts. Included by default, they inflate coverage and dilute yield. | preview: research 12 files / 3,087 lines, scripts 6 / 1,603 selected | Freeze instance scope to `daemon/`, `emulator/`, `dashboard/`, `deploy/`; report research/scripts as a separate, unscored bucket | Scoping out `scripts/debug` could hide real tooling defects; keep it as a secondary bucket |

No P0: nothing here is release-blocking, and this wave changed no code.

---

## 8. What I could not check

- `tsc --noEmit` (B0.6): not run, because `tsc -b`/composite mode would rewrite `dashboard/tsconfig.app.tsbuildinfo` in a read-only wave. `[UNVERIFIED]`
- Dashboard statement coverage: provider not installed, and installing it is a dependency change. `[UNVERIFIED]`
- Staticcheck: not installed locally (`$GOPATH/bin` has no `staticcheck`). `[UNVERIFIED]`
- Any real review run (`ocr review` without `--preview`): would need an LLM endpoint; explicitly out of scope (no vendor calls).
- The AACR headline table (precision/F1/1-9 tokens): no result artifacts published. `[UNVERIFIED]`
- B0.7 (`gofmt`) on an LF checkout: not run — the local worktree is CRLF, which is the reason for F6.
- Emulator/dashboard runtime behaviour: out of lane; see `research/82-live-20261023/REPORT.md`.

## 9. Next action

1. Add `.github/workflows/ci.yml` (F1) — without it every benchmark number is a snapshot of an unguarded tree.
2. Freeze the 25 product instances and run B0.1–B0.5 + B0.8 to produce `results.tsv` baseline rows.
3. Decide F2 (tests in or out) before publishing any coverage percentage; the 25.6% blind spot must appear beside it either way.
4. Label 5 instances (~25 comments) from the existing 12-finding review + fresh B1 passes; that is the minimum that makes B2 precision meaningful rather than zero.

---

## Appendix A — commands and raw results

Host: local Windows 10.0.26300, `go1.26.7 windows/amd64`, `node v24.19.0`, `ocr v1.13.3 (375f64c)`. No HMC access used.

| # | Command (cwd) | Result |
|---|---|---|
| 1 | `git status --porcelain` / `git log -5 --oneline` (root) | clean at session start; HEAD `2eee11a`. A concurrent lane later modified `README.md` — not this lane |
| 2 | `git diff --numstat main..HEAD` | 197 files, 54,510 ins, 60 del |
| 3 | `ocr review --preview --format json --from main --to HEAD` (root) | 103 reviewable / 94 excluded of 197 |
| 4 | `ocr review --preview --format json --commit 2eee11a` | 7/12 files, 354 lines |
| 5 | `ocr delegate preview --from main --to HEAD` | mode range, merge-base `ff34a47`, 103/197 |
| 6 | `ocr delegate rule <103 selected paths>` | 40 Go, 32 TS, 11 Py, 8 config, 12 default |
| 7 | `go test -count=1 -coverprofile=… ./...` (`daemon/`) | 10 pkgs ok, total 72.3% |
| 8 | `go test -count=1 -coverprofile=… ./...` (`emulator/`) | 4 pkgs ok, total 63.0% |
| 9 | `go vet ./...` (`daemon/`, `emulator/`) | exit 0 both |
| 10 | `gofmt -l daemon emulator` (root) | 2 files: 1 real (`nodes.go:103`), 1 CRLF-only (`nodes_test.go`) |
| 11 | `npm test` (`dashboard/`) | 11 files, 71 tests, 6.19 s |
| 12 | `grep -rhoE "^func Test…"` + `it(` counts | 229 + 32 Go, 71 TS |
| 13 | AACR dataset download + count + sha256 | 196/1,506 pos, 155/639 neg, 200 PRs, sha256 matches meta |

## Appendix B — upstream sources read

- `README.md`, `pages/src/content/docs/en/{architecture,review-rules}.md`
- `internal/agent/{selection,grouping,preview,agent}.go`, `internal/diff/{resolver,relocation}.go`, `internal/session/manifest.go`, `internal/model/review.go`
- `internal/config/template/prompts/{review_filter_task_system,re_location_task_user,main_task_system}.md`
- `alibaba/aacr-bench`: `README.md`, `docs/{metrics,metric-migration}.md`, `evaluation/{README.md,judge.py,reviewers/ocr.py,repo_utils.py}`, `dataset/*.json`
