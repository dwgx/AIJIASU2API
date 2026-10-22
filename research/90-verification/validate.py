#!/usr/bin/env python3
"""Acceptance check for research/90-verification/VERDICTS.md.

Fails unless the report has the required shape and at least 15 verdict rows.
Prints `OK verdicts=<N>` and exits 0 on success.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

REPORT = Path(__file__).with_name("VERDICTS.md")
VERDICT_RE = re.compile(r"^(CONFIRMED|WEAKENED|OVERTURNED|\[UNKNOWN\])\b")
MIN_VERDICTS = 15


def fail(msg: str) -> int:
    print(f"FAIL {msg}")
    return 1


def main() -> int:
    if not REPORT.is_file():
        return fail(f"missing {REPORT}")
    text = REPORT.read_text(encoding="utf-8")
    lines = text.splitlines()

    if not lines or lines[0].strip() != "# Independent verification \u2014 wave 1":
        return fail("first line is not '# Independent verification \u2014 wave 1'")

    for section in ("## Summary", "## Verdicts", "## Contradictions", "## Self-audit"):
        if section not in text:
            return fail(f"missing section {section!r}")

    # Verdict rows: table rows inside the Verdicts section, four cells, verdict token in cell 3.
    start = lines.index("## Verdicts")
    end = next(
        (i for i in range(start + 1, len(lines)) if lines[i].startswith("## ")),
        len(lines),
    )
    rows = 0
    for line in lines[start + 1 : end]:
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) != 4:
            continue
        if not cells[0].isdigit():
            continue  # header / separator
        if not VERDICT_RE.match(cells[2]):
            return fail(f"row {cells[0]} has no verdict token: {cells[2]!r}")
        if not cells[3]:
            return fail(f"row {cells[0]} has no evidence")
        rows += 1

    if rows < MIN_VERDICTS:
        return fail(f"only {rows} verdict rows, need >= {MIN_VERDICTS}")

    evidence = Path(__file__).with_name("evidence")
    if not evidence.is_dir() or not any(evidence.iterdir()):
        return fail("evidence/ is missing or empty")

    print(f"OK verdicts={rows}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
