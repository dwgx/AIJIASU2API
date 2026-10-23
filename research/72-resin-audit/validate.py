#!/usr/bin/env python3
"""Validate the Resin v1.3.0 audit deliverables.

Checks, against REPORT.md next to this script:
  * first line is the required title;
  * the Verdicts table has at least 10 numbered rows;
  * every row carries one of the four verdict tokens;
  * the Integration risks section exists.

On success prints exactly: OK claims=<N> overturned=<M>  and exits 0.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

REPORT = Path(__file__).resolve().with_name("REPORT.md")
TITLE = "# Resin v1.3.0 audit"
VERDICT_TOKENS = ("[CONFIRMED]", "[WEAKENED]", "[OVERTURNED]", "[UNKNOWN]")
MIN_CLAIMS = 10


def fail(message: str) -> None:
    print(f"FAIL: {message}", file=sys.stderr)
    raise SystemExit(1)


def section(lines: list[str], heading: str) -> list[str] | None:
    """Return the body lines of a '## heading' section, or None."""
    start = None
    for i, line in enumerate(lines):
        if line.strip() == heading:
            start = i + 1
            break
    if start is None:
        return None
    end = len(lines)
    for j in range(start, len(lines)):
        if lines[j].startswith("## "):
            end = j
            break
    return lines[start:end]


def main() -> None:
    if not REPORT.is_file():
        fail(f"missing report: {REPORT}")

    text = REPORT.read_text(encoding="utf-8")
    lines = text.splitlines()
    if not lines or lines[0].strip() != TITLE:
        fail(f"first line must be {TITLE!r}")

    verdicts = section(lines, "## Verdicts")
    if verdicts is None:
        fail("missing '## Verdicts' section")

    rows: list[tuple[int, str]] = []
    for line in verdicts:
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 3:
            continue
        if not cells[0].isdigit():
            continue  # header and separator
        rows.append((int(cells[0]), line))

    claims = len(rows)
    if claims < MIN_CLAIMS:
        fail(f"Verdicts has {claims} numbered rows, need >= {MIN_CLAIMS}")

    numbers = [n for n, _ in rows]
    if numbers != list(range(1, claims + 1)):
        fail(f"Verdicts row numbers are not 1..{claims}: {numbers}")

    for n, line in rows:
        tokens = [t for t in VERDICT_TOKENS if t in line]
        if len(tokens) != 1:
            fail(f"row {n} must carry exactly one verdict token, found {tokens}")

    overturned = sum(1 for _, line in rows if "[OVERTURNED]" in line)

    if section(lines, "## Integration risks") is None:
        fail("missing '## Integration risks' section")

    print(f"OK claims={claims} overturned={overturned}")


if __name__ == "__main__":
    main()
