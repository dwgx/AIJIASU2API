#!/usr/bin/env python3
"""Validate the AJS-14 reverse-engineering playbook deliverables.

Checks
  1. REPORT.md / PLAYBOOK.md / TOOLS.tsv exist.
  2. REPORT.md has the six required sections.
  3. The method inventory has >= 20 distinct IDs, each with a detail block
     carrying When / Run / Proves / Fails / Safety, and each block's tool line
     contains a command (a backtick span or a fenced block).
  4. The per-platform matrix covers Windows, Linux and macOS (3 platforms).
  5. TOOLS.tsv has the five required columns and >= 15 well-formed rows.
  6. PLAYBOOK.md has >= 10 goal sections and a "When not to patch" section.

Prints exactly one summary line on success:
  OK methods=<N> platforms=3 tools=<M>

Exit code 0 on success, 1 on any failure. No network, no vendor binary.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent
REPORT = ROOT / "REPORT.md"
PLAYBOOK = ROOT / "PLAYBOOK.md"
TOOLS = ROOT / "TOOLS.tsv"

REQUIRED_SECTIONS = [
    "## Answer",
    "## Method inventory",
    "## Per-platform matrix",
    "## AJiaSu anchors",
    "## Safety and rollback",
    "## Open questions",
]
DETAIL_LABELS = ["**When:**", "**Run:**", "**Proves:**", "**Fails:**", "**Safety:**"]
STATUS_VALUES = {"ran", "doc", "no-install"}
PLATFORM_TOKENS = {"windows", "linux", "macos", "all", "python"}
TOOL_COLUMNS = ["tool", "platform", "purpose", "command", "risk"]

errors: list[str] = []


def fail(msg: str) -> None:
    errors.append(msg)


def read(path: Path) -> str:
    if not path.is_file():
        fail(f"missing file: {path.name}")
        return ""
    return path.read_text(encoding="utf-8")


def section(text: str, heading: str) -> str:
    """Return the body of a level-2 section, stopping at the next level-2 heading.

    The heading must start a line: the same string may also appear inline
    (backticked) in the Answer section.
    """
    m = re.search(r"^" + re.escape(heading) + r"\s*$", text, re.MULTILINE)
    if not m:
        return ""
    body_start = text.find("\n", m.end())
    if body_start < 0:
        return ""
    nxt = re.search(r"^## ", text[body_start + 1 :], re.MULTILINE)
    if not nxt:
        return text[body_start + 1 :]
    return text[body_start + 1 : body_start + 1 + nxt.start()]


def parse_inventory(text: str) -> dict[str, dict[str, str]]:
    body = section(text, "## Method inventory")
    rows: dict[str, dict[str, str]] = {}
    for line in body.splitlines():
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) != 6:
            continue
        mid, layer, method, tool, proves, status = cells
        if not re.fullmatch(r"[A-Z]\d{2}", mid):
            continue
        if status not in STATUS_VALUES:
            fail(f"inventory {mid}: bad status {status!r}")
        if mid in rows:
            fail(f"inventory: duplicate id {mid}")
        rows[mid] = {
            "layer": layer,
            "method": method,
            "tool": tool,
            "proves": proves,
            "status": status,
        }
    return rows


def parse_details(text: str) -> dict[str, str]:
    body = section(text, "## Method detail")
    blocks: dict[str, str] = {}
    matches = list(re.finditer(r"^### ([A-Z]\d{2})\b.*$", body, re.MULTILINE))
    for i, m in enumerate(matches):
        end = matches[i + 1].start() if i + 1 < len(matches) else len(body)
        mid = m.group(1)
        if mid in blocks:
            fail(f"detail: duplicate block {mid}")
        blocks[mid] = body[m.end() : end]
    return blocks


def check_report(text: str) -> tuple[int, int]:
    for heading in REQUIRED_SECTIONS:
        if heading not in text:
            fail(f"REPORT.md missing section {heading!r}")

    inventory = parse_inventory(text)
    details = parse_details(text)

    if len(inventory) < 20:
        fail(f"inventory has {len(inventory)} methods, need >= 20")
    if set(inventory) != set(details):
        only_inv = sorted(set(inventory) - set(details))
        only_det = sorted(set(details) - set(inventory))
        if only_inv:
            fail(f"methods without detail block: {only_inv}")
        if only_det:
            fail(f"detail blocks without inventory row: {only_det}")

    for mid, block in details.items():
        for label in DETAIL_LABELS:
            if label not in block:
                fail(f"{mid}: missing {label}")
        if "```" not in block and "`" not in block:
            fail(f"{mid}: no command (backtick span or fenced block)")

    matrix = section(text, "## Per-platform matrix")
    if not matrix:
        fail("per-platform matrix section is empty")
    else:
        lowered = matrix.lower()
        for token in ("windows", "linux", "macos"):
            if token not in lowered:
                fail(f"matrix does not mention {token}")
        header = re.search(r"^\|\s*Method\s*\|\s*W\s*\|\s*L\s*\|\s*M\s*\|", matrix, re.MULTILINE)
        if not header:
            fail("matrix header must be '| Method | W | L | M |'")
        rows = [l for l in matrix.splitlines() if l.startswith("|") and "|" in l]
        if len(rows) < 21:
            fail(f"matrix has {len(rows)} table lines, expected >= 21")

    anchors = section(text, "## AJiaSu anchors")
    for needle in ("0x28a030", "0x314468", "code=200", "OPV2", "[CONFIG]", "half-close"):
        if needle not in anchors:
            fail(f"anchors table missing {needle!r}")

    return len(inventory), 3


def check_tools(text: str) -> int:
    lines = [l for l in text.splitlines() if l.strip()]
    if not lines:
        fail("TOOLS.tsv is empty")
        return 0
    header = lines[0].split("\t")
    if header != TOOL_COLUMNS:
        fail(f"TOOLS.tsv header is {header}, expected {TOOL_COLUMNS}")
    count = 0
    for i, line in enumerate(lines[1:], start=2):
        cells = line.split("\t")
        if len(cells) != 5:
            fail(f"TOOLS.tsv line {i}: {len(cells)} columns")
            continue
        if any(not c.strip() for c in cells):
            fail(f"TOOLS.tsv line {i}: empty cell")
            continue
        tool, platform, purpose, command, risk = cells
        tokens = {t.strip().lower() for t in re.split(r"[,/ ]+", platform) if t.strip()}
        if not tokens or not tokens <= PLATFORM_TOKENS:
            fail(f"TOOLS.tsv line {i}: unknown platform {platform!r}")
        if not risk.split(" ")[0] in {"none", "low", "med", "high"}:
            fail(f"TOOLS.tsv line {i}: risk must start with none/low/med/high")
        count += 1
    if count < 15:
        fail(f"TOOLS.tsv has {count} tool rows, need >= 15")
    return count


def check_playbook(text: str) -> None:
    goals = re.findall(r"^## G\d+ ", text, re.MULTILINE)
    if len(goals) < 10:
        fail(f"PLAYBOOK.md has {len(goals)} goal sections, need >= 10")
    if "When not to patch" not in text:
        fail("PLAYBOOK.md missing 'When not to patch'")
    for token in ("SSLKEYLOGFILE", "LD_PRELOAD", "Stalker", "mitmdump", "half-close", "0x28a030"):
        if token not in text:
            fail(f"PLAYBOOK.md missing {token!r}")


def main() -> int:
    report = read(REPORT)
    playbook = read(PLAYBOOK)
    tools = read(TOOLS)

    methods = platforms = 0
    if report:
        methods, platforms = check_report(report)
    if tools:
        tool_count = check_tools(tools)
    else:
        tool_count = 0
    if playbook:
        check_playbook(playbook)

    if errors:
        for e in errors:
            print(f"FAIL {e}")
        print(f"FAILED methods={methods} platforms={platforms} tools={tool_count}")
        return 1

    print(f"OK methods={methods} platforms={platforms} tools={tool_count}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
